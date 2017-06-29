// Copyright 2012-2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package relation

import (
	"github.com/juju/errors"
	"github.com/juju/loggo"
	"github.com/juju/utils/set"
	corecharm "gopkg.in/juju/charm.v6-unstable"
	"gopkg.in/juju/charm.v6-unstable/hooks"
	"gopkg.in/juju/names.v2"
	worker "gopkg.in/juju/worker.v1"

	"github.com/juju/juju/api/caasoperator"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/worker/caasoperator/hook"
	"github.com/juju/juju/worker/caasoperator/operation"
	"github.com/juju/juju/worker/caasoperator/remotestate"
	"github.com/juju/juju/worker/caasoperator/resolver"
	"github.com/juju/juju/worker/caasoperator/runner/context"
)

var logger = loggo.GetLogger("juju.worker.caasoperator.relation")

// Relations exists to encapsulate relation state and operations behind an
// interface for the benefit of future refactoring.
type Relations interface {
	// Name returns the name of the relation with the supplied id, or an error
	// if the relation is unknown.
	Name(id int) (string, error)

	// PrepareHook returns the name of the supplied relation hook, or an error
	// if the hook is unknown or invalid given current state.
	PrepareHook(hookInfo hook.Info) (string, error)

	// CommitHook persists the state change encoded in the supplied relation
	// hook, or returns an error if the hook is unknown or invalid given
	// current relation state.
	CommitHook(hookInfo hook.Info) error

	// GetInfo returns information about current relation state.
	GetInfo() map[int]*context.RelationInfo

	// NextHook returns details on the next hook to execute, based on the local
	// and remote states.
	NextHook(resolver.LocalState, remotestate.Snapshot) (hook.Info, error)
}

// NewRelationsResolver returns a new Resolver that handles differences in
// relation state.
func NewRelationsResolver(r Relations) resolver.Resolver {
	return &relationsResolver{r}
}

type relationsResolver struct {
	relations Relations
}

// NextOp implements resolver.Resolver.
func (s *relationsResolver) NextOp(
	localState resolver.LocalState,
	remoteState remotestate.Snapshot,
	opFactory operation.Factory,
) (operation.Operation, error) {
	logger.Debugf("relationsresolver NextOp starting")
	hook, err := s.relations.NextHook(localState, remoteState)
	logger.Debugf("relationsresolver NextHook is %v (err=%v)", hook, err)
	if err != nil {
		return nil, errors.Trace(err)
	}
	return opFactory.NewRunHook(hook)
}

// relations implements Relations.
type relations struct {
	st           *caasoperator.State
	caasUnit     *caasoperator.CAASUnit
	charmDir     string
	relationsDir string
	relationers  map[int]*Relationer
	abort        <-chan struct{}
}

// NewRelations returns a new Relations instance.
func NewRelations(st *caasoperator.State, tag names.UnitTag, charmDir, relationsDir string, abort <-chan struct{}) (Relations, error) {
	unit, err := st.CAASUnit(tag)
	if err != nil {
		return nil, errors.Trace(err)
	}
	r := &relations{
		st:           st,
		caasUnit:     unit,
		charmDir:     charmDir,
		relationsDir: relationsDir,
		relationers:  make(map[int]*Relationer),
		abort:        abort,
	}
	if err := r.init(); err != nil {
		return nil, errors.Trace(err)
	}
	return r, nil
}

// init reconciles the local relation state dirs with the remote state of
// the corresponding relations. It's only expected to be called while a
// *relations is being created.
func (r *relations) init() error {
	logger.Debugf("in relations.init()")
	joinedRelationTags, err := r.caasUnit.JoinedRelations()
	if err != nil {
		return errors.Trace(err)
	}
	logger.Debugf("relations.init(), got joinedRelationTags = %v", joinedRelationTags)
	joinedRelations := make(map[int]*caasoperator.Relation)
	for _, tag := range joinedRelationTags {
		relation, err := r.st.Relation(tag)
		if err != nil {
			return errors.Trace(err)
		}
		joinedRelations[relation.Id()] = relation
	}
	logger.Debugf("relations.init(), got joinedRelations = %v", joinedRelations)
	knownDirs, err := ReadAllStateDirs(r.relationsDir)
	if err != nil {
		return errors.Trace(err)
	}

	logger.Debugf("relations.init(), got knownDirs = %v", knownDirs)
	for id, dir := range knownDirs {
		if rel, ok := joinedRelations[id]; ok {
			if err := r.add(rel, dir); err != nil {
				return errors.Trace(err)
			}
		} else if err := dir.Remove(); err != nil {
			return errors.Trace(err)
		}
	}
	for id, rel := range joinedRelations {
		if _, ok := knownDirs[id]; ok {
			continue
		}
		dir, err := ReadStateDir(r.relationsDir, id)
		if err != nil {
			return errors.Trace(err)
		}
		if err := r.add(rel, dir); err != nil {
			return errors.Trace(err)
		}
	}
	return nil
}

// NextHook implements Relations.
func (r *relations) NextHook(
	localState resolver.LocalState,
	remoteState remotestate.Snapshot,
) (hook.Info, error) {
	logger.Debugf("relations NextHook starting, about to r.update() - remoteState.Relations=%v", remoteState)
	// Add/remove local relation state; enter and leave scope as necessary.
	if err := r.update(remoteState.Relations); err != nil {
		return hook.Info{}, errors.Trace(err)
	}
	logger.Debugf("relations NextHook starting, r.update() done. localState.Kind =%v", localState.Kind)
	if localState.Kind != operation.Continue {
		return hook.Info{}, resolver.ErrNoOperation
	}

	// See if any of the relations have operations to perform.
	for relationId, relationSnapshot := range remoteState.Relations {
		logger.Debugf(" - checking relationId %v relationSnapshot %v", relationId, relationSnapshot)
		relationer, ok := r.relationers[relationId]
		logger.Debugf(" - got relationer %v, ok %v ", relationer, ok)
		if !ok || relationer.IsImplicit() {
			logger.Debugf(" - bailing on ops loop in NextHook")
			continue
		}
		var remoteBroken bool
		if remoteState.Life == params.Dying || relationSnapshot.Life == params.Dying {
			logger.Debugf(" - remoteBroken: remoteState.Life=%v, relationSnapshot.Life=%v", remoteState.Life, relationSnapshot.Life)
			relationSnapshot = remotestate.RelationSnapshot{}
			remoteBroken = true
			// TODO(axw) if relation is implicit, leave scope & remove.
		}
		// If either the unit or the relation are Dying,
		// then the relation should be broken.
		hook, err := nextRelationHook(relationer.dir.State(), relationSnapshot, remoteBroken)
		if err == resolver.ErrNoOperation {
			logger.Debugf(" NextHook: nextrelationhook returned ErrNoOperation")
			continue
		}
		logger.Debugf(" NextHook: nextrelationhook returned hook=%v", hook)
		return hook, err
	}
	logger.Debugf("NextHook done with no operation found. reutrning ErrNoOperation")
	return hook.Info{}, resolver.ErrNoOperation
}

// nextRelationHook returns the next hook op that should be executed in the
// relation characterised by the supplied local and remote state; or an error
// if the states do not refer to the same relation; or ErrRelationUpToDate if
// no hooks need to be executed.
func nextRelationHook(
	local *State,
	remote remotestate.RelationSnapshot,
	remoteBroken bool,
) (hook.Info, error) {
	logger.Debugf("+ nextRelationHook() starting") // MMCC we are not getting here.
	// If there's a guaranteed next hook, return that.
	relationId := local.RelationId
	if local.ChangedPending != "" {
		unitName := local.ChangedPending
		logger.Debugf("+ nextRelationHook: local.ChangedPending is %v, returning RelationChanged", local.ChangedPending)
		return hook.Info{
			Kind:          hooks.RelationChanged,
			RelationId:    relationId,
			RemoteUnit:    unitName,
			ChangeVersion: remote.Members[unitName],
		}, nil
	}

	// Get the union of all relevant units, and sort them, so we produce events
	// in a consistent order (largely for the convenience of the tests).
	allUnitNames := set.NewStrings()
	for unitName := range local.Members {
		allUnitNames.Add(unitName)
	}
	for unitName := range remote.Members {
		allUnitNames.Add(unitName)
	}
	sortedUnitNames := allUnitNames.SortedValues()
	logger.Debugf("+ nextRelationHook: sortedUnitNames is %v", sortedUnitNames)
	// If there are any locally known units that are no longer reflected in
	// remote state, depart them.
	for _, unitName := range sortedUnitNames {
		changeVersion, found := local.Members[unitName]
		if !found {
			continue
		}
		if _, found := remote.Members[unitName]; !found {
			return hook.Info{
				Kind:          hooks.RelationDeparted,
				RelationId:    relationId,
				RemoteUnit:    unitName,
				ChangeVersion: changeVersion,
			}, nil
		}
	}

	// If the relation's meant to be broken, break it.
	if remoteBroken {
		return hook.Info{
			Kind:       hooks.RelationBroken,
			RelationId: relationId,
		}, nil
	}

	// If there are any remote units not locally known, join them.
	for _, unitName := range sortedUnitNames {
		changeVersion, found := remote.Members[unitName]
		if !found {
			continue
		}
		if _, found := local.Members[unitName]; !found {
			return hook.Info{
				Kind:          hooks.RelationJoined,
				RelationId:    relationId,
				RemoteUnit:    unitName,
				ChangeVersion: changeVersion,
			}, nil
		}
	}

	// Finally scan for remote units whose latest version is not reflected
	// in local state.
	for _, unitName := range sortedUnitNames {
		remoteChangeVersion, found := remote.Members[unitName]
		if !found {
			continue
		}
		localChangeVersion, found := local.Members[unitName]
		if !found {
			continue
		}
		// NOTE(axw) we use != and not > to cater due to the
		// use of the relation settings document's txn-revno
		// as the version. When model-uuid migration occurs, the
		// document is recreated, resetting txn-revno.
		if remoteChangeVersion != localChangeVersion {
			return hook.Info{
				Kind:          hooks.RelationChanged,
				RelationId:    relationId,
				RemoteUnit:    unitName,
				ChangeVersion: remoteChangeVersion,
			}, nil
		}
	}

	// Nothing left to do for this relation.
	return hook.Info{}, resolver.ErrNoOperation
}

// Name is part of the Relations interface.
func (r *relations) Name(id int) (string, error) {
	relationer, found := r.relationers[id]
	if !found {
		return "", errors.Errorf("unknown relation: %d", id)
	}
	return relationer.ru.Endpoint().Name, nil
}

// PrepareHook is part of the Relations interface.
func (r *relations) PrepareHook(hookInfo hook.Info) (string, error) {
	if !hookInfo.Kind.IsRelation() {
		return "", errors.Errorf("not a relation hook: %#v", hookInfo)
	}
	relationer, found := r.relationers[hookInfo.RelationId]
	if !found {
		return "", errors.Errorf("unknown relation: %d", hookInfo.RelationId)
	}
	return relationer.PrepareHook(hookInfo)
}

// CommitHook is part of the Relations interface.
func (r *relations) CommitHook(hookInfo hook.Info) error {
	if !hookInfo.Kind.IsRelation() {
		return errors.Errorf("not a relation hook: %#v", hookInfo)
	}
	relationer, found := r.relationers[hookInfo.RelationId]
	if !found {
		return errors.Errorf("unknown relation: %d", hookInfo.RelationId)
	}
	if hookInfo.Kind == hooks.RelationBroken {
		delete(r.relationers, hookInfo.RelationId)
	}
	return relationer.CommitHook(hookInfo)
}

// GetInfo is part of the Relations interface.
func (r *relations) GetInfo() map[int]*context.RelationInfo {
	relationInfos := map[int]*context.RelationInfo{}
	for id, relationer := range r.relationers {
		relationInfos[id] = relationer.ContextInfo()
	}
	return relationInfos
}

func (r *relations) update(remote map[int]remotestate.RelationSnapshot) error {
	for id, relationSnapshot := range remote {
		if _, found := r.relationers[id]; found {
			// We've seen this relation before. The only changes
			// we care about are to the lifecycle state, and to
			// the member settings versions. We handle differences
			// in settings in nextRelationHook.
			if relationSnapshot.Life == params.Dying {
				if err := r.setDying(id); err != nil {
					return errors.Trace(err)
				}
			}
			continue
		}
		// Relations that are not alive are simply skipped, because they
		// were not previously known anyway.
		if relationSnapshot.Life != params.Alive {
			continue
		}
		logger.Debugf("about to call RelationById(%v)", id)
		rel, err := r.st.RelationById(id)
		logger.Debugf("got rel=%v, err=%v", rel, err)
		if err != nil {
			if params.IsCodeNotFoundOrCodeUnauthorized(err) {
				continue
			}
			return errors.Trace(err)
		}
		// Make sure we ignore relations not implemented by the unit's charm.
		ch, err := corecharm.ReadCharmDir(r.charmDir)
		if err != nil {
			return errors.Trace(err)
		}
		if ep, err := rel.Endpoint(); err != nil {
			return errors.Trace(err)
		} else if !ep.ImplementedBy(ch) {
			logger.Warningf("skipping relation with unknown endpoint %q", ep.Name)
			continue
		}
		dir, err := ReadStateDir(r.relationsDir, id)
		if err != nil {
			return errors.Trace(err)
		}
		addErr := r.add(rel, dir)
		if addErr == nil {
			continue
		}
		removeErr := dir.Remove()
		if !params.IsCodeCannotEnterScope(addErr) {
			return errors.Trace(addErr)
		}
		if removeErr != nil {
			return errors.Trace(removeErr)
		}
	}
	return nil
}

// add causes the unit agent to join the supplied relation, and to
// store persistent state in the supplied dir. It will block until the
// operation succeeds or fails; or until the abort chan is closed, in
// which case it will return resolver.ErrLoopAborted.
func (r *relations) add(rel *caasoperator.Relation, dir *StateDir) (err error) {
	logger.Infof("relations.add(): %q, storing state in %v", rel, dir)
	ru, err := rel.Unit(r.caasUnit)
	if err != nil {
		return errors.Trace(err)
	}
	relationer := NewRelationer(ru, dir)
	logger.Debugf("  = about to call r.caasUnit.Watch() on caasUnit=%v", r.caasUnit)
	unitWatcher, err := r.caasUnit.Watch()
	logger.Debugf("  = got unitwatcher=%v", unitWatcher)
	if err != nil {
		return errors.Trace(err)
	}
	defer func() {
		if e := worker.Stop(unitWatcher); e != nil {
			if err == nil {
				err = e
			} else {
				logger.Errorf("while stopping unit watcher: %v", e)
			}
		}
	}()
	logger.Debugf("=Starting watcher loop in add()")
	for {
		select {
		case <-r.abort:
			// Should this be a different error? e.g. resolver.ErrAborted, that
			// Loop translates into ErrLoopAborted?
			return resolver.ErrLoopAborted
		case _, ok := <-unitWatcher.Changes():
			if !ok {
				return errors.New("unit watcher closed")
			}
			logger.Debugf("got unitWatcher changes, about to call relationer.Join()")
			err := relationer.Join()
			logger.Errorf("error calling relationer.Join(): %v", err)
			if params.IsCodeCannotEnterScopeYet(err) {
				logger.Debugf("cannot enter scope for relation %q; waiting for subordinate to be removed", rel)
				continue
			} else if err != nil {
				return errors.Trace(err)
			}
			logger.Debugf("joined relation %q", rel)
			r.relationers[rel.Id()] = relationer
			return nil
		}
	}
}

// setDying notifies the relationer identified by the supplied id that the
// only hook executions to be requested should be those necessary to cleanly
// exit the relation.
func (r *relations) setDying(id int) error {
	relationer, found := r.relationers[id]
	if !found {
		return nil
	}
	if err := relationer.SetDying(); err != nil {
		return errors.Trace(err)
	}
	if relationer.IsImplicit() {
		delete(r.relationers, id)
	}
	return nil
}
