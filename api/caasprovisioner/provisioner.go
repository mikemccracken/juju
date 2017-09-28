// Copyright 2013, 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package caasprovisioner

import (
	"gopkg.in/juju/names.v2"

	"github.com/juju/juju/api/base"
	"github.com/juju/juju/api/common"
	apiwatcher "github.com/juju/juju/api/watcher"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/network"
	"github.com/juju/juju/watcher"
)

type Config struct {
	Endpoint string
	CAData   []byte
	CertData []byte
	KeyData  []byte
	Username string
	Password string
}

type State struct {
	*common.ControllerConfigAPI

	facade base.FacadeCaller
}

func NewState(caller base.APICaller) *State {
	facadeCaller := base.NewFacadeCaller(caller, "CAASProvisioner")
	return &State{
		ControllerConfigAPI: common.NewControllerConfig(facadeCaller),

		facade: facadeCaller,
	}
}

func (st *State) APIHostPorts() ([][]network.HostPort, error) {
	var result params.APIHostPortsResult
	if err := st.facade.FacadeCall("APIHostPorts", nil, &result); err != nil {
		return nil, err
	}
	return result.NetworkHostsPorts(), nil
}

func (st *State) ControllerTag() (names.ControllerTag, error) {
	var result params.StringResult
	if err := st.facade.FacadeCall("ControllerTag", nil, &result); err != nil {
		return names.NewControllerTag(""), err
	}
	if err := result.Error; err != nil {
		return names.NewControllerTag(""), err
	}
	return names.ParseControllerTag(result.Result)
}

func (st *State) ModelTag() (names.ModelTag, error) {
	var result params.StringResult
	if err := st.facade.FacadeCall("ModelTag", nil, &result); err != nil {
		return names.NewModelTag(""), err
	}
	if err := result.Error; err != nil {
		return names.NewModelTag(""), err
	}
	return names.ParseModelTag(result.Result)
}

func (st *State) ProvisioningConfig() (*Config, error) {
	var result params.CAASProvisioningConfig
	if err := st.facade.FacadeCall("ProvisioningConfig", nil, &result); err != nil {
		return nil, err
	}
	return &Config{
		Endpoint: result.Endpoint,
		CAData:   result.CAData,
		CertData: result.CertData,
		KeyData:  result.KeyData,
		Username: result.Username,
		Password: result.Password,
	}, nil
}

// WatchApplications returns a StringsWatcher that notifies of
// changes to the lifecycles of applications in the current model.
func (st *State) WatchApplications() (watcher.StringsWatcher, error) {
	var result params.StringsWatchResult
	if err := st.facade.FacadeCall("WatchApplications", nil, &result); err != nil {
		return nil, err
	}
	if err := result.Error; err != nil {
		return nil, result.Error
	}
	w := apiwatcher.NewStringsWatcher(st.facade.RawAPICaller(), result)
	return w, nil
}
