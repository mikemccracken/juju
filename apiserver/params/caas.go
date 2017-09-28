// Copyright 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package params

type CAASApplicationsDeploy struct {
	Applications []CAASApplicationDeploy `json:"applications"`
}

type CAASApplicationDeploy struct {
	ApplicationName string `json:"application"`
	CharmURL        string `json:"charm-url"`
	Channel         string `json:"channel"`
	ConfigYAML      string `json:"config-yaml"` // Takes precedence over config if both are present.
	NumUnits        int    `json:"num-units"`
}

type AllCAASUnitsResults struct {
	Results []CAASUnits
}

type CAASUnits struct {
	Error *Error     `json:"error,omitempty"`
	Units []CAASUnit `json:"units"`
}

type CAASUnit struct {
	Tag  string `json:"tag"`
	Life Life   `json:"life"`
}

type CAASProvisioningConfig struct {
	Endpoint string `json:"endpoint"`
	CAData   []byte `json:"ca-data"`
	CertData []byte `json:"cert-data"`
	KeyData  []byte `json:"key-data"`
	Username string `json:"username"`
	Password string `json:"password"`
}
