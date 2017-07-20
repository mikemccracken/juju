// Copyright 2013, 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package caasclient

import (
	"sort"

	"github.com/juju/errors"
	"github.com/juju/utils/set"
	"gopkg.in/juju/charm.v6-unstable"

	"github.com/juju/juju/apiserver/common"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/state"
	"github.com/juju/juju/status"
)

// Status gives the information needed for juju CAAS status over the API.
func (c *Client) Status(args params.StatusParams) (params.CAASStatus, error) {
	if err := c.checkCanRead(); err != nil {
		return params.CAASStatus{}, err
	}

	var noStatus params.CAASStatus
	var context statusContext
	var err error
	if context.applications, context.units, context.latestCharms, err =
		fetchAllApplicationsAndUnits(c.api.state, len(args.Patterns) <= 0); err != nil {
		return noStatus, errors.Annotate(err, "could not fetch applications and units")
	}
	if context.relations, err = fetchRelations(c.api.state); err != nil {
		return noStatus, errors.Annotate(err, "could not fetch relations")
	}

	logger.Debugf("Applications: %v", context.applications)

	modelStatus, err := c.modelStatus()
	if err != nil {
		return noStatus, errors.Annotate(err, "cannot determine model status")
	}
	return params.CAASStatus{
		Model:        modelStatus,
		Applications: context.processCAASApplications(),
		Relations:    context.processRelations(),
	}, nil
}

func (c *Client) modelStatus() (params.CAASModelStatusInfo, error) {
	var info params.CAASModelStatusInfo

	m, err := c.api.state.CAASModel()
	if err != nil {
		return info, errors.Annotate(err, "cannot get model")
	}
	info.Name = m.Name()

	return info, nil
}

type statusContext struct {
	applications map[string]*state.CAASApplication
	relations    map[string][]*state.Relation
	units        map[string]map[string]*state.CAASUnit
	latestCharms map[charm.URL]*state.Charm
	leaders      map[string]string
}

// fetchAllApplicationsAndUnits returns a map from application name to application,
// a map from application name to unit name to unit, and a map from base charm URL to latest URL.
func fetchAllApplicationsAndUnits(st *state.CAASState, matchAny bool) (map[string]*state.CAASApplication, map[string]map[string]*state.CAASUnit, map[charm.URL]*state.Charm, error) {
	appMap := make(map[string]*state.CAASApplication)
	unitMap := make(map[string]map[string]*state.CAASUnit)
	latestCharms := make(map[charm.URL]*state.Charm)

	applications, err := st.AllCAASApplications()
	if err != nil {
		return nil, nil, nil, err
	}
	for _, s := range applications {
		units, err := s.AllCAASUnits()
		if err != nil {
			return nil, nil, nil, err
		}
		appUnitMap := make(map[string]*state.CAASUnit)
		for _, u := range units {
			appUnitMap[u.Name()] = u
		}
		if matchAny || len(appUnitMap) > 0 {
			unitMap[s.Name()] = appUnitMap
			appMap[s.Name()] = s
			// Record the base URL for the application's charm so that
			// the latest store revision can be looked up.
			charmURL, _ := s.CharmURL()
			if charmURL.Schema == "cs" {
				latestCharms[*charmURL.WithRevision(-1)] = nil
			}
		}
	}
	for baseURL := range latestCharms {
		ch, err := st.LatestPlaceholderCharm(&baseURL)
		if errors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		latestCharms[baseURL] = ch
	}

	return appMap, unitMap, latestCharms, nil
}

// fetchRelations returns a map of all relations keyed by application name.
//
// This structure is useful for processApplicationRelations() which needs
// to have the relations for each application. Reading them once here
// avoids the repeated DB hits to retrieve the relations for each
// application that used to happen in processApplicationRelations().
func fetchRelations(st *state.CAASState) (map[string][]*state.Relation, error) {
	relations, err := st.AllRelations()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]*state.Relation)
	for _, relation := range relations {
		for _, ep := range relation.Endpoints() {
			out[ep.ApplicationName] = append(out[ep.ApplicationName], relation)
		}
	}
	return out, nil
}

func (context *statusContext) processCAASApplications() map[string]params.CAASApplicationStatus {
	caasApps := make(map[string]params.CAASApplicationStatus)
	for _, s := range context.applications {
		caasApps[s.Name()] = context.processCAASApplication(s)
	}
	return caasApps
}

func (context *statusContext) processCAASApplication(caasApp *state.CAASApplication) params.CAASApplicationStatus {
	caasAppCharm, _, err := caasApp.Charm()
	if err != nil {
		return params.CAASApplicationStatus{Err: common.ServerError(err)}
	}

	var processedStatus = params.CAASApplicationStatus{
		Charm: caasAppCharm.URL().String(),
		Life:  processLife(caasApp),
	}

	processedStatus.Relations, err = context.processCAASApplicationRelations(caasApp)
	if err != nil {
		processedStatus.Err = common.ServerError(err)
		return processedStatus
	}

	if latestCharm, ok := context.latestCharms[*caasAppCharm.URL().WithRevision(-1)]; ok && latestCharm != nil {
		if latestCharm.Revision() > caasAppCharm.URL().Revision {
			processedStatus.CanUpgradeTo = latestCharm.String()
		}
	}

	units := context.units[caasApp.Name()]
	processedStatus.Units = context.processUnits(units, caasAppCharm.URL().String())

	appStatus, err := caasApp.Status()
	if err != nil {
		processedStatus.Err = common.ServerError(err)
		return processedStatus
	}
	processedStatus.Status.Status = appStatus.Status.String()
	processedStatus.Status.Info = appStatus.Message
	processedStatus.Status.Data = appStatus.Data
	processedStatus.Status.Since = appStatus.Since

	versions := make([]status.StatusInfo, 0, len(units))
	for _, unit := range units {
		statuses, err := unit.WorkloadVersionHistory().StatusHistory(
			status.StatusHistoryFilter{Size: 1},
		)
		if err != nil {
			processedStatus.Err = common.ServerError(err)
			return processedStatus
		}
		// Even though we fully expect there to be historical values there,
		// even the first should be the empty string, the status history
		// collection is not added to in a transactional manner, so it may be
		// not there even though we'd really like it to be. Such is mongo.
		if len(statuses) > 0 {
			versions = append(versions, statuses[0])
		}
	}
	if len(versions) > 0 {
		sort.Sort(bySinceDescending(versions))
		processedStatus.WorkloadVersion = versions[0].Message
	}

	return processedStatus
}

func (context *statusContext) processUnits(units map[string]*state.CAASUnit, caasAppCharm string) map[string]params.CAASUnitStatus {
	unitsMap := make(map[string]params.CAASUnitStatus)
	for _, unit := range units {
		unitsMap[unit.Name()] = context.processUnit(unit, caasAppCharm)
	}
	return unitsMap
}

func (context *statusContext) processCAASApplicationRelations(caasApp *state.CAASApplication) (related map[string][]string, err error) {
	related = make(map[string][]string)
	relations := context.relations[caasApp.Name()]
	for _, relation := range relations {
		ep, err := relation.Endpoint(caasApp.Name())
		if err != nil {
			return nil, err
		}
		relationName := ep.Relation.Name
		eps, err := relation.RelatedEndpoints(caasApp.Name())
		if err != nil {
			return nil, err
		}
		for _, ep := range eps {
			related[relationName] = append(related[relationName], ep.ApplicationName)
		}
	}
	for relationName, applicationNames := range related {
		sn := set.NewStrings(applicationNames...)
		related[relationName] = sn.SortedValues()
	}
	return related, nil
}

func (context *statusContext) processUnit(unit *state.CAASUnit, caasAppCharm string) params.CAASUnitStatus {
	var result params.CAASUnitStatus
	/*addr, err := unit.PublicAddress()
	if err != nil {
		// Usually this indicates that no addresses have been set on the
		// machine yet.
		addr = network.Address{}
		logger.Debugf("error fetching public address: %v", err)
	}
	result.PublicAddress = addr.Value
	unitPorts, _ := unit.OpenedPorts()
	for _, port := range unitPorts {
		result.OpenedPorts = append(result.OpenedPorts, port.String())
	}
	curl, _ := unit.CharmURL()
	if caasAppCharm != "" && curl != nil && curl.String() != caasAppCharm {
		result.Charm = curl.String()
	}*/
	workloadVersion, err := unit.WorkloadVersion()
	if err == nil {
		result.WorkloadVersion = workloadVersion
	} else {
		logger.Debugf("error fetching workload version: %v", err)
	}

	//processUnitAndAgentStatus(unit, &result)

	return result
}

func (context *statusContext) processRelations() []params.RelationStatus {
	var out []params.RelationStatus
	relations := context.getAllRelations()
	for _, relation := range relations {
		var eps []params.EndpointStatus
		var scope charm.RelationScope
		var relationInterface string
		for _, ep := range relation.Endpoints() {
			eps = append(eps, params.EndpointStatus{
				ApplicationName: ep.ApplicationName,
				Name:            ep.Name,
				Role:            string(ep.Role),
			})
			// these should match on both sides so use the last
			relationInterface = ep.Interface
			scope = ep.Scope
		}
		relStatus := params.RelationStatus{
			Id:        relation.Id(),
			Key:       relation.String(),
			Interface: relationInterface,
			Scope:     string(scope),
			Endpoints: eps,
		}
		out = append(out, relStatus)
	}
	return out
}

// This method exists only to dedup the loaded relations as they will
// appear multiple times in context.relations.
func (context *statusContext) getAllRelations() []*state.Relation {
	var out []*state.Relation
	seenRelations := make(map[int]bool)
	for _, relations := range context.relations {
		for _, relation := range relations {
			if _, found := seenRelations[relation.Id()]; !found {
				out = append(out, relation)
				seenRelations[relation.Id()] = true
			}
		}
	}
	return out
}

type lifer interface {
	Life() state.Life
}

func processLife(entity lifer) string {
	if life := entity.Life(); life != state.Alive {
		// alive is the usual state so omit it by default.
		return life.String()
	}
	return ""
}

type bySinceDescending []status.StatusInfo

// Len implements sort.Interface.
func (s bySinceDescending) Len() int { return len(s) }

// Swap implements sort.Interface.
func (s bySinceDescending) Swap(a, b int) { s[a], s[b] = s[b], s[a] }

// Less implements sort.Interface.
func (s bySinceDescending) Less(a, b int) bool { return s[a].Since.After(*s[b].Since) }
