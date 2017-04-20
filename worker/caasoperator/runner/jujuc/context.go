// Copyright 2012, 2013, 2014 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package jujuc

import (
	"time"

	"gopkg.in/juju/charm.v6-unstable"

	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/network"
)

// RebootPriority is the type used for reboot requests.
type RebootPriority int

const (
	// RebootSkip is a noop.
	RebootSkip RebootPriority = iota
	// RebootAfterHook means wait for current hook to finish before
	// rebooting.
	RebootAfterHook
	// RebootNow means reboot immediately, killing and requeueing the
	// calling hook
	RebootNow
)

// Context is the interface that all hook helper commands
// depend on to interact with the rest of the system.
type Context interface {
	HookContext
	//	relationHookContext
	//	actionHookContext
}

// HookContext represents the information and functionality that is
// common to all charm hooks.
type HookContext interface {
	ContextCAASApplication
	ContextStatus
	ContextInstance
	// ContextNetworking
	// ContextMetrics
	//	ContextStorage
	ContextComponents
	// ContextRelations
	ContextVersion
}

type CaasApplicationHookContext interface {
	HookContext
}

// RelationHookContext is the context for a relation hook.
type RelationHookContext interface {
	HookContext
	relationHookContext
}

type relationHookContext interface {
	// HookRelation returns the ContextRelation associated with the executing
	// hook if it was found, or an error if it was not found (or is not available).
	HookRelation() (ContextRelation, error)

	// RemoteUnitName returns the name of the remote unit the hook execution
	// is associated with if it was found, and an error if it was not found or is not
	// available.
	RemoteUnitName() (string, error)
}

// ContextCAASApplication is the part of a hook context related to the application
type ContextCAASApplication interface {
	ApplicationName() string
	ConfigSettings() (charm.Settings, error)
	RunContainer(ContainerInfo) error
}

// ContextStatus is the part of a hook context related to the unit's status.
type ContextStatus interface {

	// ApplicationStatus returns the executing unit's service status
	// (including all units).
	ApplicationStatus() (ApplicationStatusInfo, error)

	// SetApplicationStatus updates the status for the unit's service.
	SetApplicationStatus(StatusInfo) error
}

// ContextInstance is the part of a hook context related to the unit's instance.
type ContextInstance interface {

	// RequestReboot will set the reboot flag to true on the machine agent
	RequestReboot(prio RebootPriority) error
}

// ContextNetworking is the part of a hook context related to network
// interface of the unit's instance.
type ContextNetworking interface {
	// PublicAddress returns the executing unit's public address or an
	// error if it is not available.
	PublicAddress() (string, error)

	// PrivateAddress returns the executing unit's private address or an
	// error if it is not available.
	PrivateAddress() (string, error)

	// OpenPorts marks the supplied port range for opening when the
	// executing unit's service is exposed.
	OpenPorts(protocol string, fromPort, toPort int) error

	// ClosePorts ensures the supplied port range is closed even when
	// the executing unit's service is exposed (unless it is opened
	// separately by a co- located unit).
	ClosePorts(protocol string, fromPort, toPort int) error

	// OpenedPorts returns all port ranges currently opened by this
	// unit on its assigned machine. The result is sorted first by
	// protocol, then by number.
	OpenedPorts() []network.PortRange

	// NetworkConfig returns the network configuration for the unit and the
	// given bindingName.
	//
	// TODO(dimitern): Currently, only the Address is populated, add the
	// rest later.
	//
	// LKK Card: https://canonical.leankit.com/Boards/View/101652562/119258804
	NetworkConfig(bindingName string) ([]params.NetworkConfig, error)
}

// ContextMetrics is the part of a hook context related to metrics.
type ContextMetrics interface {
	// AddMetric records a metric to return after hook execution.
	AddMetric(string, string, time.Time) error
}

// ContextComponents exposes modular Juju components as they relate to
// the unit in the context of the hook.
type ContextComponents interface {
	// Component returns the ContextComponent with the supplied name if
	// it was found.
	Component(name string) (ContextComponent, error)
}

// ContextRelations exposes the relations associated with the unit.
type ContextRelations interface {
	// Relation returns the relation with the supplied id if it was found, and
	// an error if it was not found or is not available.
	Relation(id int) (ContextRelation, error)

	// RelationIds returns the ids of all relations the executing unit is
	// currently participating in or an error if they are not available.
	RelationIds() ([]int, error)
}

// ContextComponent is a single modular Juju component as it relates to
// the current unit and hook. Components should implement this interfaces
// in a type-safe way. Ensuring checked type-conversions are preformed on
// the result and value interfaces. You will use the runner.RegisterComponentFunc
// to register a your components concrete ContextComponent implementation.
//
// See: process/context/context.go for an implementation example.
//
type ContextComponent interface {
	// Flush pushes the component's data to Juju state.
	// In the Flush implementation, call your components API.
	Flush() error
}

// ContextRelation expresses the capabilities of a hook with respect to a relation.
type ContextRelation interface {

	// Id returns an integer which uniquely identifies the relation.
	Id() int

	// Name returns the name the locally executing charm assigned to this relation.
	Name() string

	// FakeId returns a string of the form "relation-name:123", which uniquely
	// identifies the relation to the hook. In reality, the identification
	// of the relation is the integer following the colon, but the composed
	// name is useful to humans observing it.
	FakeId() string

	// Settings allows read/write access to the local unit's settings in
	// this relation.
	Settings() (Settings, error)

	// UnitNames returns a list of the remote units in the relation.
	UnitNames() []string

	// ReadSettings returns the settings of any remote unit in the relation.
	ReadSettings(unit string) (params.Settings, error)
}

// ContextVersion expresses the parts of a hook context related to
// reporting workload versions.
type ContextVersion interface {

	// UnitWorkloadVersion returns the currently set workload version for
	// the unit.
	ApplicationWorkloadVersion() (string, error)

	// SetUnitWorkloadVersion updates the workload version for the unit.
	SetApplicationWorkloadVersion(string) error
}

// Settings is implemented by types that manipulate unit settings.
type Settings interface {
	Map() params.Settings
	Set(string, string)
	Delete(string)
}
