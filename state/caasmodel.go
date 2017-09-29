// Copyright 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package state

import (
	"github.com/juju/errors"
	"github.com/juju/juju/apiserver/params"
)

// CAASModel contains functionality that is specific to an
// Containers-As-A-Service (CAAS) model. It embeds a Model so that
// all generic Model functionality is also available.
type CAASModel struct {
	*Model

	mb modelBackend

	// TODO(caas): This should be removed once things
	// have been sufficiently untangled.
	st *State
}

// CAASModel returns an Containers-As-A-Service (CAAS) model.
func (m *Model) CAASModel() (*CAASModel, error) {
	if m.Type() != ModelTypeCAAS {
		return nil, errors.NotSupportedf("called CAASModel() on a non-CAAS Model")
	}
	return &CAASModel{
		Model: m,
		mb:    m.st,
		st:    m.st,
	}, nil
}

// CAASModel returns Containers-As-A-Service (CAAS) model.
//
// TODO(caas): This is a convenience helper only and will go away
// once most model related functionality has been moved from State to
// Model/CAASModel. Model.CAASModel() should be preferred where-ever
// possible.
func (st *State) CAASModel() (*CAASModel, error) {
	m, err := st.Model()
	if err != nil {
		return nil, errors.Trace(err)
	}
	im, err := m.CAASModel()
	if err != nil {
		return nil, errors.Trace(err)
	}
	return im, nil
}

func (m *CAASModel) ProvisioningConfig() (params.CAASProvisioningConfig, error) {
	cloud, err := m.st.Cloud(m.Cloud())
	if err != nil {
		return params.CAASProvisioningConfig{}, errors.Trace(err)
	}

	credentialTag, is_set := m.CloudCredential()
	if !is_set {
		return params.CAASProvisioningConfig{}, errors.Errorf("CAAS cloud with no CloudCredential set")
	}

	credential, err := m.st.CloudCredential(credentialTag)
	if err != nil {
		return params.CAASProvisioningConfig{}, errors.Trace(err)
	}

	credentialAttrs := credential.Attributes()

	return params.CAASProvisioningConfig{
		Endpoint:       cloud.Endpoint, // TODO(caas) fix this if region support is added
		CACertificates: cloud.CACertificates,
		CertData:       []byte(credentialAttrs["ClientCertificateData"]),
		KeyData:        []byte(credentialAttrs["ClientKeyData"]),
		Username:       credentialAttrs["Username"],
		Password:       credentialAttrs["Password"],
	}, nil
}
