// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package common

import (
	"github.com/juju/names/v6"

	"github.com/juju/juju/environs"
	"github.com/juju/juju/internal/errors"
)

// ProvisionRecoveryMachine wraps an instance-only provisioning operation with
// the recovery machine's distinct physical identity. Providers retain their
// finaliser and session semantics. Retaining and cleaning up replacement
// resources after failed recovery is introduced by the later cleanup step.
func ProvisionRecoveryMachine(ctx environs.BootstrapContext,
	params environs.RecoveryMachineParams,
	provision func(environs.BootstrapContext, environs.BootstrapParams) (*environs.BootstrapResult, error),
) (*environs.BootstrapResult, error) {
	if !names.IsValidMachine(params.MachineID) || params.MachineID == "0" {
		return nil, errors.New("recovery requires a distinct physical machine identity")
	}
	args := params.BootstrapParams
	args.RecoveryMachineID = params.MachineID
	return provision(ctx, args)
}
