// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package bootstrap

import (
	"context"

	"github.com/juju/juju/environs"
	"github.com/juju/juju/environs/imagemetadata"
	"github.com/juju/juju/environs/simplestreams"
	"github.com/juju/juju/internal/errors"
)

// PrepareRecoveryMachine selects images and applies bootstrap defaults only
// for absent source requirements. Tools and base are fixed by the archive.
// The operation performs no provisioning or controller registration.
func PrepareRecoveryMachine(ctx context.Context, env environs.Environ, args environs.BootstrapParams) (environs.BootstrapParams, error) {
	cons := args.BootstrapConstraints
	if !cons.HasArch() || args.BootstrapBase.Empty() {
		return args, errors.New("recovery requires source architecture and base")
	}
	if cons.HasInstanceRole() && *cons.InstanceRole == "auto" {
		return args, errors.New("archive must record the resolved controller instance role")
	}
	fetcher := simplestreams.NewSimpleStreams(simplestreams.DefaultDataSourceFactory())
	var custom []*imagemetadata.ImageMetadata
	image := ""
	if cons.ImageID != nil {
		image = *cons.ImageID
	}
	metadata, err := bootstrapImageMetadata(ctx, env, fetcher, &args.BootstrapBase, *cons.Arch, image, &custom)
	if err != nil {
		return args, err
	}
	validator, err := env.ConstraintsValidator(ctx)
	if err != nil {
		return args, err
	}
	unsupported, err := validator.Validate(cons)
	if err != nil {
		return args, err
	}
	if len(unsupported) > 0 {
		return args, errors.Errorf("provider cannot honour source recovery constraints %v", unsupported)
	}
	cons, err = validator.Merge(args.ModelConstraints, cons)
	if err != nil {
		return args, err
	}
	if checker, ok := env.(environs.DefaultConstraintsChecker); !ok || checker.ShouldApplyControllerConstraints(cons) {
		cons = withDefaultControllerConstraints(cons)
	}
	args.BootstrapConstraints, args.ImageMetadata = cons, metadata
	return args, nil
}
