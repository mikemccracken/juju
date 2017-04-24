// Copyright 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

// Package caasoperator implements the API interface used by the caasoperator worker.
package caasoperator

import (
	"github.com/juju/errors"
	"github.com/juju/loggo"
	"gopkg.in/juju/charm.v6-unstable"
	"gopkg.in/juju/names.v2"

	"github.com/juju/juju/apiserver/common"
	"github.com/juju/juju/apiserver/facade"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/state"
	"github.com/juju/juju/state/watcher"
)

var logger = loggo.GetLogger("juju.apiserver.caasoperator")

type Facade struct {
	*common.LifeGetter
	//*StatusAPI
	//*common.DeadEnsurer
	*common.AgentEntityWatcher
	//*common.APIAddresser
	//*common.ModelWatcher
	//*common.RebootRequester
	//*leadershipapiserver.LeadershipSettingsAccessor
	//meterstatus.MeterStatus

	st        *state.CAASState
	auth      facade.Authorizer
	resources facade.Resources
	app       *state.CAASApplication
}

func NewFacade(ctx facade.Context) (*Facade, error) {
	st := ctx.CAASState()
	resources := ctx.Resources()
	authorizer := ctx.Auth()
	// if !authorizer.AuthUnitAgent() {
	// 	return nil, common.ErrPerm
	// }
	var app *state.CAASApplication
	var err error
	switch tag := authorizer.GetAuthTag().(type) {
	case names.ApplicationTag:
		app, err = st.CAASApplication(tag.Id())
		if err != nil {
			return nil, errors.Trace(err)
		}
	default:
		return nil, errors.Errorf("expected names.ApplicationTag, got %T", tag)
	}

	makeAllowAll := func() (common.AuthFunc, error) {
		return accessAll, nil
	}
	return &Facade{
		LifeGetter: common.NewLifeGetter(st, makeAllowAll),
		//DeadEnsurer:                common.NewDeadEnsurer(st, accessUnit),
		AgentEntityWatcher: common.NewAgentEntityWatcher(st, resources, makeAllowAll),
		//APIAddresser:               common.NewAPIAddresser(st, resources),
		//ModelWatcher:               common.NewModelWatcher(st, resources, authorizer),
		//RebootRequester:            common.NewRebootRequester(st, accessMachine),
		//LeadershipSettingsAccessor: leadershipSettingsAccessorFactory(st, resources, authorizer),
		//MeterStatus:                msAPI,
		//StatusAPI: NewStatusAPI(st, accessUnitOrService),

		st:        st,
		auth:      authorizer,
		resources: resources,
		app:       app,
	}, nil
}

func accessAll(names.Tag) bool {
	return true
}

// Resolved returns the current resolved setting for each given unit.
func (f *Facade) Resolved(args params.Entities) (params.ResolvedModeResults, error) {
	return params.ResolvedModeResults{}, errors.NotImplementedf("method")
	/* XXX
	result := params.ResolvedModeResults{
		Results: make([]params.ResolvedModeResult, len(args.Entities)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.ResolvedModeResults{}, err
	}
	for i, entity := range args.Entities {
		tag, err := names.ParseUnitTag(entity.Tag)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		err = common.ErrPerm
		if canAccess(tag) {
			var unit *state.Unit
			unit, err = f.getUnit(tag)
			if err == nil {
				result.Results[i].Mode = params.ResolvedMode(unit.Resolved())
			}
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
	*/
}

// ClearResolved removes any resolved setting from each given unit.
func (f *Facade) ClearResolved(args params.Entities) (params.ErrorResults, error) {
	return params.ErrorResults{}, errors.NotImplementedf("method")
	/* XXX
	result := params.ErrorResults{
		Results: make([]params.ErrorResult, len(args.Entities)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.ErrorResults{}, err
	}
	for i, entity := range args.Entities {
		tag, err := names.ParseUnitTag(entity.Tag)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		err = common.ErrPerm
		if canAccess(tag) {
			var unit *state.Unit
			unit, err = f.getUnit(tag)
			if err == nil {
				err = unit.ClearResolved()
			}
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
	*/
}

// Destroy advances all given Alive units' lifecycles as far as
// possible. See state/Unit.Destroy().
func (f *Facade) Destroy(args params.Entities) (params.ErrorResults, error) {
	return params.ErrorResults{}, errors.NotImplementedf("method")
	/* XXX
	result := params.ErrorResults{
		Results: make([]params.ErrorResult, len(args.Entities)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.ErrorResults{}, err
	}
	for i, entity := range args.Entities {
		tag, err := names.ParseUnitTag(entity.Tag)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		err = common.ErrPerm
		if canAccess(tag) {
			var unit *state.Unit
			unit, err = f.getUnit(tag)
			if err == nil {
				err = unit.Destroy()
			}
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
	*/
}

// CharmModifiedVersion returns the most CharmModifiedVersion for all given
// units or services.
func (f *Facade) CharmModifiedVersion(args params.Entities) (params.IntResults, error) {
	results := params.IntResults{
		Results: make([]params.IntResult, len(args.Entities)),
	}

	// accessUnitOrService := common.AuthAny(f.accessUnit, f.accessService)
	// canAccess, err := accessUnitOrService()
	// if err != nil {
	// 	return results, err
	// }
	for i, entity := range args.Entities {
		ver, err := f.charmModifiedVersion(entity.Tag, accessAll)
		if err != nil {
			results.Results[i].Error = common.ServerError(err)
			continue
		}
		results.Results[i].Result = ver
	}
	return results, nil
}

func (f *Facade) charmModifiedVersion(tagStr string, canAccess func(names.Tag) bool) (int, error) {
	tag, err := names.ParseTag(tagStr)
	if err != nil {
		return -1, common.ErrPerm
	}
	if !canAccess(tag) {
		return -1, common.ErrPerm
	}
	entity, err := f.st.FindEntity(tag)
	if err != nil {
		return -1, err
	}
	app, ok := entity.(*state.CAASApplication)
	if !ok {
		return -1, errors.BadRequestf("type %T unsupported", entity)
	}
	return app.CharmModifiedVersion(), nil
}

// CharmURL returns the charm URL for all given units or services.
func (f *Facade) CharmURL(args params.Entities) (params.StringBoolResults, error) {
	result := params.StringBoolResults{
		Results: make([]params.StringBoolResult, len(args.Entities)),
	}
	// accessUnitOrService := common.AuthAny(f.accessUnit, f.accessService)
	// canAccess, err := accessUnitOrService()
	// if err != nil {
	// 	return params.StringBoolResults{}, err
	// }
	for i, entity := range args.Entities {
		tag, err := names.ParseTag(entity.Tag)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		err = common.ErrPerm
		//if canAccess(tag) {
		var app state.Entity
		app, err = f.st.FindEntity(tag)
		if err == nil {
			charmURLer := app.(interface {
				CharmURL() (*charm.URL, bool)
			})
			curl, ok := charmURLer.CharmURL()
			if curl != nil {
				result.Results[i].Result = curl.String()
				result.Results[i].Ok = ok
			}
		}
		//}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

// SetCharmURL sets the charm URL for each given unit. An error will
// be returned if a unit is dead, or the charm URL is not know.
func (f *Facade) SetCharmURL(args params.EntitiesCharmURL) (params.ErrorResults, error) {
	return params.ErrorResults{}, errors.NotImplementedf("method")
	/* XXX
	result := params.ErrorResults{
		Results: make([]params.ErrorResult, len(args.Entities)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.ErrorResults{}, err
	}
	for i, entity := range args.Entities {
		tag, err := names.ParseUnitTag(entity.Tag)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		err = common.ErrPerm
		if canAccess(tag) {
			var unit *state.Unit
			unit, err = f.getUnit(tag)
			if err == nil {
				var curl *charm.URL
				curl, err = charm.ParseURL(entity.CharmURL)
				if err == nil {
					err = unit.SetCharmURL(curl)
				}
			}
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
	*/
}

// CharmArchiveSha256 returns the SHA256 digest of the charm archive
// (bundle) data for each charm url in the given parameters.
func (f *Facade) CharmArchiveSha256(args params.CharmURLs) (params.StringResults, error) {
	result := params.StringResults{
		Results: make([]params.StringResult, len(args.URLs)),
	}
	for i, arg := range args.URLs {
		curl, err := charm.ParseURL(arg.URL)
		if err != nil {
			err = common.ErrPerm
		} else {
			var sch *state.Charm
			sch, err = f.st.Charm(curl)
			if errors.IsNotFound(err) {
				err = common.ErrPerm
			}
			if err == nil {
				result.Results[i].Result = sch.BundleSha256()
			}
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

func (f *Facade) WorkloadVersion(args params.Entities) (params.StringResults, error) {
	return params.StringResults{}, errors.NotImplementedf("method")
	/* XXX
	result := params.StringResults{
		Results: make([]params.StringResult, len(args.Entities)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.StringResults{}, err
	}
	for i, entity := range args.Entities {
		resultItem := &result.Results[i]
		tag, err := names.ParseUnitTag(entity.Tag)
		if err != nil {
			resultItem.Error = common.ServerError(err)
			continue
		}
		if !canAccess(tag) {
			resultItem.Error = common.ServerError(common.ErrPerm)
			continue
		}
		unit, err := f.getUnit(tag)
		if err != nil {
			resultItem.Error = common.ServerError(err)
			continue
		}
		version, err := unit.WorkloadVersion()
		if err != nil {
			resultItem.Error = common.ServerError(err)
			continue
		}
		resultItem.Result = version
	}
	return result, nil
	*/
}

// SetWorkloadVersion sets the workload version for each given unit. An error will
// be returned if a unit is dead.
func (f *Facade) SetWorkloadVersion(args params.EntityWorkloadVersions) (params.ErrorResults, error) {
	return params.ErrorResults{}, errors.NotImplementedf("method")
	/* XXX
	result := params.ErrorResults{
		Results: make([]params.ErrorResult, len(args.Entities)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.ErrorResults{}, err
	}
	for i, entity := range args.Entities {
		resultItem := &result.Results[i]
		tag, err := names.ParseUnitTag(entity.Tag)
		if err != nil {
			resultItem.Error = common.ServerError(err)
			continue
		}
		if !canAccess(tag) {
			resultItem.Error = common.ServerError(common.ErrPerm)
			continue
		}
		unit, err := f.getUnit(tag)
		if err != nil {
			resultItem.Error = common.ServerError(err)
			continue
		}
		err = unit.SetWorkloadVersion(entity.WorkloadVersion)
		if err != nil {
			resultItem.Error = common.ServerError(err)
		}
	}
	return result, nil
	*/
}

func (f *Facade) WatchConfigSettings(args params.Entities) (params.NotifyWatchResults, error) {
	result := params.NotifyWatchResults{
		Results: make([]params.NotifyWatchResult, len(args.Entities)),
	}
	canAccess := accessAll
	// canAccess, err := f.accessUnit()
	// if err != nil {
	// 	return params.NotifyWatchResults{}, err
	// }
	for i, entity := range args.Entities {
		tag, err := names.ParseApplicationTag(entity.Tag)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		err = common.ErrPerm
		watcherId := ""
		if canAccess(tag) {
			watcherId, err = f.watchOneUnitConfigSettings(tag)
		}
		result.Results[i].NotifyWatcherId = watcherId
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

func (f *Facade) watchOneUnitConfigSettings(tag names.ApplicationTag) (string, error) {
	app, err := f.st.CAASApplication(tag.Id())
	if err != nil {
		return "", err
	}
	watch, err := app.WatchConfigSettings()
	if err != nil {
		return "", err
	}
	// Consume the initial event. Technically, API
	// calls to Watch 'transmit' the initial event
	// in the Watch response. But NotifyWatchers
	// have no state to transmit.
	if _, ok := <-watch.Changes(); ok {
		return f.resources.Register(watch), nil
	}
	return "", watcher.EnsureErr(watch)
}

// ConfigSettings returns the complete set of service charm config
// settings available to each given unit.
func (f *Facade) ConfigSettings(args params.Entities) (params.ConfigSettingsResults, error) {
	result := params.ConfigSettingsResults{
		Results: make([]params.ConfigSettingsResult, len(args.Entities)),
	}
	canAccess := accessAll
	// canAccess, err := f.accessUnit()
	// if err != nil {
	// 	return params.ConfigSettingsResults{}, err
	// }
	for i, entity := range args.Entities {
		tag, err := names.ParseUnitTag(entity.Tag)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		err = common.ErrPerm
		if canAccess(tag) {
			var unit *state.Unit
			app, err := f.st.CAASApplication(tag.Id())
			if err == nil {
				var settings charm.Settings
				settings, err = app.ConfigSettings()
				if err == nil {
					result.Results[i].Settings = params.ConfigSettings(settings)
				}
			}
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

func (f *Facade) CurrentModel() (params.ModelResult, error) {
	result := params.ModelResult{}
	env, err := f.st.CAASModel()
	if err == nil {
		result.Name = env.Name()
		result.UUID = env.UUID()
	}
	return result, err
}

/*

// WatchApplicationRelations returns a StringsWatcher, for each given
// service, that notifies of changes to the lifecycles of relations
// involving that service.
func (f *Facade) WatchApplicationRelations(args params.Entities) (params.StringsWatchResults, error) {
	result := params.StringsWatchResults{
		Results: make([]params.StringsWatchResult, len(args.Entities)),
	}
	canAccess, err := f.accessService()
	if err != nil {
		return params.StringsWatchResults{}, err
	}
	for i, entity := range args.Entities {
		tag, err := names.ParseApplicationTag(entity.Tag)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		err = common.ErrPerm
		if canAccess(tag) {
			result.Results[i], err = f.watchOneServiceRelations(tag)
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

// Relation returns information about all given relation/unit pairs,
// including their id, key and the local endpoint.
func (f *Facade) Relation(args params.RelationUnits) (params.RelationResults, error) {
	result := params.RelationResults{
		Results: make([]params.RelationResult, len(args.RelationUnits)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.RelationResults{}, err
	}
	for i, rel := range args.RelationUnits {
		relParams, err := f.getOneRelation(canAccess, rel.Relation, rel.Unit)
		if err == nil {
			result.Results[i] = relParams
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

// RelationById returns information about all given relations,
// specified by their ids, including their key and the local
// endpoint.
func (f *Facade) RelationById(args params.RelationIds) (params.RelationResults, error) {
	result := params.RelationResults{
		Results: make([]params.RelationResult, len(args.RelationIds)),
	}
	for i, relId := range args.RelationIds {
		relParams, err := f.getOneRelationById(relId)
		if err == nil {
			result.Results[i] = relParams
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

// JoinedRelations returns the tags of all relations for which each supplied unit
// has entered scope. It should be called RelationsInScope, but it's not convenient
// to make that change until we have versioned APIs.
func (f *Facade) JoinedRelations(args params.Entities) (params.StringsResults, error) {
	result := params.StringsResults{
		Results: make([]params.StringsResult, len(args.Entities)),
	}
	if len(args.Entities) == 0 {
		return result, nil
	}
	canRead, err := f.accessUnit()
	if err != nil {
		return params.StringsResults{}, err
	}
	for i, entity := range args.Entities {
		tag, err := names.ParseUnitTag(entity.Tag)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		err = common.ErrPerm
		if canRead(tag) {
			var unit *state.Unit
			unit, err = f.getUnit(tag)
			if err == nil {
				result.Results[i].Result, err = relationsInScopeTags(unit)
			}
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}


// EnterScope ensures each unit has entered its scope in the relation,
// for all of the given relation/unit pairs. See also
// state.RelationUnit.EnterScope().
func (f *Facade) EnterScope(args params.RelationUnits) (params.ErrorResults, error) {
	result := params.ErrorResults{
		Results: make([]params.ErrorResult, len(args.RelationUnits)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.ErrorResults{}, err
	}
	one := func(relTag string, unitTag names.UnitTag) error {
		rel, unit, err := f.getRelationAndUnit(canAccess, relTag, unitTag)
		if err != nil {
			return err
		}
		relUnit, err := rel.Unit(unit)
		if err != nil {
			return err
		}

		settings := map[string]interface{}{}
		settingsAddress, err := relUnit.SettingsAddress()
		if err == nil {
			// Construct the settings, passing the unit's address (we
			// already know it). Normally this will be the private
			// address, but if this relation is to a remote application it
			// might be the public one.
			settings["private-address"] = settingsAddress.Value
		} else {
			logger.Warningf("cannot set private-address for unit %v in relation %v: %v", unitTag.Id(), relTag, err)
		}
		return relUnit.EnterScope(settings)
	}
	for i, arg := range args.RelationUnits {
		tag, err := names.ParseUnitTag(arg.Unit)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		err = one(arg.Relation, tag)
		if err != nil {
			result.Results[i].Error = common.ServerError(err)
		}
	}
	return result, nil
}

// LeaveScope signals each unit has left its scope in the relation,
// for all of the given relation/unit pairs. See also
// state.RelationUnit.LeaveScope().
func (f *Facade) LeaveScope(args params.RelationUnits) (params.ErrorResults, error) {
	result := params.ErrorResults{
		Results: make([]params.ErrorResult, len(args.RelationUnits)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.ErrorResults{}, err
	}
	for i, arg := range args.RelationUnits {
		unit, err := names.ParseUnitTag(arg.Unit)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		relUnit, err := f.getRelationUnit(canAccess, arg.Relation, unit)
		if err == nil {
			err = relUnit.LeaveScope()
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

// ReadSettings returns the local settings of each given set of
// relation/unit.
func (f *Facade) ReadSettings(args params.RelationUnits) (params.SettingsResults, error) {
	result := params.SettingsResults{
		Results: make([]params.SettingsResult, len(args.RelationUnits)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.SettingsResults{}, err
	}
	for i, arg := range args.RelationUnits {
		unit, err := names.ParseUnitTag(arg.Unit)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		relUnit, err := f.getRelationUnit(canAccess, arg.Relation, unit)
		if err == nil {
			var settings *state.Settings
			settings, err = relUnit.Settings()
			if err == nil {
				result.Results[i].Settings, err = convertRelationSettings(settings.Map())
			}
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

// ReadRemoteSettings returns the remote settings of each given set of
// relation/local unit/remote unit.
func (f *Facade) ReadRemoteSettings(args params.RelationUnitPairs) (params.SettingsResults, error) {
	result := params.SettingsResults{
		Results: make([]params.SettingsResult, len(args.RelationUnitPairs)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.SettingsResults{}, err
	}
	for i, arg := range args.RelationUnitPairs {
		unit, err := names.ParseUnitTag(arg.LocalUnit)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		relUnit, err := f.getRelationUnit(canAccess, arg.Relation, unit)
		if err == nil {
			// TODO(dfc) rework this logic
			remoteUnit := ""
			remoteUnit, err = f.checkRemoteUnit(relUnit, arg.RemoteUnit)
			if err == nil {
				var settings map[string]interface{}
				settings, err = relUnit.ReadSettings(remoteUnit)
				if err == nil {
					result.Results[i].Settings, err = convertRelationSettings(settings)
				}
			}
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

// UpdateSettings persists all changes made to the local settings of
// all given pairs of relation and unit. Keys with empty values are
// considered a signal to delete these values.
func (f *Facade) UpdateSettings(args params.RelationUnitsSettings) (params.ErrorResults, error) {
	result := params.ErrorResults{
		Results: make([]params.ErrorResult, len(args.RelationUnits)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.ErrorResults{}, err
	}
	for i, arg := range args.RelationUnits {
		unit, err := names.ParseUnitTag(arg.Unit)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		relUnit, err := f.getRelationUnit(canAccess, arg.Relation, unit)
		if err == nil {
			var settings *state.Settings
			settings, err = relUnit.Settings()
			if err == nil {
				for k, v := range arg.Settings {
					if v == "" {
						settings.Delete(k)
					} else {
						settings.Set(k, v)
					}
				}
				_, err = settings.Write()
			}
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

// WatchRelationUnits returns a RelationUnitsWatcher for observing
// changes to every unit in the supplied relation that is visible to
// the supplied unit. See also state/watcher.go:RelationUnit.Watch().
func (f *Facade) WatchRelationUnits(args params.RelationUnits) (params.RelationUnitsWatchResults, error) {
	result := params.RelationUnitsWatchResults{
		Results: make([]params.RelationUnitsWatchResult, len(args.RelationUnits)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.RelationUnitsWatchResults{}, err
	}
	for i, arg := range args.RelationUnits {
		unit, err := names.ParseUnitTag(arg.Unit)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		relUnit, err := f.getRelationUnit(canAccess, arg.Relation, unit)
		if err == nil {
			result.Results[i], err = f.watchOneRelationUnit(relUnit)
		}
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

// WatchUnitAddresses returns a NotifyWatcher for observing changes
// to each unit's addresses.
func (f *Facade) WatchUnitAddresses(args params.Entities) (params.NotifyWatchResults, error) {
	result := params.NotifyWatchResults{
		Results: make([]params.NotifyWatchResult, len(args.Entities)),
	}
	canAccess, err := f.accessUnit()
	if err != nil {
		return params.NotifyWatchResults{}, err
	}
	for i, entity := range args.Entities {
		unit, err := names.ParseUnitTag(entity.Tag)
		if err != nil {
			result.Results[i].Error = common.ServerError(common.ErrPerm)
			continue
		}
		err = common.ErrPerm
		watcherId := ""
		if canAccess(unit) {
			watcherId, err = f.watchOneUnitAddresses(unit)
		}
		result.Results[i].NotifyWatcherId = watcherId
		result.Results[i].Error = common.ServerError(err)
	}
	return result, nil
}

func (f *Facade) getRelationUnit(canAccess common.AuthFunc, relTag string, unitTag names.UnitTag) (*state.RelationUnit, error) {
	rel, unit, err := f.getRelationAndUnit(canAccess, relTag, unitTag)
	if err != nil {
		return nil, err
	}
	return rel.Unit(unit)
}

func (f *Facade) getOneRelationById(relId int) (params.RelationResult, error) {
	nothing := params.RelationResult{}
	rel, err := f.st.Relation(relId)
	if errors.IsNotFound(err) {
		return nothing, common.ErrPerm
	} else if err != nil {
		return nothing, err
	}
	tag := f.auth.GetAuthTag()
	switch tag.(type) {
	case names.UnitTag:
		// do nothing
	default:
		panic("authenticated entity is not a unit")
	}
	unit, err := f.st.FindEntity(tag)
	if err != nil {
		return nothing, err
	}
	// Use the currently authenticated unit to get the endpoint.
	result, err := f.prepareRelationResult(rel, unit.(*state.Unit))
	if err != nil {
		// An error from prepareRelationResult means the authenticated
		// unit's service is not part of the requested
		// relation. That's why it's appropriate to return ErrPerm
		// here.
		return nothing, common.ErrPerm
	}
	return result, nil
}

func (f *Facade) getRelationAndUnit(canAccess common.AuthFunc, relTag string, unitTag names.UnitTag) (*state.Relation, *state.Unit, error) {
	tag, err := names.ParseRelationTag(relTag)
	if err != nil {
		return nil, nil, common.ErrPerm
	}
	rel, err := f.st.KeyRelation(tag.Id())
	if errors.IsNotFound(err) {
		return nil, nil, common.ErrPerm
	} else if err != nil {
		return nil, nil, err
	}
	if !canAccess(unitTag) {
		return nil, nil, common.ErrPerm
	}
	unit, err := f.getUnit(unitTag)
	return rel, unit, err
}

func (f *Facade) prepareRelationResult(rel *state.Relation, unit *state.Unit) (params.RelationResult, error) {
	nothing := params.RelationResult{}
	ep, err := rel.Endpoint(unit.ApplicationName())
	if err != nil {
		// An error here means the unit's service is not part of the
		// relation.
		return nothing, err
	}
	return params.RelationResult{
		Id:   rel.Id(),
		Key:  rel.String(),
		Life: params.Life(rel.Life().String()),
		Endpoint: multiwatcher.Endpoint{
			ApplicationName: ep.ApplicationName,
			Relation:        multiwatcher.NewCharmRelation(ep.Relation),
		},
	}, nil
}

func (f *Facade) getOneRelation(canAccess common.AuthFunc, relTag, unitTag string) (params.RelationResult, error) {
	nothing := params.RelationResult{}
	tag, err := names.ParseUnitTag(unitTag)
	if err != nil {
		return nothing, common.ErrPerm
	}
	rel, unit, err := f.getRelationAndUnit(canAccess, relTag, tag)
	if err != nil {
		return nothing, err
	}
	return f.prepareRelationResult(rel, unit)
}

func (f *Facade) watchOneServiceRelations(tag names.ApplicationTag) (params.StringsWatchResult, error) {
	nothing := params.StringsWatchResult{}
	service, err := f.st.Application(tag.Id())
	if err != nil {
		return nothing, err
	}
	watch := service.WatchRelations()
	// Consume the initial event and forward it to the result.
	if changes, ok := <-watch.Changes(); ok {
		return params.StringsWatchResult{
			StringsWatcherId: f.resources.Register(watch),
			Changes:          changes,
		}, nil
	}
	return nothing, watcher.EnsureErr(watch)
}

func (f *Facade) watchOneUnitAddresses(tag names.UnitTag) (string, error) {
	unit, err := f.getUnit(tag)
	if err != nil {
		return "", err
	}
	machineId, err := unit.AssignedMachineId()
	if err != nil {
		return "", err
	}
	machine, err := f.st.Machine(machineId)
	if err != nil {
		return "", err
	}
	watch := machine.WatchAddresses()
	// Consume the initial event. Technically, API
	// calls to Watch 'transmit' the initial event
	// in the Watch response. But NotifyWatchers
	// have no state to transmit.
	if _, ok := <-watch.Changes(); ok {
		return f.resources.Register(watch), nil
	}
	return "", watcher.EnsureErr(watch)
}

func (f *Facade) watchOneRelationUnit(relUnit *state.RelationUnit) (params.RelationUnitsWatchResult, error) {
	watch := relUnit.Watch()
	// Consume the initial event and forward it to the result.
	if changes, ok := <-watch.Changes(); ok {
		return params.RelationUnitsWatchResult{
			RelationUnitsWatcherId: f.resources.Register(watch),
			Changes:                changes,
		}, nil
	}
	return params.RelationUnitsWatchResult{}, watcher.EnsureErr(watch)
}

func (f *Facade) checkRemoteUnit(relUnit *state.RelationUnit, remoteUnitTag string) (string, error) {
	// Make sure the unit is indeed remote.
	if remoteUnitTag == f.auth.GetAuthTag().String() {
		return "", common.ErrPerm
	}
	// Check remoteUnit is indeed related. Note that we don't want to actually get
	// the *Unit, because it might have been removed; but its relation settings will
	// persist until the relation itself has been removed (and must remain accessible
	// because the local unit's view of reality may be time-shifted).
	tag, err := names.ParseUnitTag(remoteUnitTag)
	if err != nil {
		return "", common.ErrPerm
	}
	remoteUnitName := tag.Id()
	remoteApplicationName, err := names.UnitApplication(remoteUnitName)
	if err != nil {
		return "", common.ErrPerm
	}
	rel := relUnit.Relation()
	_, err = rel.RelatedEndpoints(remoteApplicationName)
	if err != nil {
		return "", common.ErrPerm
	}
	return remoteUnitName, nil
}

func convertRelationSettings(settings map[string]interface{}) (params.Settings, error) {
	result := make(params.Settings)
	for k, v := range settings {
		// All relation settings should be strings.
		sval, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected relation setting %q: expected string, got %T", k, v)
		}
		result[k] = sval
	}
	return result, nil
}

func relationsInScopeTags(unit *state.Unit) ([]string, error) {
	relations, err := unit.RelationsInScope()
	if err != nil {
		return nil, err
	}
	tags := make([]string, len(relations))
	for i, relation := range relations {
		tags[i] = relation.Tag().String()
	}
	return tags, nil
}

func leadershipSettingsAccessorFactory(
	st *state.State,
	resources facade.Resources,
	auth facade.Authorizer,
) *leadershipapiserver.LeadershipSettingsAccessor {
	registerWatcher := func(serviceId string) (string, error) {
		service, err := st.Application(serviceId)
		if err != nil {
			return "", err
		}
		w := service.WatchLeaderSettings()
		if _, ok := <-w.Changes(); ok {
			return resources.Register(w), nil
		}
		return "", watcher.EnsureErr(w)
	}
	getSettings := func(serviceId string) (map[string]string, error) {
		service, err := st.Application(serviceId)
		if err != nil {
			return nil, err
		}
		return service.LeaderSettings()
	}
	writeSettings := func(token leadership.Token, serviceId string, settings map[string]string) error {
		service, err := st.Application(serviceId)
		if err != nil {
			return err
		}
		return service.UpdateLeaderSettings(token, settings)
	}
	return leadershipapiserver.NewLeadershipSettingsAccessor(
		auth,
		registerWatcher,
		getSettings,
		st.LeadershipChecker().LeadershipCheck,
		writeSettings,
	)
}
*/
