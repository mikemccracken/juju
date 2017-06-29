// Copyright 2012-2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package caasoperator

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/juju/errors"
	"github.com/juju/loggo"
	"github.com/juju/mutex"
	"github.com/juju/utils"
	"github.com/juju/utils/clock"
	"github.com/juju/utils/exec"
	corecharm "gopkg.in/juju/charm.v6-unstable"
	"gopkg.in/juju/names.v2"
	worker "gopkg.in/juju/worker.v1"

	"github.com/juju/juju/api/caasoperator"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/status"
	jworker "github.com/juju/juju/worker"
	"github.com/juju/juju/worker/caasoperator/actions"
	"github.com/juju/juju/worker/caasoperator/charm"
	"github.com/juju/juju/worker/caasoperator/hook"
	"github.com/juju/juju/worker/caasoperator/operation"
	"github.com/juju/juju/worker/caasoperator/relation"
	"github.com/juju/juju/worker/caasoperator/remotestate"
	"github.com/juju/juju/worker/caasoperator/resolver"
	"github.com/juju/juju/worker/caasoperator/runcommands"
	"github.com/juju/juju/worker/caasoperator/runner"
	"github.com/juju/juju/worker/caasoperator/runner/context"
	"github.com/juju/juju/worker/caasoperator/runner/jujuc"
	"github.com/juju/juju/worker/catacomb"
	"github.com/juju/juju/worker/fortress"
)

var logger = loggo.GetLogger("juju.worker.caasoperator")

// A CaasOperatorExecutionObserver gets the appropriate methods called when a hook
// is executed and either succeeds or fails.  Missing hooks don't get reported
// in this way.
type CaasOperatorExecutionObserver interface {
	HookCompleted(hookName string)
	HookFailed(hookName string)
}

// CaasOperator implements the capabilities of the caasoperator agent. It is not intended to
// implement the actual *behaviour* of the caasoperator agent; that responsibility is
// delegated to Mode values, which are expected to react to events and direct
// the caasoperator's responses to them.
type CaasOperator struct {
	catacomb        catacomb.Catacomb
	st              *caasoperator.State
	paths           Paths
	caasapplication *caasoperator.CAASApplication
	caasunit        *caasoperator.CAASUnit
	relations       relation.Relations
	clock           clock.Clock

	// Cache the last reported status information
	// so we don't make unnecessary api calls.
	setStatusMutex      sync.Mutex
	lastReportedStatus  status.Status
	lastReportedMessage string

	operationFactory     operation.Factory
	operationExecutor    operation.Executor
	newOperationExecutor NewExecutorFunc
	translateResolverErr func(error) error

	charmDirGuard fortress.Guard

	hookLockName string

	// TODO(axw) move the runListener and run-command code outside of the
	// caasoperator, and introduce a separate worker. Each worker would feed
	// operations to a single, synchronized runner to execute.
	runListener    *RunListener
	commands       runcommands.Commands
	commandChannel chan string

	// The execution observer is only used in tests at this stage. Should this
	// need to be extended, perhaps a list of observers would be needed.
	observer CaasOperatorExecutionObserver

	// updateStatusAt defines a function that will be used to generate signals for
	// the update-status hook
	updateStatusAt func() <-chan time.Time

	// hookRetryStrategy represents configuration for hook retries
	hookRetryStrategy params.RetryStrategy

	// downloader is the downloader that should be used to get the charm
	// archive.
	downloader charm.Downloader
}

// CaasOperatorParams hold all the necessary parameters for a new CaasOperator.
type CaasOperatorParams struct {
	CaasOperatorFacade   *caasoperator.State
	CaasOperatorTag      names.ApplicationTag
	DataDir              string
	Downloader           charm.Downloader
	MachineLockName      string
	CharmDirGuard        fortress.Guard
	UpdateStatusSignal   func() <-chan time.Time
	HookRetryStrategy    params.RetryStrategy
	NewOperationExecutor NewExecutorFunc
	TranslateResolverErr func(error) error
	Clock                clock.Clock
	// TODO (mattyw, wallyworld, fwereade) Having the observer here make this approach a bit more legitimate, but it isn't.
	// the observer is only a stop gap to be used in tests. A better approach would be to have the caasoperator tests start hooks
	// that write to files, and have the tests watch the output to know that hooks have finished.
	Observer CaasOperatorExecutionObserver
}

type NewExecutorFunc func(string, func() (*corecharm.URL, error), func() (mutex.Releaser, error)) (operation.Executor, error)

// NewCaasOperator creates a new CaasOperator which will install, run, and upgrade
// a charm on behalf of the unit with the given unitTag, by executing
// hooks and operations provoked by changes in st.
func NewCaasOperator(caasoperatorParams *CaasOperatorParams) (*CaasOperator, error) {
	translateResolverErr := caasoperatorParams.TranslateResolverErr
	if translateResolverErr == nil {
		translateResolverErr = func(err error) error { return err }
	}

	op := &CaasOperator{
		st:                   caasoperatorParams.CaasOperatorFacade,
		paths:                NewPaths(caasoperatorParams.DataDir, caasoperatorParams.CaasOperatorTag),
		hookLockName:         caasoperatorParams.MachineLockName,
		charmDirGuard:        caasoperatorParams.CharmDirGuard,
		updateStatusAt:       caasoperatorParams.UpdateStatusSignal,
		hookRetryStrategy:    caasoperatorParams.HookRetryStrategy,
		newOperationExecutor: caasoperatorParams.NewOperationExecutor,
		translateResolverErr: translateResolverErr,
		observer:             caasoperatorParams.Observer,
		clock:                caasoperatorParams.Clock,
		downloader:           caasoperatorParams.Downloader,
	}
	err := catacomb.Invoke(catacomb.Plan{
		Site: &op.catacomb,
		Work: func() error {
			return op.loop(caasoperatorParams.CaasOperatorTag)
		},
	})
	return op, errors.Trace(err)
}

func (op *CaasOperator) loop(applicationTag names.ApplicationTag) (err error) {
	if err := op.init(applicationTag); err != nil {
		if err == jworker.ErrTerminateAgent {
			return err
		}
		return errors.Annotatef(err, "failed to initialize caasoperator for %q", applicationTag)
	}

	// Install is a special case, as it must run before there
	// is any remote state, and before the remote state watcher
	// is started.
	var charmURL *corecharm.URL
	var charmModifiedVersion int
	opState := op.operationExecutor.State()
	if opState.Kind == operation.Install {
		logger.Infof("resuming charm install")
		operation, err := op.operationFactory.NewInstall(opState.CharmURL)
		if err != nil {
			return errors.Trace(err)
		}
		if err := op.operationExecutor.Run(operation); err != nil {
			return errors.Trace(err)
		}
		charmURL = opState.CharmURL
	} else {
		charmURL, _, err = op.caasapplication.CharmURL()
		if err != nil {
			return errors.Trace(err)
		}
		charmModifiedVersion, err = op.caasapplication.CharmModifiedVersion()
		if err != nil {
			return errors.Trace(err)
		}
	}

	var (
		watcher   *remotestate.RemoteStateWatcher
		watcherMu sync.Mutex
	)

	logger.Infof("hooks are retried %v", op.hookRetryStrategy.ShouldRetry)
	retryHookChan := make(chan struct{}, 1)
	// TODO(katco): 2016-08-09: This type is deprecated: lp:1611427
	retryHookTimer := utils.NewBackoffTimer(utils.BackoffTimerConfig{
		Min:    op.hookRetryStrategy.MinRetryTime,
		Max:    op.hookRetryStrategy.MaxRetryTime,
		Jitter: op.hookRetryStrategy.JitterRetryTime,
		Factor: op.hookRetryStrategy.RetryTimeFactor,
		Func: func() {
			// Don't try to send on the channel if it's already full
			// This can happen if the timer fires off before the event is consumed
			// by the resolver loop
			select {
			case retryHookChan <- struct{}{}:
			default:
			}
		},
		Clock: op.clock,
	})

	defer func() {
		// Whenever we exit the caasoperator we want to stop a potentially
		// running timer so it doesn't trigger for nothing.
		retryHookTimer.Reset()
	}()

	restartWatcher := func() error {
		watcherMu.Lock()
		defer watcherMu.Unlock()

		if watcher != nil {
			// watcher added to catacomb, will kill caasoperator if there's an error.
			worker.Stop(watcher)
		}
		var err error
		watcher, err = remotestate.NewWatcher(
			remotestate.WatcherConfig{
				State:               remotestate.NewAPIState(op.st),
				ApplicationTag:      applicationTag,
				UpdateStatusChannel: op.updateStatusAt,
				CommandChannel:      op.commandChannel,
				RetryHookChannel:    retryHookChan,
			})
		if err != nil {
			return errors.Trace(err)
		}
		if err := op.catacomb.Add(watcher); err != nil {
			return errors.Trace(err)
		}
		return nil
	}

	onIdle := func() error {
		opState := op.operationExecutor.State()
		if opState.Kind != operation.Continue {
			// We should only set idle status if we're in
			// the "Continue" state, which indicates that
			// there is nothing to do and we're not in an
			// error state.
			return nil
		}
		return nil // setAgentStatus(op, status.Idle, "", nil)
	}

	clearResolved := func() error {
		if err := op.caasunit.ClearResolved(); err != nil {
			return errors.Trace(err)
		}

		watcher.ClearResolvedMode()
		return nil
	}

	for {
		if err = restartWatcher(); err != nil {
			err = errors.Annotate(err, "(re)starting watcher")
			break
		}
		caasoperatorResolver := NewCaasOperatorResolver(ResolverConfig{
			ClearResolved:       clearResolved,
			ReportHookError:     op.reportHookError,
			ShouldRetryHooks:    op.hookRetryStrategy.ShouldRetry,
			StartRetryHookTimer: retryHookTimer.Start,
			StopRetryHookTimer:  retryHookTimer.Reset,
			Actions:             actions.NewResolver(),
			Relations:           relation.NewRelationsResolver(op.relations),
			Commands: runcommands.NewCommandsResolver(
				op.commands, watcher.CommandCompleted,
			),
		})

		// We should not do anything until there has been a change
		// to the remote state. The watcher will trigger at least
		// once initially.
		select {
		case <-op.catacomb.Dying():
			return op.catacomb.ErrDying()
		case <-watcher.RemoteStateChanged():
		}

		localState := resolver.LocalState{
			CharmURL:             charmURL,
			CharmModifiedVersion: charmModifiedVersion,
		}
		for err == nil {
			err = resolver.Loop(resolver.LoopConfig{
				Resolver:      caasoperatorResolver,
				Watcher:       watcher,
				Executor:      op.operationExecutor,
				Factory:       op.operationFactory,
				Abort:         op.catacomb.Dying(),
				OnIdle:        onIdle,
				CharmDirGuard: op.charmDirGuard,
			}, &localState)

			err = op.translateResolverErr(err)

			switch cause := errors.Cause(err); cause {
			case nil:
				// Loop back around.
			case resolver.ErrLoopAborted:
				err = op.catacomb.ErrDying()
			case operation.ErrNeedsReboot:
				err = jworker.ErrRebootMachine
			case operation.ErrHookFailed:
				// Loop back around. The resolver can tell that it is in
				// an error state by inspecting the operation state.
				err = nil
			case resolver.ErrTerminate:
				err = op.terminate()
			case resolver.ErrRestart:
				// make sure we update the two values used above in
				// creating LocalState.
				charmURL = localState.CharmURL
				charmModifiedVersion = localState.CharmModifiedVersion
				// leave err assigned, causing loop to break
			default:
				// We need to set conflicted from here, because error
				// handling is outside of the resolver's control.
				if operation.IsDeployConflictError(cause) {
					localState.Conflicted = true
					err = nil // setAgentStatus(op, status.Error, "upgrade failed", nil)
				} else {
					// reportAgentError(op, "resolver loop error", err)
				}
			}
		}

		if errors.Cause(err) != resolver.ErrRestart {
			break
		}
	}

	logger.Infof("caasoperator %p shutting down: %s", op, err)
	return err
}

func (op *CaasOperator) terminate() error {
	applicationWatcher, err := op.caasapplication.Watch()
	if err != nil {
		return errors.Trace(err)
	}
	if err := op.catacomb.Add(applicationWatcher); err != nil {
		return errors.Trace(err)
	}
	for {
		select {
		case <-op.catacomb.Dying():
			return op.catacomb.ErrDying()
		case _, ok := <-applicationWatcher.Changes():
			if !ok {
				return errors.New("caasapplication watcher closed")
			}
			if err := op.caasapplication.Refresh(); err != nil {
				return errors.Trace(err)
			}
			// XXX do this for each unit here or ?
			// if err := op.caasunit.EnsureDead(); err != nil {
			// 	return errors.Trace(err)
			// }
			return jworker.ErrTerminateAgent
		}
	}
}

func (op *CaasOperator) init(caasapplicationtag names.ApplicationTag) (err error) {
	op.caasapplication, err = op.st.CAASApplication(caasapplicationtag)
	if err != nil {
		return err
	}

	// MMCC TEMP: to test relations in the prototype, we hard-code
	// a single unit per application
	fakeCaasUnitTag, err := names.ParseUnitTag("unit-" + caasapplicationtag.Id() + "/0")
	if err != nil {
		return err
	}
	op.caasunit, err = op.st.CAASUnit(fakeCaasUnitTag)
	if err != nil {
		return err
	}

	if op.caasapplication.Life() == params.Dead {
		// If we started up already dead, we should not progress further. If we
		// become Dead immediately after starting up, we may well complete any
		// operations in progress before detecting it; but that race is fundamental
		// and inescapable, whereas this one is not.
		return jworker.ErrTerminateAgent
	}

	if err := jujuc.EnsureSymlinks(op.paths.ToolsDir); err != nil {
		return err
	}
	if err := os.MkdirAll(op.paths.State.RelationsDir, 0755); err != nil {
		return errors.Trace(err)
	}

	relations, err := relation.NewRelations(
		op.st, op.caasunit.Tag(), op.paths.State.CharmDir,
		op.paths.State.RelationsDir, op.catacomb.Dying(),
	)
	if err != nil {
		return errors.Annotatef(err, "cannot create relations for unit %v",
			op.caasunit.Tag())
	}
	op.relations = relations

	op.commands = runcommands.NewCommands()
	op.commandChannel = make(chan string)

	if err := charm.ClearDownloads(op.paths.State.BundlesDir); err != nil {
		logger.Warningf(err.Error())
	}

	deployer, err := charm.NewDeployer(
		op.paths.State.CharmDir,
		op.paths.State.DeployerDir,
		charm.NewBundlesDir(op.paths.State.BundlesDir, op.downloader),
	)
	if err != nil {
		return errors.Annotatef(err, "cannot create deployer")
	}

	contextFactory, err := context.NewContextFactory(
		op.st, caasapplicationtag,
		op.relations.GetInfo,
		op.paths, op.clock,
	)
	if err != nil {
		return errors.Annotate(err, "creating context factory")
	}
	runnerFactory, err := runner.NewFactory(
		op.st, op.paths, contextFactory,
	)
	if err != nil {
		return errors.Annotate(err, "creating runner factory")
	}
	op.operationFactory = operation.NewFactory(operation.FactoryParams{
		Deployer:       deployer,
		RunnerFactory:  runnerFactory,
		Callbacks:      &operationCallbacks{op},
		Abort:          op.catacomb.Dying(),
		MetricSpoolDir: op.paths.GetMetricsSpoolDir(),
	})

	operationExecutor, err := op.newOperationExecutor(
		op.paths.State.OperationsFile,
		op.getServiceCharmURL,
		op.acquireExecutionLock,
	)
	if err != nil {
		return errors.Trace(err)
	}
	op.operationExecutor = operationExecutor

	logger.Debugf("starting juju-run listener on unix:%s", op.paths.Runtime.JujuRunSocket)
	commandRunner, err := NewChannelCommandRunner(ChannelCommandRunnerConfig{
		Abort:          op.catacomb.Dying(),
		Commands:       op.commands,
		CommandChannel: op.commandChannel,
	})
	if err != nil {
		return errors.Annotate(err, "creating command runner")
	}
	op.runListener, err = NewRunListener(RunListenerConfig{
		SocketPath:    op.paths.Runtime.JujuRunSocket,
		CommandRunner: commandRunner,
	})
	if err != nil {
		return errors.Trace(err)
	}
	rlw := newRunListenerWrapper(op.runListener)
	if err := op.catacomb.Add(rlw); err != nil {
		return errors.Trace(err)
	}
	return os.Chmod(op.paths.Runtime.JujuRunSocket, 0777)
}

func (op *CaasOperator) Kill() {
	op.catacomb.Kill(nil)
}

func (op *CaasOperator) Wait() error {
	return op.catacomb.Wait()
}

func (op *CaasOperator) getServiceCharmURL() (*corecharm.URL, error) {
	charmURL, _, err := op.caasapplication.CharmURL()
	return charmURL, err
}

// RunCommands executes the supplied commands in a hook context.
func (op *CaasOperator) RunCommands(args RunCommandsArgs) (results *exec.ExecResponse, err error) {
	// TODO(axw) drop this when we move the run-listener to an independent
	// worker. This exists purely for the tests.
	return op.runListener.RunCommands(args)
}

// acquireExecutionLock acquires the machine-level execution lock, and
// returns a func that must be called to unlock it. It's used by operation.Executor
// when running operations that execute external code.
func (op *CaasOperator) acquireExecutionLock() (mutex.Releaser, error) {
	// We want to make sure we don't block forever when locking, but take the
	// CaasOperator's catacomb into account.
	spec := mutex.Spec{
		Name:   op.hookLockName,
		Clock:  op.clock,
		Delay:  250 * time.Millisecond,
		Cancel: op.catacomb.Dying(),
	}
	logger.Debugf("acquire lock %q for caasoperator hook execution", op.hookLockName)
	releaser, err := mutex.Acquire(spec)
	if err != nil {
		return nil, errors.Trace(err)
	}
	logger.Debugf("lock %q acquired", op.hookLockName)
	return releaser, nil
}

func (op *CaasOperator) reportHookError(hookInfo hook.Info) error {
	// Set the agent status to "error". We must do this here in case the
	// hook is interrupted (e.g. unit agent crashes), rather than immediately
	// after attempting a runHookOp.
	hookName := string(hookInfo.Kind)
	statusData := map[string]interface{}{}
	if hookInfo.Kind.IsRelation() {
		statusData["relation-id"] = hookInfo.RelationId
		if hookInfo.RemoteUnit != "" {
			statusData["remote-unit"] = hookInfo.RemoteUnit
		}
		relationName, err := op.relations.Name(hookInfo.RelationId)
		if err != nil {
			return errors.Trace(err)
		}
		hookName = fmt.Sprintf("%s-%s", relationName, hookInfo.Kind)
	}
	statusData["hook"] = hookName
	//statusMessage := fmt.Sprintf("hook failed: %q", hookName)
	return nil // setAgentStatus(op, status.Error, statusMessage, statusData)
}
