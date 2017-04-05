// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package apiserver

import (
	"github.com/juju/errors"
	"gopkg.in/juju/names.v2"

	"github.com/juju/juju/apiserver/common"
	"github.com/juju/juju/state"
)

// isMachineWithJob returns whether the given entity is a machine that
// is configured to run the given job.
func isMachineWithJob(e state.Entity, j state.MachineJob) bool {
	m, ok := e.(*state.Machine)
	if !ok {
		return false
	}
	for _, mj := range m.Jobs() {
		if mj == j {
			return true
		}
	}
	return false
}

type validateArgs struct {
	statePool *state.StatePool
	modelUUID string
	// strict validation does not allow empty UUID values
	strict bool
	// controllerModelOnly only validates the controller model
	controllerModelOnly bool
}

// validateModelUUID is the common validator for the various
// apiserver components that need to check for a valid model
// UUID.  An empty modelUUID means that the connection has come in at
// the root of the URL space and refers to the controller
// model.
//
// It returns the validated model UUID.
func validateModelUUID(args validateArgs) (string, bool, error) {
	ssState := args.statePool.SystemState()
	if args.modelUUID == "" {
		// We allow the modelUUID to be empty so that:
		//    TODO: server a limited API at the root (empty modelUUID)
		//    just the user manager and model manager are able to accept
		//    requests that don't require a modelUUID, like add-model.
		if args.strict {
			return "", false, errors.Trace(common.UnknownModelError(args.modelUUID))
		}
		return ssState.ModelUUID(), false, nil
	}
	if args.modelUUID == ssState.ModelUUID() {
		return args.modelUUID, false, nil
	}
	if args.controllerModelOnly {
		return "", false, errors.Unauthorizedf("requested model %q is not the controller model", args.modelUUID)
	}
	if !names.IsValidModel(args.modelUUID) {
		return "", false, errors.Trace(common.UnknownModelError(args.modelUUID))
	}

	if caasExists, err := ssState.IsCAASModel(args.modelUUID); err != nil {
		return "", false, errors.Wrap(err, common.UnknownModelError(args.modelUUID))
	} else if caasExists {
		return args.modelUUID, true, nil
	}

	modelTag := names.NewModelTag(args.modelUUID)
	if _, err := ssState.GetModel(modelTag); err != nil {
		return "", false, errors.Wrap(err, common.UnknownModelError(args.modelUUID))
	}
	return args.modelUUID, false, nil
}
