// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package caasoperator

import (
	"fmt"

	"github.com/juju/errors"
	"gopkg.in/juju/charm.v6-unstable"
	"gopkg.in/juju/names.v2"

	"github.com/juju/juju/api/common"
	apiwatcher "github.com/juju/juju/api/watcher"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/status"
	"github.com/juju/juju/watcher"
)

// This module implements a subset of the interface provided by
// state.CAASApplication, as needed by the caasoperator API.

// CAASApplication represents the state of an application.
type CAASApplication struct {
	st   *State
	tag  names.ApplicationTag
	life params.Life
}

// Tag returns the application's tag.
func (s *CAASApplication) Tag() names.ApplicationTag {
	return s.tag
}

// Name returns the application name.
func (s *CAASApplication) Name() string {
	return s.tag.Id()
}

// String returns the application as a string.
func (s *CAASApplication) String() string {
	return s.Name()
}

// Watch returns a watcher for observing changes to an application.
func (s *CAASApplication) Watch() (watcher.NotifyWatcher, error) {
	return common.Watch(s.st.facade, s.tag)
}

// WatchUnits returns a StringsWatcher that notifies of changes to
// the lifecycles of the application's units.
func (s *CAASApplication) WatchUnits() (watcher.StringsWatcher, error) {
	var results params.StringsWatchResults
	args := params.Entities{
		Entities: []params.Entity{{Tag: s.tag.String()}},
	}
	err := s.st.facade.FacadeCall("WatchUnits", args, &results)
	if err != nil {
		return nil, err
	}
	if len(results.Results) != 1 {
		return nil, fmt.Errorf("expected 1 result, got %d", len(results.Results))
	}
	result := results.Results[0]
	if result.Error != nil {
		return nil, result.Error
	}
	w := apiwatcher.NewStringsWatcher(s.st.facade.RawAPICaller(), result)
	return w, nil
}

func (s *CAASApplication) AllCAASUnits() ([]CAASUnit, error) {
	var results params.AllCAASUnitsResults
	args := params.Entities{
		Entities: []params.Entity{{Tag: s.tag.String()}},
	}
	err := s.st.facade.FacadeCall("AllCAASUnits", args, &results)
	if err != nil {
		return nil, err
	}
	if len(results.Results) != 1 {
		return nil, fmt.Errorf("expected 1 result, got %d", len(results.Results))
	}
	result := results.Results[0]
	if result.Error != nil {
		return nil, result.Error
	}
	out := make([]CAASUnit, 0, len(result.Units))
	for _, unit := range result.Units {
		tag, err := names.ParseUnitTag(unit.Tag)
		if err != nil {
			return nil, err
		}
		out = append(out, CAASUnit{
			st:   s.st,
			tag:  tag,
			life: unit.Life,
		})
	}
	return out, nil
}

// WatchRelations returns a StringsWatcher that notifies of changes to
// the lifecycles of relations involving s.
func (s *CAASApplication) WatchRelations() (watcher.StringsWatcher, error) {
	var results params.StringsWatchResults
	args := params.Entities{
		Entities: []params.Entity{{Tag: s.tag.String()}},
	}
	logger.Debugf("About to make WatchApplicationRelations facade call with args=%v", args)
	err := s.st.facade.FacadeCall("WatchApplicationRelations", args, &results)
	if err != nil {
		return nil, err
	}
	if len(results.Results) != 1 {
		return nil, fmt.Errorf("expected 1 result, got %d", len(results.Results))
	}
	result := results.Results[0]
	if result.Error != nil {
		return nil, result.Error
	}
	w := apiwatcher.NewStringsWatcher(s.st.facade.RawAPICaller(), result)
	return w, nil
}

// Life returns the application's current life state.
func (s *CAASApplication) Life() params.Life {
	return s.life
}

// Refresh refreshes the contents of the Service from the underlying
// state.
func (s *CAASApplication) Refresh() error {
	life, err := s.st.life(s.tag)
	if err != nil {
		return err
	}
	s.life = life
	return nil
}

// CharmModifiedVersion increments every time the charm, or any part of it, is
// changed in some way.
func (s *CAASApplication) CharmModifiedVersion() (int, error) {
	var results params.IntResults
	args := params.Entities{
		Entities: []params.Entity{{Tag: s.tag.String()}},
	}
	err := s.st.facade.FacadeCall("CharmModifiedVersion", args, &results)
	if err != nil {
		return -1, err
	}

	if len(results.Results) != 1 {
		return -1, fmt.Errorf("expected 1 result, got %d", len(results.Results))
	}
	result := results.Results[0]
	if result.Error != nil {
		return -1, result.Error
	}

	return result.Result, nil
}

// CharmURL returns the service's charm URL, and whether units should
// upgrade to the charm with that URL even if they are in an error
// state (force flag).
//
// NOTE: This differs from state.Service.CharmURL() by returning
// an error instead as well, because it needs to make an API call.
func (s *CAASApplication) CharmURL() (*charm.URL, bool, error) {
	var results params.StringBoolResults
	args := params.Entities{
		Entities: []params.Entity{{Tag: s.tag.String()}},
	}
	err := s.st.facade.FacadeCall("CharmURL", args, &results)
	if err != nil {
		return nil, false, err
	}
	if len(results.Results) != 1 {
		return nil, false, fmt.Errorf("expected 1 result, got %d", len(results.Results))
	}
	result := results.Results[0]
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.Result != "" {
		curl, err := charm.ParseURL(result.Result)
		if err != nil {
			return nil, false, err
		}
		return curl, result.Ok, nil
	}
	return nil, false, fmt.Errorf("%q has no charm url set", s.tag)
}

// SetStatus sets the status of the service.
func (s *CAASApplication) SetStatus(appName string, stat status.Status, info string, data map[string]interface{}) error {
	tag := names.NewApplicationTag(appName)
	var result params.ErrorResults
	args := params.SetStatus{
		Entities: []params.EntityStatusArgs{
			{
				Tag:    tag.String(),
				Status: stat.String(),
				Info:   info,
				Data:   data,
			},
		},
	}
	err := s.st.facade.FacadeCall("SetApplicationStatus", args, &result)
	if err != nil {
		return errors.Trace(err)
	}
	return result.OneError()
}

// Status returns the status of the service if the passed unitName,
// corresponding to the calling unit, is of the leader.
func (s *CAASApplication) Status(unitName string) (params.ApplicationStatusResult, error) {
	tag := names.NewUnitTag(unitName)
	var results params.ApplicationStatusResults
	args := params.Entities{
		Entities: []params.Entity{
			{
				Tag: tag.String(),
			},
		},
	}
	err := s.st.facade.FacadeCall("ApplicationStatus", args, &results)
	if err != nil {
		return params.ApplicationStatusResult{}, errors.Trace(err)
	}
	result := results.Results[0]
	if result.Error != nil {
		return params.ApplicationStatusResult{}, result.Error
	}
	return result, nil
}

// WatchLeadershipSettings returns a watcher which can be used to wait
// for leadership settings changes to be made for the application.
func (s *CAASApplication) WatchLeadershipSettings() (watcher.NotifyWatcher, error) {
	return s.st.LeadershipSettings.WatchLeadershipSettings(s.tag.Id())
}
