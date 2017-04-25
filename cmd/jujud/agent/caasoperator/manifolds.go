// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package caasoperator

import (
	"github.com/juju/utils/clock"
	"github.com/juju/utils/voyeur"
	"github.com/prometheus/client_golang/prometheus"

	coreagent "github.com/juju/juju/agent"
	"github.com/juju/juju/api"
	//	msapi "github.com/juju/juju/api/meterstatus"
	//	"github.com/juju/juju/utils/proxy"

	"github.com/juju/juju/worker/agent"
	"github.com/juju/juju/worker/apicaller"
	"github.com/juju/juju/worker/dependency"
	"github.com/juju/juju/worker/fortress"

	"github.com/juju/juju/worker/logsender"
	//	"github.com/juju/juju/worker/meterstatus"
	//	"github.com/juju/juju/worker/metrics/collect"
	//	"github.com/juju/juju/worker/metrics/sender"
	//	"github.com/juju/juju/worker/metrics/spool"
	//	"github.com/juju/juju/worker/proxyupdater"
	//	"github.com/juju/juju/worker/retrystrategy"
	"github.com/juju/juju/worker/caasoperator"
	//	"github.com/juju/juju/worker/upgrader"
)

// ManifoldsConfig allows specialisation of the result of Manifolds.
type ManifoldsConfig struct {

	// Agent contains the agent that will be wrapped and made available to
	// its dependencies via a dependency.Engine.
	Agent coreagent.Agent

	// LogSource will be read from by the logsender component.
	LogSource logsender.LogRecordCh

	// AgentConfigChanged is set whenever the unit agent's config
	// is updated.
	AgentConfigChanged *voyeur.Value

	// PrometheusRegisterer is a prometheus.Registerer that may be used
	// by workers to register Prometheus metric collectors.
	PrometheusRegisterer prometheus.Registerer
}

// Manifolds returns a set of co-configured manifolds covering the various
// responsibilities of a caasoperator agent. It also accepts the logSource
// argument because we haven't figured out how to thread all the logging bits
// through a dependency engine yet.
//
// Thou Shalt Not Use String Literals In This Function. Or Else.
func Manifolds(config ManifoldsConfig) dependency.Manifolds {

	return dependency.Manifolds{

		// The agent manifold references the enclosing agent, and is the
		// foundation stone on which most other manifolds ultimately depend.
		// (Currently, that is "all manifolds", but consider a shared clock.)
		agentName: agent.Manifold(config.Agent),

		// The api caller is a thin concurrent wrapper around a connection
		// to some API server. It's used by many other manifolds, which all
		// select their own desired facades. It will be interesting to see
		// how this works when we consolidate the agents; might be best to
		// handle the auth changes server-side..?
		apiCallerName: apicaller.Manifold(apicaller.ManifoldConfig{
			AgentName:     agentName,
			APIOpen:       api.Open,
			NewConnection: apicaller.OnlyConnect,
		}),

		// The log sender is a leaf worker that sends log messages to some
		// API server, when configured so to do. We should only need one of
		// these in a consolidated agent.
		// logSenderName: logsender.Manifold(logsender.ManifoldConfig{
		// 	APICallerName: apiCallerName,
		// 	LogSource:     config.LogSource,
		// }),

		// The upgrader is a leaf worker that returns a specific error type
		// recognised by the operator agent, causing other workers to be stopped
		// and the agent to be restarted running the new tools. We should only
		// need one of these in a consolidated agent, but we'll need to be
		// careful about behavioural differences, and interactions with the
		// upgradesteps worker.
		// upgraderName: upgrader.Manifold(upgrader.ManifoldConfig{
		// 	AgentName:     agentName,
		// 	APICallerName: apiCallerName,
		// }),

		// The logging config updater is a leaf worker that indirectly
		// controls the messages sent via the log sender according to
		// changes in environment config. We should only need one of
		// these in a consolidated agent.
		// loggingConfigUpdaterName: logger.Manifold(logger.ManifoldConfig{
		// 	AgentName:     agentName,
		// 	APICallerName: apiCallerName,
		// }),

		// The api address updater is a leaf worker that rewrites agent config
		// as the controller addresses change. We should only need one of
		// these in a consolidated agent.
		// apiAddressUpdaterName: apiaddressupdater.Manifold(apiaddressupdater.ManifoldConfig{
		// 	AgentName:     agentName,
		// 	APICallerName: apiCallerName,
		// }),

		// The proxy config updater is a leaf worker that sets http/https/apt/etc
		// proxy settings.
		// TODO(fwereade): timing of this is suspicious. There was superstitious
		// code trying to run this early; if that ever helped, it was only by
		// coincidence. Probably we ought to be making components that might
		// need proxy config into explicit dependencies of the proxy updater...
		// proxyConfigUpdaterName: proxyupdater.Manifold(proxyupdater.ManifoldConfig{
		// 	AgentName:       agentName,
		// 	APICallerName:   apiCallerName,
		// 	WorkerFunc:      proxyupdater.NewWorker,
		// 	InProcessUpdate: proxy.DefaultConfig.Set,
		// })),

		// The charmdir resource coordinates whether the charm directory is
		// available or not; after 'start' hook and before 'stop' hook
		// executes, and not during upgrades.
		charmDirName: fortress.Manifold(),

		// HookRetryStrategy uses a retrystrategy worker to get a
		// retry strategy that will be used by the caasoperator to run its hooks.
		// hookRetryStrategyName: retrystrategy.Manifold(retrystrategy.ManifoldConfig{
		// 	AgentName:     agentName,
		// 	APICallerName: apiCallerName,
		// 	NewFacade:     retrystrategy.NewFacade,
		// 	NewWorker:     retrystrategy.NewRetryStrategyWorker,
		// })),

		// The operator installs and deploys charm containers;
		// manages the unit's presence in its relations;
		// creates suboordinate units; runs all the hooks;
		// sends metrics; etc etc etc.

		operatorName: caasoperator.Manifold(caasoperator.ManifoldConfig{
			AgentName:             agentName,
			APICallerName:         apiCallerName,
			MachineLockName:       coreagent.MachineLockName,
			Clock:                 clock.WallClock,
			CharmDirName:          charmDirName,
			HookRetryStrategyName: hookRetryStrategyName,
		}),

		// // TODO (mattyw) should be added to machine agent.
		// metricSpoolName: spool.Manifold(spool.ManifoldConfig{
		// 	AgentName: agentName,
		// })),

		// The metric collect worker executes the collect-metrics hook in a
		// restricted context that can safely run concurrently with other hooks.
		// metricCollectName: collect.Manifold(collect.ManifoldConfig{
		// 	AgentName:       agentName,
		// 	MetricSpoolName: metricSpoolName,
		// 	CharmDirName:    charmDirName,
		// })),

		// // The meter status worker executes the meter-status-changed hook when it detects
		// // that the meter status has changed.
		// meterStatusName: meterstatus.Manifold(meterstatus.ManifoldConfig{
		// 	AgentName:                agentName,
		// 	APICallerName:            apiCallerName,
		// 	MachineLockName:          coreagent.MachineLockName,
		// 	Clock:                    clock.WallClock,
		// 	NewHookRunner:            meterstatus.NewHookRunner,
		// 	NewMeterStatusAPIClient:  msapi.NewClient,
		// 	NewConnectedStatusWorker: meterstatus.NewConnectedStatusWorker,
		// 	NewIsolatedStatusWorker:  meterstatus.NewIsolatedStatusWorker,
		// })),

		// // The metric sender worker periodically sends accumulated metrics to the controller.
		// metricSenderName: sender.Manifold(sender.ManifoldConfig{
		// 	AgentName:       agentName,
		// 	APICallerName:   apiCallerName,
		// 	MetricSpoolName: metricSpoolName,
		// })),
	}
}

const (
	agentName     = "agent"
	apiCallerName = "api-caller"
	logSenderName = "log-sender"
	upgraderName  = "upgrader"

	loggingConfigUpdaterName = "logging-config-updater"
	proxyConfigUpdaterName   = "proxy-config-updater"
	apiAddressUpdaterName    = "api-address-updater"

	charmDirName          = "charm-dir"
	hookRetryStrategyName = "hook-retry-strategy"
	operatorName          = "operator"

	metricSpoolName   = "metric-spool"
	meterStatusName   = "meter-status"
	metricCollectName = "metric-collect"
	metricSenderName  = "metric-sender"
)
