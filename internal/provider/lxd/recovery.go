// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package lxd

import (
	"github.com/juju/juju/environs"
	"github.com/juju/juju/internal/provider/common"
)

// PrepareForRecovery validates access to the existing source environment.
// It creates no provider resources.
func (env *environ) PrepareForRecovery(ctx environs.BootstrapContext, params environs.RecoveryControllerParams) error {
	return env.PrepareForBootstrap(ctx, params.ControllerName)
}

// ProvisionRecoveryMachine retains the provider's finalisation lifecycle and
// provisions the replacement with a distinct physical resource name.
func (env *environ) ProvisionRecoveryMachine(ctx environs.BootstrapContext, params environs.RecoveryMachineParams) (*environs.BootstrapResult, error) {
	return common.ProvisionRecoveryMachine(ctx, params, env.Bootstrap)
}

var _ environs.RecoveryMachineProvisioner = (*environ)(nil)
