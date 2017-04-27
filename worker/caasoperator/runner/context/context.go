// Copyright 2012-2014 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

// Package context contains the ContextFactory and Context definitions. Context implements
// jujuc.Context and is used together with caasoperator.Runner to run hooks, commands and actions.
package context

import (
	"strings"
	"sync"
	"time"

	"github.com/juju/errors"
	"github.com/juju/loggo"
	"github.com/juju/utils/clock"
	"github.com/juju/utils/proxy"
	"gopkg.in/juju/charm.v6-unstable"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/rest"

	"github.com/juju/juju/api/base"
	"github.com/juju/juju/api/caasoperator"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/network"
	"github.com/juju/juju/worker/caasoperator/runner/jujuc"
)

// Paths exposes the paths needed by Context.
type Paths interface {

	// GetToolsDir returns the filesystem path to the dirctory containing
	// the hook tool symlinks.
	GetToolsDir() string

	// GetCharmDir returns the filesystem path to the directory in which
	// the charm is installed.
	GetCharmDir() string

	// GetJujucSocket returns the path to the socket used by the hook tools
	// to communicate back to the executing caasoperator process. It might be a
	// filesystem path, or it might be abstract.
	GetJujucSocket() string

	// GetMetricsSpoolDir returns the path to a metrics spool dir, used
	// to store metrics recorded during a single hook run.
	GetMetricsSpoolDir() string

	// ComponentDir returns the filesystem path to the directory
	// containing all data files for a component.
	ComponentDir(name string) string
}

var logger = loggo.GetLogger("juju.worker.caasoperator.context")
var mutex = sync.Mutex{}

// ComponentConfig holds all the information related to a hook context
// needed by components.
type ComponentConfig struct {
	AppName   string
	DataDir   string
	APICaller base.APICaller
}

// ComponentFunc is a factory function for Context components.
type ComponentFunc func(ComponentConfig) (jujuc.ContextComponent, error)

var registeredComponentFuncs = map[string]ComponentFunc{}

// Add the named component factory func to the registry.
func RegisterComponentFunc(name string, f ComponentFunc) error {
	if _, ok := registeredComponentFuncs[name]; ok {
		return errors.AlreadyExistsf("%s", name)
	}
	registeredComponentFuncs[name] = f
	return nil
}

// meterStatus describes the caasoperator's meter status.
type meterStatus struct {
	code string
	info string
}

// HookProcess is an interface representing a process running a hook.
type HookProcess interface {
	Pid() int
	Kill() error
}

// HookContext is the implementation of jujuc.Context.
type HookContext struct {
	app *caasoperator.CAASApplication

	// state is the handle to the caasoperator State so that HookContext can make
	// API calls on the stateservice.
	// NOTE: We would like to be rid of the fake-remote-Caasoperator and switch
	// over fully to API calls on State.  This adds that ability, but we're
	// not fully there yet.
	state *caasoperator.State

	// privateAddress is the cached value of the caas unit's private
	// address.
	privateAddress string

	// publicAddress is the cached value of the caas unit's public
	// address.
	publicAddress string

	// configSettings holds the service configuration.
	configSettings charm.Settings

	// id identifies the context.
	id string

	// actionData contains the values relevant to the run of an Action:
	// its tag, its parameters, and its results.
	actionData *ActionData

	// uuid is the universally unique identifier of the environment.
	uuid string

	// envName is the human friendly name of the environment.
	envName string

	// applicationName is the human friendly name of the local application.
	applicationName string

	// status is the status of the local caasoperator.
	status *jujuc.StatusInfo

	// relationId identifies the relation for which a relation hook is
	// executing. If it is -1, the context is not running a relation hook;
	// otherwise, its value must be a valid key into the relations map.
	relationId int

	// remoteUnitName identifies the changing caasoperator of the executing relation
	// hook. It will be empty if the context is not running a relation hook,
	// or if it is running a relation-broken hook.
	remoteUnitName string

	// relations contains the context for every relation the caasoperator is a member
	// of, keyed on relation id.
	relations map[int]*ContextRelation

	// apiAddrs contains the API server addresses.
	apiAddrs []string

	// proxySettings are the current proxy settings that the caasoperator knows about.
	proxySettings proxy.Settings

	// meterStatus is the status of the caasoperator's metering.
	meterStatus *meterStatus

	// pendingPorts contains a list of port ranges to be opened or
	// closed when the current hook is committed.
	pendingPorts map[PortRange]PortRangeInfo

	// machinePorts contains cached information about all opened port
	// ranges on the caasoperator's assigned machine, mapped to the caasoperator that
	// opened each range and the relevant relation.
	machinePorts map[network.PortRange]params.RelationUnit

	// process is the process of the command that is being run in the local context,
	// like a juju-run command or a hook
	process HookProcess

	// rebootPriority tells us when the hook wants to reboot. If rebootPriority is jujuc.RebootNow
	// the hook will be killed and requeued
	rebootPriority jujuc.RebootPriority

	// hasRunSetStatus is true if a call to the status-set was made during the
	// invocation of a hook.
	// This attribute is persisted to local caasoperator state at the end of the hook
	// execution so that the caasoperator can ultimately decide if it needs to update
	// a charm's workload status, or if the charm has already taken care of it.
	hasRunStatusSet bool

	// clock is used for any time operations.
	clock clock.Clock

	componentDir   func(string) string
	componentFuncs map[string]ComponentFunc
}

// Component implements jujuc.Context.
func (ctx *HookContext) Component(name string) (jujuc.ContextComponent, error) {
	compCtxFunc, ok := ctx.componentFuncs[name]
	if !ok {
		return nil, errors.NotFoundf("context component %q", name)
	}

	facade := ctx.state.Facade()
	config := ComponentConfig{
		AppName:   ctx.app.Name(),
		DataDir:   ctx.componentDir(name),
		APICaller: facade.RawAPICaller(),
	}
	compCtx, err := compCtxFunc(config)
	if err != nil {
		return nil, errors.Trace(err)
	}
	return compCtx, nil
}

func (ctx *HookContext) RequestReboot(priority jujuc.RebootPriority) error {
	// Must set reboot priority first, because killing the hook
	// process will trigger the completion of the hook. If killing
	// the hook fails, then we can reset the priority.
	ctx.SetRebootPriority(priority)

	var err error
	if priority == jujuc.RebootNow {
		// At this point, the hook should be running
		err = ctx.killCharmHook()
	}

	switch err {
	case nil, ErrNoProcess:
		// ErrNoProcess almost certainly means we are running in debug hooks
	default:
		ctx.SetRebootPriority(jujuc.RebootSkip)
	}
	return err
}

func (ctx *HookContext) GetRebootPriority() jujuc.RebootPriority {
	mutex.Lock()
	defer mutex.Unlock()
	return ctx.rebootPriority
}

func (ctx *HookContext) SetRebootPriority(priority jujuc.RebootPriority) {
	mutex.Lock()
	defer mutex.Unlock()
	ctx.rebootPriority = priority
}

func (ctx *HookContext) GetProcess() HookProcess {
	mutex.Lock()
	defer mutex.Unlock()
	return ctx.process
}

func (ctx *HookContext) SetProcess(process HookProcess) {
	mutex.Lock()
	defer mutex.Unlock()
	ctx.process = process
}

func (ctx *HookContext) Id() string {
	return ctx.id
}

func (ctx *HookContext) ApplicationName() string {
	return ctx.applicationName
}

// ApplicationStatus returns the status for the application and all the caasunits on
// the service to which this context caasoperator belongs.
func (ctx *HookContext) ApplicationStatus() (jujuc.ApplicationStatusInfo, error) {
	// XXX
	// var err error
	// status, err := service.Status(ctx.app.Name())
	// if err != nil {
	// 	return jujuc.ApplicationStatusInfo{}, errors.Trace(err)
	// }
	// us := make([]jujuc.StatusInfo, len(status.Caasoperators))
	// i := 0
	// for t, s := range status.Caasoperators {
	// 	us[i] = jujuc.StatusInfo{
	// 		Tag:    t,
	// 		Status: string(s.Status),
	// 		Info:   s.Info,
	// 		Data:   s.Data,
	// 	}
	// 	i++
	// }
	// return jujuc.ApplicationStatusInfo{
	// 	Application: jujuc.StatusInfo{
	// 		Tag:    service.Tag().String(),
	// 		Status: string(status.Application.Status),
	// 		Info:   status.Application.Info,
	// 		Data:   status.Application.Data,
	// 	},
	// 	//Caasoperators: us,
	// }, nil
	return jujuc.ApplicationStatusInfo{}, errors.NotImplementedf("method")
}

// SetCaasoperatorStatus will set the given status for this caasoperator.
func (ctx *HookContext) SetCaasUnitStatus(caasUnitStatus jujuc.StatusInfo) error {
	ctx.hasRunStatusSet = true
	logger.Tracef("[CAASUNIT-STATUS] %s: %s", caasUnitStatus.Status, caasUnitStatus.Info)

	// XXX
	// return ctx.caasUnit.SetCAASUnitStatus(
	// 	status.Status(caasUnitStatus.Status),
	// 	caasUnitStatus.Info,
	// 	caasUnitStatus.Data,
	// )
	return nil
}

func (ctx *HookContext) RunContainer(containerInfo jujuc.ContainerInfo) error {

	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	logger.Debugf("deploying image %v", containerInfo)

	containerName := ctx.applicationName + "-" + containerInfo.Name
	spec := &v1.Pod{
		ObjectMeta: v1.ObjectMeta{
			Name: containerName,
			Labels: map[string]string{
				"juju-app-name": ctx.applicationName,
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{{
				Name:            containerName,
				Image:           containerInfo.Image,
				ImagePullPolicy: v1.PullIfNotPresent,
				Args:            []string{}, // MMCC TODO
			}},
		},
	}
	_, err = client.CoreV1().Pods("default").Create(spec) // XXX TODO namespace
	return errors.Trace(err)

}

// SetApplicationStatus will set the given status to the service to which this
// caasoperator's belong.
func (ctx *HookContext) SetApplicationStatus(serviceStatus jujuc.StatusInfo) error {
	logger.Tracef("[APPLICATION-STATUS] %s: %s", serviceStatus.Status, serviceStatus.Info)

	// XXX
	// service, err := ctx.caasUnit.CAASApplication()
	// if err != nil {
	// 	return errors.Trace(err)
	// }
	// return service.SetStatus(
	// 	ctx.caasUnit.Name(),
	// 	status.Status(serviceStatus.Status),
	// 	serviceStatus.Info,
	// 	serviceStatus.Data,
	// )
	return nil
}

func (ctx *HookContext) HasExecutionSetApplicationStatus() bool {
	return ctx.hasRunStatusSet
}

func (ctx *HookContext) ResetExecutionSetApplicationStatus() {
	ctx.hasRunStatusSet = false
}

func (ctx *HookContext) PublicAddress() (string, error) {
	if ctx.publicAddress == "" {
		return "", errors.NotFoundf("public address")
	}
	return ctx.publicAddress, nil
}

func (ctx *HookContext) PrivateAddress() (string, error) {
	if ctx.privateAddress == "" {
		return "", errors.NotFoundf("private address")
	}
	return ctx.privateAddress, nil
}

func (ctx *HookContext) ConfigSettings() (charm.Settings, error) {
	// XXX
	// if ctx.configSettings == nil {
	// 	var err error
	// 	ctx.configSettings, err = ctx.app.ConfigSettings()
	// 	if err != nil {
	// 		return nil, err
	// 	}
	// }
	// result := charm.Settings{}
	// for name, value := range ctx.configSettings {
	// 	result[name] = value
	// }
	// return result, nil
	return nil, errors.NotImplementedf("method")
}

// HookVars returns an os.Environ-style list of strings necessary to run a hook
// such that it can know what environment it's operating in, and can call back
// into context.
func (context *HookContext) HookVars(paths Paths) ([]string, error) {
	vars := context.proxySettings.AsEnvironmentValues()
	vars = append(vars,
		"CHARM_DIR="+paths.GetCharmDir(), // legacy, embarrassing
		"JUJU_CHARM_DIR="+paths.GetCharmDir(),
		"JUJU_CONTEXT_ID="+context.id,
		"JUJU_AGENT_SOCKET="+paths.GetJujucSocket(),
		"JUJU_APPLICATION_NAME="+context.applicationName,
		"JUJU_MODEL_UUID="+context.uuid,
		"JUJU_MODEL_NAME="+context.envName,
		"JUJU_API_ADDRESSES="+strings.Join(context.apiAddrs, " "),
	// "JUJU_METER_STATUS="+context.meterStatus.code,
	// "JUJU_METER_INFO="+context.meterStatus.info,
	)
	// if r, err := context.HookRelation(); err == nil {
	// 	vars = append(vars,
	// 		"JUJU_RELATION="+r.Name(),
	// 		"JUJU_RELATION_ID="+r.FakeId(),
	// 		"JUJU_REMOTE_UNIT="+context.remoteUnitName,
	// 	)
	// } else if !errors.IsNotFound(err) {
	// 	return nil, errors.Trace(err)
	// }
	return append(vars, OSDependentEnvVars(paths)...), nil
}

func (ctx *HookContext) handleReboot(err *error) {
	// XXX
	// logger.Tracef("checking for reboot request")
	// rebootPriority := ctx.GetRebootPriority()
	// switch rebootPriority {
	// case jujuc.RebootSkip:
	// 	return
	// case jujuc.RebootAfterHook:
	// 	// Reboot should happen only after hook has finished.
	// 	if *err != nil {
	// 		return
	// 	}
	// 	*err = ErrReboot
	// case jujuc.RebootNow:
	// 	*err = ErrRequeueAndReboot
	// }
	// err2 := ctx.caasUnit.SetCAASUnitStatus(status.Rebooting, "", nil)
	// if err2 != nil {
	// 	logger.Errorf("updating agent status: %v", err2)
	// }
	// reqErr := ctx.caasUnit.RequestReboot()
	// if reqErr != nil {
	// 	*err = reqErr
	// }
}

// Prepare implements the Context interface.
func (ctx *HookContext) Prepare() error {
	if ctx.actionData != nil {
		err := ctx.state.ActionBegin(ctx.actionData.Tag)
		if err != nil {
			return errors.Trace(err)
		}
	}
	return nil
}

// Flush implements the Context interface.
func (ctx *HookContext) Flush(process string, ctxErr error) (err error) {
	writeChanges := ctxErr == nil

	// In the case of Actions, handle any errors using finalizeAction.
	// if ctx.actionData != nil {
	// 	// If we had an error in err at this point, it's part of the
	// 	// normal behavior of an Action.  Errors which happen during
	// 	// the finalize should be handed back to the caasoperator.  Close
	// 	// over the existing err, clear it, and only return errors
	// 	// which occur during the finalize, e.g. API call errors.
	// 	defer func(ctxErr error) {
	// 		err = ctx.finalizeAction(ctxErr, err)
	// 	}(ctxErr)
	// 	ctxErr = nil
	// } else {
	// TODO(gsamfira): Just for now, reboot will not be supported in actions.
	defer ctx.handleReboot(&err)
	// }

	for id, rctx := range ctx.relations {
		if writeChanges {
			if e := rctx.WriteSettings(); e != nil {
				e = errors.Errorf(
					"could not write settings from %q to relation %d: %v",
					process, id, e,
				)
				logger.Errorf("%v", e)
				if ctxErr == nil {
					ctxErr = e
				}
			}
		}
	}

	// for rangeKey, rangeInfo := range ctx.pendingPorts {
	// 	if writeChanges {
	// 		var e error
	// 		var op string
	// 		if rangeInfo.ShouldOpen {
	// 			e = ctx.caasoperator.OpenPorts(
	// 				rangeKey.Ports.Protocol,
	// 				rangeKey.Ports.FromPort,
	// 				rangeKey.Ports.ToPort,
	// 			)
	// 			op = "open"
	// 		} else {
	// 			e = ctx.caasoperator.ClosePorts(
	// 				rangeKey.Ports.Protocol,
	// 				rangeKey.Ports.FromPort,
	// 				rangeKey.Ports.ToPort,
	// 			)
	// 			op = "close"
	// 		}
	// 		if e != nil {
	// 			e = errors.Annotatef(e, "cannot %s %v", op, rangeKey.Ports)
	// 			logger.Errorf("%v", e)
	// 			if ctxErr == nil {
	// 				ctxErr = e
	// 			}
	// 		}
	// 	}
	// }

	// TODO (tasdomas) 2014 09 03: context finalization needs to modified to apply all
	//                             changes in one api call to minimize the risk
	//                             of partial failures.

	if !writeChanges {
		return ctxErr
	}

	return ctxErr
}

// killCharmHook tries to kill the current running charm hook.
func (ctx *HookContext) killCharmHook() error {
	proc := ctx.GetProcess()
	if proc == nil {
		// nothing to kill
		return ErrNoProcess
	}
	logger.Infof("trying to kill context process %v", proc.Pid())

	tick := ctx.clock.After(0)
	timeout := ctx.clock.After(30 * time.Second)
	for {
		// We repeatedly try to kill the process until we fail; this is
		// because we don't control the *Process, and our clients expect
		// to be able to Wait(); so we can't Wait. We could do better,
		//   but not with a single implementation across all platforms.
		// TODO(gsamfira): come up with a better cross-platform approach.
		select {
		case <-tick:
			err := proc.Kill()
			if err != nil {
				logger.Infof("kill returned: %s", err)
				logger.Infof("assuming already killed")
				return nil
			}
		case <-timeout:
			return errors.Errorf("failed to kill context process %v", proc.Pid())
		}
		logger.Infof("waiting for context process %v to die", proc.Pid())
		tick = ctx.clock.After(100 * time.Millisecond)
	}
}

// NetworkConfig returns the network config for the given bindingName.
func (ctx *HookContext) NetworkConfig(bindingName string) ([]params.NetworkConfig, error) {
	return []params.NetworkConfig{}, nil
	//return ctx.caasoperator.NetworkConfig(bindingName)
}

// CaasoperatorWorkloadVersion returns the version of the workload reported by
// the current caasoperator.
func (ctx *HookContext) ApplicationWorkloadVersion() (string, error) {
	// XXX
	// var results params.StringResults
	// args := params.Entities{
	// 	Entities: []params.Entity{{Tag: ctx.caasUnit.Tag().String()}},
	// }
	// err := ctx.state.Facade().FacadeCall("WorkloadVersion", args, &results)
	// if err != nil {
	// 	return "", err
	// }
	// if len(results.Results) != 1 {
	// 	return "", fmt.Errorf("expected 1 result, got %d", len(results.Results))
	// }
	// result := results.Results[0]
	// if result.Error != nil {
	// 	return "", result.Error
	// }
	// return result.Result, nil
	return "", errors.NotImplementedf("method")
}

// SetCaasoperatorWorkloadVersion sets the current caasoperator's workload version to
// the specified value.
func (ctx *HookContext) SetApplicationWorkloadVersion(version string) error {
	// XXX
	// var result params.ErrorResults
	// args := params.EntityWorkloadVersions{
	// 	Entities: []params.EntityWorkloadVersion{
	// 		{Tag: ctx.caasUnit.Tag().String(), WorkloadVersion: version},
	// 	},
	// }
	// err := ctx.state.Facade().FacadeCall("SetWorkloadVersion", args, &result)
	// if err != nil {
	// 	return err
	// }
	// return result.OneError()
	return errors.NotImplementedf("method")
}
