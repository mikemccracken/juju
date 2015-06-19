// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package process_test

import (
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"

	"github.com/juju/juju/process"
	"github.com/juju/juju/testing"
)

var _ = gc.Suite(&LaunchDetailsSuite{})
var _ = gc.Suite(&pluginSuite{})

type LaunchDetailsSuite struct {
	testing.BaseSuite
}

func (*LaunchDetailsSuite) TestValidateOkay(c *gc.C) {
	details := process.LaunchDetails{
		ID:     "abc123",
		Status: "running",
	}
	err := details.Validate()

	c.Check(err, jc.ErrorIsNil)
}

func (*LaunchDetailsSuite) TestValidateMissingID(c *gc.C) {
	details := process.LaunchDetails{
		Status: "running",
	}
	err := details.Validate()

	c.Check(err, gc.ErrorMatches, "ID must be set")
}

func (*LaunchDetailsSuite) TestValidateMissingStatus(c *gc.C) {
	details := process.LaunchDetails{
		ID: "abc123",
	}
	err := details.Validate()

	c.Check(err, gc.ErrorMatches, "Status must be set")
}

type pluginSuite struct {
	testing.BaseSuite
}

func (*pluginSuite) TestParseEnvOkay(c *gc.C) {
	raw := []string{"A=1", "B=2", "C=3"}
	env, err := process.ParseEnv(raw)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(env, jc.DeepEquals, map[string]string{
		"A": "1",
		"B": "2",
		"C": "3",
	})
}

func (*pluginSuite) TestParseEnvEmpty(c *gc.C) {
	var raw []string
	env, err := process.ParseEnv(raw)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(env, gc.HasLen, 0)
}

func (*pluginSuite) TestParseEnvEssentiallyEmpty(c *gc.C) {
	raw := []string{""}
	env, err := process.ParseEnv(raw)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(env, gc.HasLen, 0)
}

func (*pluginSuite) TestParseEnvSkipped(c *gc.C) {
	raw := []string{"A=1", "B=2", "", "D=4"}
	env, err := process.ParseEnv(raw)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(env, jc.DeepEquals, map[string]string{
		"A": "1",
		"B": "2",
		"D": "4",
	})
}

func (*pluginSuite) TestParseEnvMissing(c *gc.C) {
	raw := []string{"A=1", "B=", "C", "D=4"}
	env, err := process.ParseEnv(raw)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(env, jc.DeepEquals, map[string]string{
		"A": "1",
		"B": "",
		"C": "",
		"D": "4",
	})
}

func (*pluginSuite) TestParseEnvBadName(c *gc.C) {
	raw := []string{"=1"}
	_, err := process.ParseEnv(raw)

	c.Check(err, gc.ErrorMatches, `got "" for env var name`)
}

func (*pluginSuite) TestUnparseEnvOkay(c *gc.C) {
	env := map[string]string{
		"A": "1",
		"B": "2",
		"C": "3",
	}
	raw, err := process.UnparseEnv(env)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(raw, jc.DeepEquals, []string{"A=1", "B=2", "C=3"})
}

func (*pluginSuite) TestUnparseEnvEmpty(c *gc.C) {
	var env map[string]string
	raw, err := process.UnparseEnv(env)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(raw, gc.IsNil)
}

func (*pluginSuite) TestUnparseEnvMissingKey(c *gc.C) {
	env := map[string]string{
		"A": "1",
		"":  "2",
		"C": "3",
	}
	_, err := process.UnparseEnv(env)

	c.Check(err, gc.ErrorMatches, `got "" for env var name`)
}

func (*pluginSuite) TestUnparseEnvMissing(c *gc.C) {
	env := map[string]string{
		"A": "1",
		"B": "",
		"C": "3",
	}
	raw, err := process.UnparseEnv(env)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(raw, jc.DeepEquals, []string{"A=1", "B=", "C=3"})
}

func (*pluginSuite) TestParseDetailsValid(c *gc.C) {
	input := `{"id":"1234", "status":"running"}`

	ld, err := process.ParseDetails(input)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(ld, jc.DeepEquals, &process.LaunchDetails{
		ID:     "1234",
		Status: "running",
	})
}

func (*pluginSuite) TestParseDetailsEmptyInput(c *gc.C) {
	input := ""

	_, err := process.ParseDetails(input)

	c.Check(err, gc.ErrorMatches, ".*")
}

func (*pluginSuite) TestParseDetailsMissingID(c *gc.C) {
	input := `{"status":"running"}`

	_, err := process.ParseDetails(input)
	c.Assert(err, gc.ErrorMatches, "ID must be set")
}

func (*pluginSuite) TestParseDetailsMissingStatus(c *gc.C) {
	input := `{"id":"1234"}`

	_, err := process.ParseDetails(input)
	c.Assert(err, gc.ErrorMatches, "Status must be set")
}

func (*pluginSuite) TestParseDetailsExtraInfo(c *gc.C) {
	input := `{"id":"1234", "status":"running", "extra":"stuff"}`

	ld, err := process.ParseDetails(input)
	c.Assert(err, jc.ErrorIsNil)

	c.Check(ld, jc.DeepEquals, &process.LaunchDetails{
		ID:     "1234",
		Status: "running",
	})
}