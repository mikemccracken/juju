// Copyright 2012-2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package state

import (
	"fmt"

	"github.com/juju/errors"
	"github.com/juju/loggo"
	jujutxn "github.com/juju/txn"
	"gopkg.in/juju/charm.v6-unstable"
	"gopkg.in/juju/names.v2"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"gopkg.in/mgo.v2/txn"

	"github.com/juju/juju/network"
	"github.com/juju/juju/status"
)

var caasUnitLogger = loggo.GetLogger("juju.state.caasunit")

// caasUnitDoc represents the internal state of a unit in MongoDB.
// Note the correspondence with UnitInfo in apiserver/params.
type caasUnitDoc struct {
	DocID           string       `bson:"_id"`
	Name            string       `bson:"name"`
	ModelUUID       string       `bson:"model-uuid"`
	CAASApplication string       `bson:"caasapplication"`
	Resolved        ResolvedMode `bson:"resolved"`
	Life            Life         `bson:"life"`

	Addresses               []address
	PreferredPublicAddress  address `bson:",omitempty"`
	PreferredPrivateAddress address `bson:",omitempty"`
}

// Unit represents the state of a service unit.
type CAASUnit struct {
	st  *CAASState
	doc caasUnitDoc
}

func newCAASUnit(st *CAASState, cudoc *caasUnitDoc) *CAASUnit {
	caasunit := &CAASUnit{
		st:  st,
		doc: *cudoc,
	}
	return caasunit
}

// Application returns the application.
func (u *CAASUnit) Application() (*CAASApplication, error) {
	return u.st.CAASApplication(u.doc.CAASApplication)
}

// ConfigSettings returns the complete set of service charm config settings
// available to the unit. Unset values will be replaced with the default
// value for the associated option, and may thus be nil when no default is
// specified.
func (u *CAASUnit) ConfigSettings() (charm.Settings, error) {
	app, err := u.st.CAASApplication(u.doc.CAASApplication)
	if err != nil {
		return nil, err
	}
	charmURL := app.doc.CharmURL
	settings, err := readSettings(u.st, settingsC, applicationSettingsKey(u.doc.CAASApplication, charmURL))
	if err != nil {
		return nil, err
	}
	chrm, err := loadCharm(u.st, charmURL)
	if err != nil {
		return nil, err
	}
	result := chrm.Config().DefaultSettings()
	for name, value := range settings.Map() {
		result[name] = value
	}
	return result, nil
}

// ApplicationName returns the application name.
func (u *CAASUnit) ApplicationName() string {
	return u.doc.CAASApplication
}

// // Series returns the deployed charm's series.
// func (u *CAASUnit) Series() string {
// 	return u.doc.Series
// }

// String returns the unit as string.
func (u *CAASUnit) String() string {
	return u.doc.Name
}

// Name returns the unit name.
func (u *CAASUnit) Name() string {
	return u.doc.Name
}

// unitGlobalKey returns the global database key for the named unit.
func caasUnitGlobalKey(name string) string {
	return "cu#" + name + "#charm"
}

// globalWorkloadVersionKey returns the global database key for the
// workload version status key for this unit.
func caasGlobalWorkloadVersionKey(name string) string {
	return caasUnitGlobalKey(name) + "#sat#workload-version"
}

// globalAgentKey returns the global database key for the unit.
func (u *CAASUnit) globalAgentKey() string {
	return caasUnitAgentGlobalKey(u.doc.Name)
}

// globalMeterStatusKey returns the global database key for the meter status of the unit.
func (u *CAASUnit) globalMeterStatusKey() string {
	return caasUnitAgentGlobalKey(u.doc.Name)
}

// globalKey returns the global database key for the unit.
func (u *CAASUnit) globalKey() string {
	return caasUnitGlobalKey(u.doc.Name)
}

// globalWorkloadVersionKey returns the global database key for the unit's
// workload version info.
func (u *CAASUnit) globalWorkloadVersionKey() string {
	return caasGlobalWorkloadVersionKey(u.doc.Name)
}

// Life returns whether the unit is Alive, Dying or Dead.
func (u *CAASUnit) Life() Life {
	return u.doc.Life
}

// WorkloadVersion returns the version of the running workload set by
// the charm (eg, the version of postgresql that is running, as
// opposed to the version of the postgresql charm).
func (u *CAASUnit) WorkloadVersion() (string, error) {
	status, err := getStatus(u.st, u.globalWorkloadVersionKey(), "workload")
	if errors.IsNotFound(err) {
		return "", nil
	} else if err != nil {
		return "", errors.Trace(err)
	}
	return status.Message, nil
}

// SetWorkloadVersion sets the version of the workload that the unit
// is currently running.
func (u *CAASUnit) SetWorkloadVersion(version string) error {
	// Store in status rather than an attribute of the unit doc - we
	// want to avoid everything being an attr of the main docs to
	// stop a swarm of watchers being notified for irrelevant changes.
	now := u.st.clock.Now()
	return setStatus(u.st, setStatusParams{
		badge:     "workload",
		globalKey: u.globalWorkloadVersionKey(),
		status:    status.Active,
		message:   version,
		updated:   &now,
	})
}

// WorkloadVersionHistory returns a HistoryGetter which enables the
// caller to request past workload version changes.
func (u *CAASUnit) WorkloadVersionHistory() *HistoryGetter {
	return &HistoryGetter{st: u.st, globalKey: u.globalWorkloadVersionKey()}
}

// Destroy, when called on a Alive unit, advances its lifecycle as far as
// possible; it otherwise has no effect. In most situations, the unit's
// life is just set to Dying; but if a principal unit that is not assigned
// to a provisioned machine is Destroyed, it will be removed from state
// directly.
func (u *CAASUnit) Destroy() (err error) {
	defer func() {
		if err == nil {
			// This is a white lie; the document might actually be removed.
			u.doc.Life = Dying
		}
	}()
	unit := &CAASUnit{st: u.st, doc: u.doc}
	buildTxn := func(attempt int) ([]txn.Op, error) {
		if attempt > 0 {
			if err := unit.Refresh(); errors.IsNotFound(err) {
				return nil, jujutxn.ErrNoOperations
			} else if err != nil {
				return nil, err
			}
		}
		switch ops, err := unit.destroyOps(); err {
		case errRefresh:
		case errAlreadyDying:
			return nil, jujutxn.ErrNoOperations
		case nil:
			return ops, nil
		default:
			return nil, err
		}
		return nil, jujutxn.ErrNoOperations
	}
	if err = unit.st.db().Run(buildTxn); err == nil {
		if historyErr := unit.eraseHistory(); historyErr != nil {
			logger.Errorf("cannot delete history for unit %q: %v", unit.globalKey(), err)
		}
		if err = unit.Refresh(); errors.IsNotFound(err) {
			return nil
		}
	}
	return err
}

func (u *CAASUnit) eraseHistory() error {
	history, closer := u.st.db().GetCollection(statusesHistoryC)
	defer closer()
	historyW := history.Writeable()

	if _, err := historyW.RemoveAll(bson.D{{"statusid", u.globalKey()}}); err != nil {
		return err
	}
	if _, err := historyW.RemoveAll(bson.D{{"statusid", u.globalAgentKey()}}); err != nil {
		return err
	}
	return nil
}

// destroyOps returns the operations required to destroy the unit. If it
// returns errRefresh, the unit should be refreshed and the destruction
// operations recalculated.
func (u *CAASUnit) destroyOps() ([]txn.Op, error) {
	if u.doc.Life != Alive {
		return nil, errAlreadyDying
	}

	// XXX Check agent status info when implemented...

	removeOps, err := u.removeOps(isAliveDoc)
	if err == errAlreadyRemoved {
		return nil, errAlreadyDying
	} else if err != nil {
		return nil, err
	}
	return removeOps, nil
}

// removeOps returns the operations necessary to remove the unit, assuming
// the supplied asserts apply to the unit document.
func (u *CAASUnit) removeOps(asserts bson.D) ([]txn.Op, error) {
	app, err := u.st.CAASApplication(u.doc.CAASApplication)
	if errors.IsNotFound(err) {
		// If the application has been removed, the unit must already have been.
		return nil, errAlreadyRemoved
	} else if err != nil {
		return nil, err
	}
	return app.removeUnitOps(u, asserts)
}

// EnsureDead sets the unit lifecycle to Dead if it is Alive or Dying.
// It does nothing otherwise. If the unit has subordinates, it will
// return ErrUnitHasSubordinates; otherwise, if it has storage instances,
// it will return ErrUnitHasStorageInstances.
func (u *CAASUnit) EnsureDead() (err error) {
	if u.doc.Life == Dead {
		return nil
	}
	defer func() {
		if err == nil {
			u.doc.Life = Dead
		}
	}()

	ops := []txn.Op{{
		C:      caasUnitsC,
		Id:     u.doc.DocID,
		Assert: notDeadDoc,
		Update: bson.D{{"$set", bson.D{{"life", Dead}}}},
	}}
	if err := u.st.runTransaction(ops); err != txn.ErrAborted {
		return err
	}
	if notDead, err := isNotDead(u.st, caasUnitsC, u.doc.DocID); err != nil {
		return err
	} else if !notDead {
		return nil
	}
	if err := u.Refresh(); errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	return ErrUnitHasStorageAttachments
}

// Remove removes the unit from state, and may remove its service as well, if
// the service is Dying and no other references to it exist. It will fail if
// the unit is not Dead.
func (u *CAASUnit) Remove() (err error) {
	defer errors.DeferredAnnotatef(&err, "cannot remove unit %q", u)
	if u.doc.Life != Dead {
		return errors.New("unit is not dead")
	}

	// Now the unit is Dead, we can be sure that it's impossible for it to
	// enter relation scopes (once it's Dying, we can be sure of this; but
	// EnsureDead does not require that it already be Dying, so this is the
	// only point at which we can safely backstop lp:1233457 and mitigate
	// the impact of unit agent bugs that leave relation scopes occupied).
	// relations, err := applicationRelations(u.st, u.doc.CAASApplication)
	// if err != nil {
	// 	return err
	// }
	// for _, rel := range relations {
	// 	ru, err := rel.Unit(u)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	if err := ru.LeaveScope(); err != nil {
	// 		return err
	// 	}
	// }

	// Now we're sure we haven't left any scopes occupied by this unit, we
	// can safely remove the document.
	unit := &CAASUnit{st: u.st, doc: u.doc}
	buildTxn := func(attempt int) ([]txn.Op, error) {
		if attempt > 0 {
			if err := unit.Refresh(); errors.IsNotFound(err) {
				return nil, jujutxn.ErrNoOperations
			} else if err != nil {
				return nil, err
			}
		}
		switch ops, err := unit.removeOps(isDeadDoc); err {
		case errRefresh:
		case errAlreadyDying:
			return nil, jujutxn.ErrNoOperations
		case nil:
			return ops, nil
		default:
			return nil, err
		}
		return nil, jujutxn.ErrNoOperations
	}
	return unit.st.db().Run(buildTxn)
}

// Resolved returns the resolved mode for the unit.
func (u *CAASUnit) Resolved() ResolvedMode {
	return u.doc.Resolved
}

// RelationsJoined returns the relations for which the unit has entered scope
// and neither left it nor prepared to leave it
func (u *CAASUnit) RelationsJoined() ([]*Relation, error) {
	logger.Debugf("RelationsJoined")
	return u.relations(func(ru *RelationUnit) (bool, error) {
		return ru.Joined()
	})
}

// RelationsInScope returns the relations for which the unit has entered scope
// and not left it.
func (u *CAASUnit) RelationsInScope() ([]*Relation, error) {
	logger.Debugf("RelationsInScope")
	return u.relations(func(ru *RelationUnit) (bool, error) {
		return ru.InScope()
	})
}

// relations implements RelationsJoined and RelationsInScope.
func (u *CAASUnit) relations(predicate relationPredicate) ([]*Relation, error) {
	logger.Debugf("getting applicationRelations for CAASApplication")
	candidates, err := applicationRelations(u.st, u.doc.CAASApplication)
	if err != nil {
		return nil, err
	}

	logger.Debugf("in relations(), candidates is %v", candidates)
	var filtered []*Relation
	for _, relation := range candidates {
		relationUnit, err := relation.CAASUnit(u)
		if err != nil {
			return nil, err
		}
		if include, err := predicate(relationUnit); err != nil {
			return nil, err
		} else if include {
			filtered = append(filtered, relation)
		}
	}
	logger.Debugf("in relations(), filtered is %v", filtered)
	return filtered, nil
}

// // DeployerTag returns the tag of the agent responsible for deploying
// // the unit. If no such entity can be determined, false is returned.
// func (u *CAASUnit) DeployerTag() (names.Tag, bool) {
// 	if u.doc.Principal != "" {
// 		return names.NewUnitTag(u.doc.Principal), true
// 	} else if u.doc.MachineId != "" {
// 		return names.NewMachineTag(u.doc.MachineId), true
// 	}
// 	return nil, false
// }

// PublicAddress returns the public address of the unit.
func (u *CAASUnit) PublicAddress() (network.Address, error) {
	publicAddress := u.doc.PreferredPublicAddress.networkAddress()
	var err error
	if publicAddress.Value == "" {
		err = network.NoAddressError("public")
	}
	return publicAddress, err
}

// PrivateAddress returns the private address of the unit.
func (u *CAASUnit) PrivateAddress() (network.Address, error) {
	privateAddress := u.doc.PreferredPrivateAddress.networkAddress()
	var err error
	if privateAddress.Value == "" {
		err = network.NoAddressError("private")
	}
	return privateAddress, err
}

// Refresh refreshes the contents of the Unit from the underlying
// state. It an error that satisfies errors.IsNotFound if the unit has
// been removed.
func (u *CAASUnit) Refresh() error {
	units, closer := u.st.db().GetCollection(caasUnitsC)
	defer closer()

	err := units.FindId(u.doc.DocID).One(&u.doc)
	if err == mgo.ErrNotFound {
		return errors.NotFoundf("unit %q", u)
	}
	if err != nil {
		return fmt.Errorf("cannot refresh unit %q: %v", u, err)
	}
	return nil
}

// // Agent Returns an agent by its unit's name.
// func (u *CAASUnit) Agent() *UnitAgent {
// 	return newUnitAgent(u.st, u.Tag(), u.Name())
// }

// // AgentHistory returns an StatusHistoryGetter which can
// //be used to query the status history of the unit's agent.
// func (u *CAASUnit) AgentHistory() status.StatusHistoryGetter {
// 	return u.Agent()
// }

// // SetAgentStatus calls SetStatus for this unit's agent, this call
// // is equivalent to the former call to SetStatus when Agent and Unit
// // where not separate entities.
// func (u *CAASUnit) SetAgentStatus(agentStatus status.StatusInfo) error {
// 	agent := newUnitAgent(u.st, u.Tag(), u.Name())
// 	s := status.StatusInfo{
// 		Status:  agentStatus.Status,
// 		Message: agentStatus.Message,
// 		Data:    agentStatus.Data,
// 		Since:   agentStatus.Since,
// 	}
// 	return agent.SetStatus(s)
// }

// // AgentStatus calls Status for this unit's agent, this call
// // is equivalent to the former call to Status when Agent and Unit
// // where not separate entities.
// func (u *CAASUnit) AgentStatus() (status.StatusInfo, error) {
// 	agent := newUnitAgent(u.st, u.Tag(), u.Name())
// 	return agent.Status()
// }

// StatusHistory returns a slice of at most <size> StatusInfo items
// or items as old as <date> or items newer than now - <delta> time
// representing past statuses for this unit.
func (u *CAASUnit) StatusHistory(filter status.StatusHistoryFilter) ([]status.StatusInfo, error) {
	args := &statusHistoryArgs{
		st:        u.st,
		globalKey: u.globalKey(),
		filter:    filter,
	}
	return statusHistory(args)
}

// Status returns the status of the unit.
// This method relies on globalKey instead of globalAgentKey since it is part of
// the effort to separate Unit from UnitAgent. Now the Status for UnitAgent is in
// the UnitAgent struct.
func (u *CAASUnit) Status() (status.StatusInfo, error) {
	// The current health spec says when a hook error occurs, the workload should
	// be in error state, but the state model more correctly records the agent
	// itself as being in error. So we'll do that model translation here.
	// TODO(fwereade) as on unitagent, this transformation does not belong here.
	// For now, pretend we're always reading the unit status.
	info, err := getStatus(u.st, u.globalAgentKey(), "unit")
	if err != nil {
		return status.StatusInfo{}, err
	}
	if info.Status != status.Error {
		info, err = getStatus(u.st, u.globalKey(), "unit")
		if err != nil {
			return status.StatusInfo{}, err
		}
	}
	return info, nil
}

// SetStatus sets the status of the unit agent. The optional values
// allow to pass additional helpful status data.
// This method relies on globalKey instead of globalAgentKey since it is part of
// the effort to separate Unit from UnitAgent. Now the SetStatus for UnitAgent is in
// the UnitAgent struct.
func (u *CAASUnit) SetStatus(unitStatus status.StatusInfo) error {
	if !status.ValidWorkloadStatus(unitStatus.Status) {
		return errors.Errorf("cannot set invalid status %q", unitStatus.Status)
	}
	return setStatus(u.st, setStatusParams{
		badge:     "unit",
		globalKey: u.globalKey(),
		status:    unitStatus.Status,
		message:   unitStatus.Message,
		rawData:   unitStatus.Data,
		updated:   unitStatus.Since,
	})
}

// Tag returns a name identifying the unit.
// The returned name will be different from other Tag values returned by any
// other entities from the same state.
func (u *CAASUnit) Tag() names.Tag {
	return u.UnitTag()
}

// UnitTag returns a names.UnitTag representing this Unit, unless the
// unit Name is invalid, in which case it will panic
func (u *CAASUnit) UnitTag() names.UnitTag {
	return names.NewUnitTag(u.Name())
}

// Resolve marks the unit as having had any previous state transition
// problems resolved, and informs the unit that it may attempt to
// reestablish normal workflow. The retryHooks parameter informs
// whether to attempt to reexecute previous failed hooks or to continue
// as if they had succeeded before.
func (u *CAASUnit) Resolve(noretryHooks bool) error {
	// We currently check agent status to see if a unit is
	// in error state. As the new Juju Health work is completed,
	// this will change to checking the unit status.
	statusInfo, err := u.Status()
	if err != nil {
		return err
	}
	if statusInfo.Status != status.Error {
		return errors.Errorf("unit %q is not in an error state", u)
	}
	mode := ResolvedRetryHooks
	if noretryHooks {
		mode = ResolvedNoHooks
	}
	return u.SetResolved(mode)
}

// SetResolved marks the unit as having had any previous state transition
// problems resolved, and informs the unit that it may attempt to
// reestablish normal workflow. The resolved mode parameter informs
// whether to attempt to reexecute previous failed hooks or to continue
// as if they had succeeded before.
func (u *CAASUnit) SetResolved(mode ResolvedMode) (err error) {
	defer errors.DeferredAnnotatef(&err, "cannot set resolved mode for unit %q", u)
	switch mode {
	case ResolvedRetryHooks, ResolvedNoHooks:
	default:
		return fmt.Errorf("invalid error resolution mode: %q", mode)
	}
	// TODO(fwereade): assert unit has error status.
	resolvedNotSet := bson.D{{"resolved", ResolvedNone}}
	ops := []txn.Op{{
		C:      caasUnitsC,
		Id:     u.doc.DocID,
		Assert: append(notDeadDoc, resolvedNotSet...),
		Update: bson.D{{"$set", bson.D{{"resolved", mode}}}},
	}}
	if err := u.st.runTransaction(ops); err == nil {
		u.doc.Resolved = mode
		return nil
	} else if err != txn.ErrAborted {
		return err
	}
	if ok, err := isNotDead(u.st, caasUnitsC, u.doc.DocID); err != nil {
		return err
	} else if !ok {
		return ErrDead
	}
	// For now, the only remaining assert is that resolved was unset.
	return fmt.Errorf("already resolved")
}

// ClearResolved removes any resolved setting on the unit.
func (u *CAASUnit) ClearResolved() error {
	ops := []txn.Op{{
		C:      caasUnitsC,
		Id:     u.doc.DocID,
		Assert: txn.DocExists,
		Update: bson.D{{"$set", bson.D{{"resolved", ResolvedNone}}}},
	}}
	err := u.st.runTransaction(ops)
	if err != nil {
		return fmt.Errorf("cannot clear resolved mode for unit %q: %v", u, errors.NotFoundf("unit"))
	}
	u.doc.Resolved = ResolvedNone
	return nil
}

type addCAASUnitOpsArgs struct {
	caasUnitDoc        *caasUnitDoc
	agentStatusDoc     statusDoc
	workloadStatusDoc  statusDoc
	workloadVersionDoc statusDoc
	meterStatusDoc     *meterStatusDoc
}

// addUnitOps returns the operations required to add a unit to the units
// collection, along with all the associated expected other unit entries. This
// method is used by both the *Service.addUnitOpsWithCons method and the
// migration import code.
func addCAASUnitOps(st *CAASState, args addCAASUnitOpsArgs) ([]txn.Op, error) {
	name := args.caasUnitDoc.Name
	agentGlobalKey := unitAgentGlobalKey(name)

	// TODO: consider the constraints op
	// TODO: consider storageOps
	prereqOps := []txn.Op{
		createStatusOp(st, caasUnitGlobalKey(name), args.workloadStatusDoc),
		createStatusOp(st, agentGlobalKey, args.agentStatusDoc),
		createStatusOp(st, caasGlobalWorkloadVersionKey(name), args.workloadVersionDoc),
		// XXX createMeterStatusOp(st, agentGlobalKey, args.meterStatusDoc),
	}

	return append(prereqOps, txn.Op{
		C:      caasUnitsC,
		Id:     name,
		Assert: txn.DocMissing,
		Insert: args.caasUnitDoc,
	}), nil
}
