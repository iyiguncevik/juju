// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package common_test

import (
	"testing"

	"github.com/juju/tc"

	"github.com/juju/juju/environs"
	envtesting "github.com/juju/juju/environs/testing"
	"github.com/juju/juju/internal/provider/common"
)

type recoveryProvisioningSuite struct{}

func TestRecoveryProvisioningSuite(t *testing.T) { tc.Run(t, &recoveryProvisioningSuite{}) }

// TestAppliesDistinctPhysicalIdentity verifies the provisioning wrapper
// carries the replacement's physical machine identity in the bootstrap
// parameters handed to the provider operation.
func (s *recoveryProvisioningSuite) TestAppliesDistinctPhysicalIdentity(c *tc.C) {
	ctx := envtesting.BootstrapContext(c.Context(), c)
	captured := environs.BootstrapParams{}
	result, err := common.ProvisionRecoveryMachine(ctx,
		environs.RecoveryMachineParams{MachineID: "1234567"},
		func(_ environs.BootstrapContext, args environs.BootstrapParams) (*environs.BootstrapResult, error) {
			captured = args
			return &environs.BootstrapResult{}, nil
		})
	c.Assert(err, tc.ErrorIsNil)
	c.Check(result, tc.NotNil)
	c.Check(captured.RecoveryMachineID, tc.Equals, "1234567")
}

// TestRejectsBootstrapPhysicalIdentity verifies the source's logical
// machine identity is never used as the replacement's physical identity.
func (s *recoveryProvisioningSuite) TestRejectsBootstrapPhysicalIdentity(c *tc.C) {
	ctx := envtesting.BootstrapContext(c.Context(), c)
	for _, machineID := range []string{"", "0"} {
		result, err := common.ProvisionRecoveryMachine(ctx,
			environs.RecoveryMachineParams{MachineID: machineID},
			func(_ environs.BootstrapContext, _ environs.BootstrapParams) (*environs.BootstrapResult, error) {
				return &environs.BootstrapResult{}, nil
			})
		c.Check(result, tc.IsNil)
		c.Check(err, tc.ErrorMatches, "recovery requires a distinct physical machine identity")
	}
}
