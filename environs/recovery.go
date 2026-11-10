// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package environs

// RecoveryControllerParams identifies the archived source controller a
// replacement is allocated for.
type RecoveryControllerParams struct {
	// ControllerUUID is the archived source controller's identity.
	ControllerUUID string

	// ControllerName is the archived source controller's name.
	ControllerName string

	// ControllerModelUUID identifies the archived source controller model.
	ControllerModelUUID string
}

// RecoveryControllerPreparer verifies access to the surviving source
// environment before a replacement is provisioned. It must create or
// modify no provider resources.
type RecoveryControllerPreparer interface {
	PrepareForRecovery(ctx BootstrapContext, params RecoveryControllerParams) error
}

// RecoveryMachineParams preserves the logical source identity while giving
// the replacement's provider resources a distinct physical name.
type RecoveryMachineParams struct {
	BootstrapParams
	MachineID string
}

// RecoveryMachineProvisioner provisions only the replacement machine, using
// the distinct physical identity carried by RecoveryMachineParams. The
// finaliser remains usable after ProvisionRecoveryMachine returns. Retaining
// and cleaning up replacement resources after failed recovery is introduced
// by the later cleanup step.
type RecoveryMachineProvisioner interface {
	RecoveryControllerPreparer
	ProvisionRecoveryMachine(BootstrapContext, RecoveryMachineParams) (*BootstrapResult, error)
}
