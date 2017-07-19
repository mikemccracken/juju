// Copyright 2012, 2013, 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package caasprovisioner

import (
	"github.com/juju/errors"
	"github.com/juju/juju/apiserver/common"
	"github.com/juju/juju/apiserver/facade"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/state"
	"github.com/juju/juju/state/watcher"
	"github.com/juju/loggo"
)

var logger = loggo.GetLogger("juju.apiserver.caasprovisioner")

type API struct {
	*common.ControllerConfigAPI

	auth      facade.Authorizer
	model     *state.CAASModel
	resources facade.Resources
	state     *state.CAASState
}

// NewFacade provides the signature required for facade registration.
func NewFacade(ctx facade.Context) (*API, error) {
	if !ctx.IsCAAS() {
		return nil, errors.New("not a CAAS state")
	}

	authorizer := ctx.Auth()
	resources := ctx.Resources()
	state := ctx.CAASState()

	model, err := state.CAASModel()
	if err != nil {
		return nil, errors.Trace(err)
	}

	if !authorizer.AuthMachineAgent() && !authorizer.AuthController() {
		return nil, common.ErrPerm
	}

	return &API{
		ControllerConfigAPI: common.NewControllerConfig(state),

		auth:      authorizer,
		model:     model,
		resources: resources,
		state:     state,
	}, nil
}

func (a *API) APIHostPorts() (params.APIHostPortsResult, error) {
	servers, err := a.state.APIHostPorts()
	if err != nil {
		return params.APIHostPortsResult{}, err
	}
	return params.APIHostPortsResult{
		Servers: params.FromNetworkHostsPorts(servers),
	}, nil
}

func (a *API) ControllerTag() (params.StringResult, error) {
	return params.StringResult{Result: a.state.ControllerTag().String()}, nil
}

func (a *API) ModelTag() (params.StringResult, error) {
	return params.StringResult{Result: a.state.ModelTag().String()}, nil
}

// ProvisioningConfig returns the configuration to be used when provisioning
// applications.
func (a *API) ProvisioningConfig() (params.CAASProvisioningConfig, error) {
	return params.CAASProvisioningConfig{
		Endpoint: a.model.Endpoint(),
		CAData:   a.model.CAData(),
		CertData: a.model.CertData(),
		KeyData:  a.model.KeyData(),
		Username: a.model.Username(),
		Password: a.model.Password(),
	}, nil
}

// ModelUUID returns the model UUID to connect to the environment
// that the current connection is for.
func (a *API) ModelUUID() (params.StringResult, error) {
	return params.StringResult{Result: a.state.ModelUUID()}, nil
}

// WatchApplications starts a StringsWatcher to watch applications deployed to
// this model.
func (a *API) WatchApplications(args params.WatchApplications) (params.StringsWatchResult, error) {
	watch := a.state.WatchApplications()
	// Consume the initial event and forward it to the result.
	if changes, ok := <-watch.Changes(); ok {
		return params.StringsWatchResult{
			StringsWatcherId: a.resources.Register(watch),
			Changes:          changes,
		}, nil
	}
	return params.StringsWatchResult{}, watcher.EnsureErr(watch)
}
