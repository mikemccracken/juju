// Copyright 2012-2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package remotestate

import (
	"sync"
	"time"

	"github.com/juju/errors"
	"github.com/juju/loggo"
	"gopkg.in/juju/names.v2"
	"gopkg.in/juju/worker.v1"

	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/watcher"
	jworker "github.com/juju/juju/worker"
	"github.com/juju/juju/worker/catacomb"
)

var logger = loggo.GetLogger("juju.worker.caasoperator.remotestate")

// RemoteStateWatcher collects caasunit, service, and service config information
// from separate state watchers, and updates a Snapshot which is sent on a
// channel upon change.
type RemoteStateWatcher struct {
	st                        State
	caasUnit                  CAASUnit
	app                       CAASApplication
	relations                 map[names.RelationTag]*relationUnitsWatcher
	relationUnitsChanges      chan relationUnitsChange
	storageAttachmentWatchers map[names.StorageTag]*storageAttachmentWatcher
	storageAttachmentChanges  chan storageAttachmentChange
	updateStatusChannel       func() <-chan time.Time
	commandChannel            <-chan string
	retryHookChannel          <-chan struct{}

	catacomb catacomb.Catacomb

	out     chan struct{}
	mu      sync.Mutex
	current Snapshot
}

// WatcherConfig holds configuration parameters for the
// remote state watcher.
type WatcherConfig struct {
	State               State
	UpdateStatusChannel func() <-chan time.Time
	CommandChannel      <-chan string
	RetryHookChannel    <-chan struct{}
	ApplicationTag      names.ApplicationTag
}

// NewWatcher returns a RemoteStateWatcher that handles state changes pertaining to the
// supplied unit.
func NewWatcher(config WatcherConfig) (*RemoteStateWatcher, error) {
	w := &RemoteStateWatcher{
		st:                        config.State,
		relations:                 make(map[names.RelationTag]*relationUnitsWatcher),
		relationUnitsChanges:      make(chan relationUnitsChange),
		storageAttachmentWatchers: make(map[names.StorageTag]*storageAttachmentWatcher),
		storageAttachmentChanges:  make(chan storageAttachmentChange),
		updateStatusChannel:       config.UpdateStatusChannel,
		commandChannel:            config.CommandChannel,
		retryHookChannel:          config.RetryHookChannel,
		// Note: it is important that the out channel be buffered!
		// The remote state watcher will perform a non-blocking send
		// on the channel to wake up the observer. It is non-blocking
		// so that we coalesce events while the observer is busy.
		out: make(chan struct{}, 1),
		current: Snapshot{
			Relations: make(map[int]RelationSnapshot),
			Storage:   make(map[names.StorageTag]StorageSnapshot),
		},
	}
	err := catacomb.Invoke(catacomb.Plan{
		Site: &w.catacomb,
		Work: func() error {
			return w.loop(config.ApplicationTag)
		},
	})
	if err != nil {
		return nil, errors.Trace(err)
	}
	return w, nil
}

// Kill is part of the worker.Worker interface.
func (w *RemoteStateWatcher) Kill() {
	w.catacomb.Kill(nil)
}

// Wait is part of the worker.Worker interface.
func (w *RemoteStateWatcher) Wait() error {
	return w.catacomb.Wait()
}

func (w *RemoteStateWatcher) RemoteStateChanged() <-chan struct{} {
	return w.out
}

func (w *RemoteStateWatcher) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	snapshot := w.current
	snapshot.Relations = make(map[int]RelationSnapshot)
	for id, relationSnapshot := range w.current.Relations {
		relationSnapshotCopy := RelationSnapshot{
			Life:    relationSnapshot.Life,
			Members: make(map[string]int64),
		}
		for name, version := range relationSnapshot.Members {
			relationSnapshotCopy.Members[name] = version
		}
		snapshot.Relations[id] = relationSnapshotCopy
	}
	snapshot.Storage = make(map[names.StorageTag]StorageSnapshot)
	for tag, storageSnapshot := range w.current.Storage {
		snapshot.Storage[tag] = storageSnapshot
	}
	snapshot.Actions = make([]string, len(w.current.Actions))
	copy(snapshot.Actions, w.current.Actions)
	snapshot.Commands = make([]string, len(w.current.Commands))
	copy(snapshot.Commands, w.current.Commands)
	return snapshot
}

func (w *RemoteStateWatcher) ClearResolvedMode() {
	w.mu.Lock()
	w.current.ResolvedMode = params.ResolvedNone
	w.mu.Unlock()
}

func (w *RemoteStateWatcher) CommandCompleted(completed string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, id := range w.current.Commands {
		if id != completed {
			continue
		}
		w.current.Commands = append(
			w.current.Commands[:i],
			w.current.Commands[i+1:]...,
		)
		break
	}
}

func (w *RemoteStateWatcher) setUp(applicationtag names.ApplicationTag) (err error) {
	// TODO(dfc) named return value is a time bomb
	// TODO(axw) move this logic.
	defer func() {
		cause := errors.Cause(err)
		if params.IsCodeNotFoundOrCodeUnauthorized(cause) {
			err = jworker.ErrTerminateAgent
		}
	}()

	w.app, err = w.st.CAASApplication(applicationtag)
	if err != nil {
		return errors.Trace(err)
	}

	fakeCaasUnitTag, err := names.ParseUnitTag("unit-" + applicationtag.Id() + "/0")
	if err != nil {
		return errors.Trace(err)
	}
	w.caasUnit, err = w.st.CAASUnit(fakeCaasUnitTag)
	if err != nil {
		return errors.Trace(err)
	}
	return nil
}

func (w *RemoteStateWatcher) loop(caasApplicationTag names.ApplicationTag) (err error) {
	if err := w.setUp(caasApplicationTag); err != nil {
		return errors.Trace(err)
	}
	logger.Debugf("remotestatewatcher setup correctly.")
	var requiredEvents int

	var seenApplicationChange bool
	appw, err := w.app.Watch()
	if err != nil {
		return errors.Trace(err)
	}
	if err := w.catacomb.Add(appw); err != nil {
		return errors.Trace(err)
	}
	requiredEvents++
	logger.Debugf("app.watch created.")

	var seenUnitsChange bool
	unitsw, err := w.app.WatchUnits()
	if err != nil {
		return errors.Trace(err)
	}
	if err := w.catacomb.Add(unitsw); err != nil {
		return errors.Trace(err)
	}
	requiredEvents++
	logger.Debugf("watchunits created.")
	// var seenConfigChange bool
	// configw, err := w.unit.WatchConfigSettings()
	// if err != nil {
	// 	return errors.Trace(err)
	// }
	// if err := w.catacomb.Add(configw); err != nil {
	// 	return errors.Trace(err)
	// }
	// requiredEvents++

	var seenRelationsChange bool
	relationsw, err := w.app.WatchRelations()
	if err != nil {
		return errors.Trace(err)
	}
	if err := w.catacomb.Add(relationsw); err != nil {
		return errors.Trace(err)
	}
	requiredEvents++
	logger.Debugf("relationsw created.")

	// var seenAddressesChange bool
	// addressesw, err := w.unit.WatchAddresses()
	// if err != nil {
	// 	return errors.Trace(err)
	// }
	// if err := w.catacomb.Add(addressesw); err != nil {
	// 	return errors.Trace(err)
	// }
	// requiredEvents++

	// var seenActionsChange bool
	// actionsw, err := w.unit.WatchActionNotifications()
	// if err != nil {
	// 	return errors.Trace(err)
	// }
	// if err := w.catacomb.Add(actionsw); err != nil {
	// 	return errors.Trace(err)
	// }
	// requiredEvents++

	var eventsObserved int
	observedEvent := func(flag *bool) {
		if !*flag {
			*flag = true
			eventsObserved++
		}
	}

	// fire will, once the first event for each watcher has
	// been observed, send a signal on the out channel.
	fire := func() {
		if eventsObserved != requiredEvents {
			return
		}
		select {
		case w.out <- struct{}{}:
		default:
		}
	}

	for {
		select {
		case <-w.catacomb.Dying():
			return w.catacomb.ErrDying()

		case _, ok := <-appw.Changes():
			logger.Debugf("got app change")
			if !ok {
				return errors.New("app watcher closed")
			}
			if err := w.applicationChanged(); err != nil {
				return errors.Trace(err)
			}
			observedEvent(&seenApplicationChange)

		case _, ok := <-unitsw.Changes():
			logger.Debugf("got units change")
			if !ok {
				return errors.New("units watcher closed")
			}
			if err := w.unitsChanged(); err != nil {
				return errors.Trace(err)
			}
			observedEvent(&seenUnitsChange)

		// case _, ok := <-configw.Changes():
		// 	logger.Debugf("got config change: ok=%t", ok)
		// 	if !ok {
		// 		return errors.New("config watcher closed")
		// 	}
		// 	if err := w.configChanged(); err != nil {
		// 		return errors.Trace(err)
		// 	}
		// 	observedEvent(&seenConfigChange)

		// case _, ok := <-addressesw.Changes():
		// 	logger.Debugf("got address change: ok=%t", ok)
		// 	if !ok {
		// 		return errors.New("addresses watcher closed")
		// 	}
		// 	if err := w.addressesChanged(); err != nil {
		// 		return errors.Trace(err)
		// 	}
		// 	observedEvent(&seenAddressesChange)

		// case actions, ok := <-actionsw.Changes():
		// 	logger.Debugf("got action change: %v ok=%t", actions, ok)
		// 	if !ok {
		// 		return errors.New("actions watcher closed")
		// 	}
		// 	if err := w.actionsChanged(actions); err != nil {
		// 		return errors.Trace(err)
		// 	}
		// 	observedEvent(&seenActionsChange)

		case keys, ok := <-relationsw.Changes():
			logger.Debugf("got relations change: ok=%t, keys=%v", ok, keys)
			if !ok {
				return errors.New("relations watcher closed")
			}
			if err := w.relationsChanged(keys); err != nil {
				return errors.Trace(err)
			}
			observedEvent(&seenRelationsChange)

		// case keys, ok := <-storagew.Changes():
		// 	logger.Debugf("got storage change: %v ok=%t", keys, ok)
		// 	if !ok {
		// 		return errors.New("storage watcher closed")
		// 	}
		// 	if err := w.storageChanged(keys); err != nil {
		// 		return errors.Trace(err)
		// 	}
		// 	observedEvent(&seenStorageChange)

		// case change := <-w.storageAttachmentChanges:
		// 	logger.Debugf("storage attachment change %v", change)
		// 	if err := w.storageAttachmentChanged(change); err != nil {
		// 		return errors.Trace(err)
		// 	}

		case change := <-w.relationUnitsChanges:
			logger.Debugf("got a relation units change: %v", change)
			if err := w.relationUnitsChanged(change); err != nil {
				return errors.Trace(err)
			}

		case <-w.updateStatusChannel():
			logger.Debugf("update status timer triggered")
			if err := w.updateStatusChanged(); err != nil {
				return errors.Trace(err)
			}

		case id, ok := <-w.commandChannel:
			if !ok {
				return errors.New("commandChannel closed")
			}
			logger.Debugf("command enqueued: %v", id)
			if err := w.commandsChanged(id); err != nil {
				return err
			}

		case _, ok := <-w.retryHookChannel:
			if !ok {
				return errors.New("retryHookChannel closed")
			}
			logger.Debugf("retry hook timer triggered")
			if err := w.retryHookTimerTriggered(); err != nil {
				return err
			}
		}

		// Something changed.
		fire()
	}
}

// updateStatusChanged is called when the update status timer expires.
func (w *RemoteStateWatcher) updateStatusChanged() error {
	w.mu.Lock()
	w.current.UpdateStatusVersion++
	w.mu.Unlock()
	return nil
}

// commandsChanged is called when a command is enqueued.
func (w *RemoteStateWatcher) commandsChanged(id string) error {
	w.mu.Lock()
	w.current.Commands = append(w.current.Commands, id)
	w.mu.Unlock()
	return nil
}

// retryHookTimerTriggered is called when the retry hook timer expires.
func (w *RemoteStateWatcher) retryHookTimerTriggered() error {
	w.mu.Lock()
	w.current.RetryHookVersion++
	w.mu.Unlock()
	return nil
}

// unitsChanged responds to changes any of the application's CAAS units.
func (w *RemoteStateWatcher) unitsChanged() error {
	// XXX CAAS this probably warrants its own hook but just trigger config-changed for now
	w.mu.Lock()
	w.current.ConfigVersion++
	w.mu.Unlock()
	return nil
}

// applicationChanged responds to changes in the application.
func (w *RemoteStateWatcher) applicationChanged() error {
	if err := w.app.Refresh(); err != nil {
		return errors.Trace(err)
	}
	url, force, err := w.app.CharmURL()
	if err != nil {
		return errors.Trace(err)
	}
	ver, err := w.app.CharmModifiedVersion()
	if err != nil {
		return errors.Trace(err)
	}
	w.mu.Lock()
	w.current.CharmURL = url
	w.current.ForceCharmUpgrade = force
	w.current.CharmModifiedVersion = ver
	w.mu.Unlock()
	return nil
}

func (w *RemoteStateWatcher) configChanged() error {
	w.mu.Lock()
	w.current.ConfigVersion++
	w.mu.Unlock()
	return nil
}

func (w *RemoteStateWatcher) addressesChanged() error {
	w.mu.Lock()
	w.current.ConfigVersion++
	w.mu.Unlock()
	return nil
}

// relationsChanged responds to app relation changes.
func (w *RemoteStateWatcher) relationsChanged(keys []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	logger.Debugf("in RemoteStateWatcher.relationsChanged, keys = %v", keys)

	for _, key := range keys {
		logger.Debugf("^^ examining key %v", key)
		relationTag := names.NewRelationTag(key)
		logger.Debugf("^^ got relationTag: %v", relationTag)
		rel, err := w.st.Relation(relationTag)
		logger.Debugf("^^ got rel=%v, err=%v", rel, err)
		if params.IsCodeNotFoundOrCodeUnauthorized(err) {
			logger.Debugf("^^ params.IsCodeNotFoundOrCodeUnauthorized was true")
			// If it's actually gone, this unit cannot have entered
			// scope, and therefore never needs to know about it.
			if ruw, ok := w.relations[relationTag]; ok {
				logger.Debugf("^^ stopping relation unit watcher")
				worker.Stop(ruw)
				delete(w.relations, relationTag)
				delete(w.current.Relations, ruw.relationId)
			}
		} else if err != nil {
			logger.Errorf("returning with err %v", err)
			return errors.Trace(err)
		} else {
			if _, ok := w.relations[relationTag]; ok {
				logger.Debugf("^^ Already watching relation %v, updating snapshot and bailing", relationTag)
				relationSnapshot := w.current.Relations[rel.Id()]
				relationSnapshot.Life = rel.Life()
				w.current.Relations[rel.Id()] = relationSnapshot
				continue
			}
			logger.Debugf("^^ about to call WatchRelationUnits(%v, %v)", relationTag, w.caasUnit.Tag())
			ruw, err := w.st.WatchRelationUnits(relationTag, w.caasUnit.Tag())
			if err != nil {
				return errors.Trace(err)
			}
			logger.Debugf("^^ adding to catacomb")
			// Because of the delay before handing off responsibility to
			// newRelationUnitsWatcher below, add to our own catacomb to
			// ensure errors get picked up if they happen.
			if err := w.catacomb.Add(ruw); err != nil {
				return errors.Trace(err)
			}
			logger.Debugf("^^ calling watchRelationUnits")
			if err := w.watchRelationUnits(rel, relationTag, ruw); err != nil {
				logger.Errorf("^^ error from watchRelationUnits: %v", err)
				return errors.Trace(err)
			}
		}
	}
	logger.Debugf("successful relationsChanged()")
	return nil
}

// watchRelationUnits starts watching the relation units for the given
// relation, waits for its first event, and records the information in
// the current snapshot.
func (w *RemoteStateWatcher) watchRelationUnits(
	rel Relation, relationTag names.RelationTag, ruw watcher.RelationUnitsWatcher,
) error {
	relationSnapshot := RelationSnapshot{
		Life:    rel.Life(),
		Members: make(map[string]int64),
	}
	select {
	case <-w.catacomb.Dying():
		return w.catacomb.ErrDying()
	case change, ok := <-ruw.Changes():
		logger.Debugf("^^^ Got change %v from ruw", change)
		if !ok {
			return errors.New("relation units watcher closed")
		}
		for unit, settings := range change.Changed {
			relationSnapshot.Members[unit] = settings.Version
		}
	}
	logger.Debugf("getting new innerRUW from newRelationUnitsWatcher")
	innerRUW, err := newRelationUnitsWatcher(rel.Id(), ruw, w.relationUnitsChanges)
	if err != nil {
		return errors.Trace(err)
	}
	logger.Debugf("got innerRUW from newRelationUnitsWatcher: %v", innerRUW)
	if err := w.catacomb.Add(innerRUW); err != nil {
		return errors.Trace(err)
	}
	w.current.Relations[rel.Id()] = relationSnapshot
	w.relations[relationTag] = innerRUW
	return nil
}

// relationUnitsChanged responds to relation units changes.
func (w *RemoteStateWatcher) relationUnitsChanged(change relationUnitsChange) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	snapshot, ok := w.current.Relations[change.relationId]
	if !ok {
		return nil
	}
	for unit, settings := range change.Changed {
		snapshot.Members[unit] = settings.Version
	}
	for _, unit := range change.Departed {
		delete(snapshot.Members, unit)
	}
	return nil
}

// storageAttachmentChanged responds to storage attachment changes.
func (w *RemoteStateWatcher) storageAttachmentChanged(change storageAttachmentChange) error {
	w.mu.Lock()
	w.current.Storage[change.Tag] = change.Snapshot
	w.mu.Unlock()
	return nil
}

func (w *RemoteStateWatcher) actionsChanged(actions []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.current.Actions = append(w.current.Actions, actions...)
	return nil
}

// storageChanged responds to unit storage changes.
func (w *RemoteStateWatcher) storageChanged(keys []string) error {
	// tags := make([]names.StorageTag, len(keys))
	// for i, key := range keys {
	// 	tags[i] = names.NewStorageTag(key)
	// }
	// ids := make([]params.StorageAttachmentId, len(keys))
	// for i, tag := range tags {
	// 	ids[i] = params.StorageAttachmentId{
	// 		StorageTag:      tag.String(),
	// 		CaasOperatorTag: w.caasoperator.Tag().String(),
	// 	}
	// }
	// results, err := w.st.StorageAttachmentLife(ids)
	// if err != nil {
	// 	return errors.Trace(err)
	// }

	// w.mu.Lock()
	// defer w.mu.Unlock()

	// for i, result := range results {
	// 	tag := tags[i]
	// 	if result.Error == nil {
	// 		if storageSnapshot, ok := w.current.Storage[tag]; ok {
	// 			// We've previously started a watcher for this storage
	// 			// attachment, so all we needed to do was update the
	// 			// lifecycle state.
	// 			storageSnapshot.Life = result.Life
	// 			w.current.Storage[tag] = storageSnapshot
	// 			continue
	// 		}
	// 		// We haven't seen this storage attachment before, so start
	// 		// a watcher now; add it to our catacomb in case of mishap;
	// 		// and wait for the initial event.
	// 		saw, err := w.st.WatchStorageAttachment(tag, w.caasoperator.Tag())
	// 		if err != nil {
	// 			return errors.Annotate(err, "watching storage attachment")
	// 		}
	// 		if err := w.catacomb.Add(saw); err != nil {
	// 			return errors.Trace(err)
	// 		}
	// 		if err := w.watchStorageAttachment(tag, result.Life, saw); err != nil {
	// 			return errors.Trace(err)
	// 		}
	// 	} else if params.IsCodeNotFound(result.Error) {
	// 		if watcher, ok := w.storageAttachmentWatchers[tag]; ok {
	// 			// already under catacomb management, any error tracked already
	// 			worker.Stop(watcher)
	// 			delete(w.storageAttachmentWatchers, tag)
	// 		}
	// 		delete(w.current.Storage, tag)
	// 	} else {
	// 		return errors.Annotatef(
	// 			result.Error, "getting life of %s attachment",
	// 			names.ReadableString(tag),
	// 		)
	// 	}
	// }
	return nil
}
