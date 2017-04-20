// Copyright 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package jujuc

import (
	"github.com/juju/cmd"
	"github.com/juju/errors"
	"github.com/juju/gnuflag"
)

// RunContainerCommand implements the run-container command.
type RunContainerCommand struct {
	cmd.CommandBase
	ctx         Context
	args        string
	environment string
	image       string
}

// NewRunContainerCommand makes a jujuc run-container command.
func NewRunContainerCommand(ctx Context) (cmd.Command, error) {
	return &RunContainerCommand{ctx: ctx}, nil
}

func (c *RunContainerCommand) Info() *cmd.Info {
	doc := `
Sets the workload status of the charm. Message is optional.
The "last updated" attribute of the status is set, even if the
status and message are the same as what's already set.
`
	return &cmd.Info{
		Name:    "run-container",
		Args:    "<args> <env> <image>",
		Purpose: "tell container framework to start one container",
		Doc:     doc,
	}
}

func (c *RunContainerCommand) SetFlags(f *gnuflag.FlagSet) {
}

func (c *RunContainerCommand) Init(args []string) error {
	if len(args) < 3 {
		return errors.Errorf("invalid args, require <args> <env> <image>")
	}
	c.args = args[0]
	c.environment = args[1]
	c.image = args[2]
	return nil
}

type ContainerInfo struct {
	Args        string
	Environment string
	Image       string
}

func (c *RunContainerCommand) Run(ctx *cmd.Context) error {
	containerInfo := ContainerInfo{
		Args:        c.args,
		Environment: c.environment,
		Image:       c.image,
	}
	return c.ctx.RunContainer(containerInfo)
}
