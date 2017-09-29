// Copyright 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package params

type CAASProvisioningConfig struct {
	Endpoint       string   `json:"endpoint"`
	CACertificates []string `json:"ca-certificates,omitempty"`
	CertData       []byte   `json:"cert-data"`
	KeyData        []byte   `json:"key-data"`
	Username       string   `json:"username"`
	Password       string   `json:"password"`
}
