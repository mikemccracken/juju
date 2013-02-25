package api

import (
	"fmt"
	"launchpad.net/juju-core/log"
	"launchpad.net/tomb"
	"strings"
	"sync"
)

// Machine represents the state of a machine.
type Machine struct {
	st  *State
	id  string
	doc rpcMachine
}

// Client represents the client-accessible part of the state.
type Client struct {
	st *State
}

// Client returns an object that can be used
// to access client-specific functionality.
func (st *State) Client() *Client {
	return &Client{st}
}

// MachineInfo holds information about a machine.
type MachineInfo struct {
	InstanceId string // blank if not set.
}

// Status holds information about the status of a juju environment.
type Status struct {
	Machines map[string]MachineInfo
	// TODO the rest
}

// Status returns the status of the juju environment.
func (c *Client) Status() (*Status, error) {
	var s Status
	if err := c.st.call("Client", "", "Status", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Machine returns a reference to the machine with the given id.
func (st *State) Machine(id string) (*Machine, error) {
	m := &Machine{
		st: st,
		id: id,
	}
	if err := m.Refresh(); err != nil {
		return nil, err
	}
	return m, nil
}

// Unit represents the state of a service unit.
type Unit struct {
	st   *State
	name string
	doc  rpcUnit
}

// Unit returns a unit by name.
func (st *State) Unit(name string) (*Unit, error) {
	u := &Unit{
		st:   st,
		name: name,
	}
	if err := u.Refresh(); err != nil {
		return nil, err
	}
	return u, nil
}

// Login authenticates as the entity with the given name and password.
// Subsequent requests on the state will act as that entity.
// This method is usually called automatically by Open.
func (st *State) Login(entityName, password string) error {
	return st.call("Admin", "", "Login", &rpcCreds{
		EntityName: entityName,
		Password:   password,
	}, nil)
}

// Id returns the machine id.
func (m *Machine) Id() string {
	return m.id
}

// EntityName returns a name identifying the machine that is safe to use
// as a file name.  The returned name will be different from other
// EntityName values returned by any other entities from the same state.
func (m *Machine) EntityName() string {
	return MachineEntityName(m.Id())
}

// MachineEntityName returns the entity name for the
// machine with the given id.
func MachineEntityName(id string) string {
	return fmt.Sprintf("machine-%s", id)
}

// Refresh refreshes the contents of the machine from the underlying
// state. TODO(rog) It returns a NotFoundError if the machine has been removed.
func (m *Machine) Refresh() error {
	return m.st.call("Machine", m.id, "Get", nil, &m.doc)
}

// String returns the machine's id.
func (m *Machine) String() string {
	return m.id
}

// InstanceId returns the provider specific instance id for this machine
// and whether it has been set.
func (m *Machine) InstanceId() (string, bool) {
	return m.doc.InstanceId, m.doc.InstanceId != ""
}

// SetPassword sets the password for the machine's agent.
func (m *Machine) SetPassword(password string) error {
	return m.st.call("Machine", m.id, "SetPassword", &rpcPassword{
		Password: password,
	}, nil)
}

func (m *Machine) Watch() *EntityWatcher {
	return newEntityWatcher(m.st, "Machine", m.id)
}

type EntityWatcher struct {
	tomb  tomb.Tomb
	wg    sync.WaitGroup
	st    *State
	etype string
	eid   string
	out   chan struct{}
}

func newEntityWatcher(st *State, etype, id string) *EntityWatcher {
	w := &EntityWatcher{
		st:    st,
		etype: etype,
		eid:   id,
		out:   make(chan struct{}),
	}
	go func() {
		defer w.tomb.Done()
		defer close(w.out)
		defer w.wg.Wait() // Wait for watcher to be stopped.
		w.tomb.Kill(w.loop())
	}()
	return w
}

func (w *EntityWatcher) loop() error {
	var id rpcEntityWatcherId
	if err := w.st.call(w.etype, w.eid, "Watch", nil, &id); err != nil {
		return err
	}
	callWatch := func(request string) error {
		return w.st.call("EntityWatcher", id.EntityWatcherId, request, nil, nil)
	}
	// When the EntityWatcher has been stopped, we send a stop
	// request to the server, which will remove the watcher
	// and return a CodeStopped error to any currently outstanding call
	// to Next. If a call to Next happens just after the watcher
	// has been stopped, we'll get a CodeNotFound error;
	// Either way we'll return, wait for the stop request to
	// complete, and the watcher will die with all resources
	// cleaned up.
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		<-w.tomb.Dying()
		if err := callWatch("Stop"); err != nil {
			log.Printf("state/api: error trying to stop watcher: %v", err)
		}
	}()
	for {
		select {
		case <-w.tomb.Dying():
			return tomb.ErrDying
		case w.out <- struct{}{}:
			// Note that because the change notification
			// contains no information, there's no point in
			// calling Next again until we have sent a notification
			// on w.out.
		}
		if err := callWatch("Next"); err != nil {
			if code := ErrCode(err); code == CodeStopped || code == CodeNotFound {
				err = nil
			}
			return err
		}
	}
	panic("unreachable")
}

func (w *EntityWatcher) Changes() <-chan struct{} {
	return w.out
}

func (w *EntityWatcher) Stop() error {
	w.tomb.Kill(nil)
	return w.tomb.Wait()
}

func (w *EntityWatcher) Err() error {
	return w.tomb.Err()
}

// Refresh refreshes the contents of the Unit from the underlying
// state. TODO(rog) It returns a NotFoundError if the unit has been removed.
func (u *Unit) Refresh() error {
	return u.st.call("Unit", u.name, "Get", nil, &u.doc)
}

// SetPassword sets the password for the unit's agent.
func (u *Unit) SetPassword(password string) error {
	return u.st.call("Unit", u.name, "SetPassword", &rpcPassword{
		Password: password,
	}, nil)
}

// UnitEntityName returns the entity name for the
// unit with the given name.
func UnitEntityName(unitName string) string {
	return "unit-" + strings.Replace(unitName, "/", "-", -1)
}

// EntityName returns a name identifying the unit that is safe to use
// as a file name.  The returned name will be different from other
// EntityName values returned by any other entities from the same state.
func (u *Unit) EntityName() string {
	return UnitEntityName(u.name)
}

// DeployerName returns the entity name of the agent responsible for deploying
// the unit. If no such entity can be determined, false is returned.
func (u *Unit) DeployerName() (string, bool) {
	return u.doc.DeployerName, u.doc.DeployerName != ""
}
