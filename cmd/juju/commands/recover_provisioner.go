// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package commands

import (
	"context"
	"encoding/binary"
	"sort"
	"strconv"
	"time"

	"github.com/juju/errors"
	"github.com/juju/utils/v4/ssh"

	"github.com/juju/juju/cmd/cmd"
	"github.com/juju/juju/controller"
	"github.com/juju/juju/core/base"
	"github.com/juju/juju/core/instance"
	"github.com/juju/juju/domain/recovery"
	"github.com/juju/juju/environs"
	"github.com/juju/juju/environs/bootstrap"
	"github.com/juju/juju/environs/cloudspec"
	environscmd "github.com/juju/juju/environs/cmd"
	"github.com/juju/juju/environs/config"
	internalrecovery "github.com/juju/juju/internal/recovery"
	jujussh "github.com/juju/juju/internal/ssh"
	"github.com/juju/juju/internal/storage"
	"github.com/juju/juju/internal/tools"
	"github.com/juju/juju/internal/uuid"
)

// provisionRecoveryController owns replacement provisioning. It deliberately
// does not call the fresh controller bootstrap orchestrator: allocation ends
// at the retained replacement machine, before any archive hand-off.
func (c *recoveryCommand) provisionRecoveryController(ctx *cmd.Context, info *recovery.ArchiveInfo) error {
	bundle, err := internalrecovery.SelectAgent(ctx, ctx.AbsPath(c.archivePath), info)
	if err != nil {
		return errors.Annotate(err, "selecting archived controller agent")
	}
	defer bundle.Close()
	ctx.Infof("Using archived controller agent %s", bundle.Tools.Version)
	sourceBase, err := base.ParseBaseFromString(info.SourceBase)
	if err != nil {
		return errors.Annotate(err, "archive does not record a usable controller base")
	}
	provider, err := environs.Provider(info.CloudType)
	if err != nil {
		return errors.Trace(err)
	}
	sourceCloud := recoveryCloud(info)
	credentials, region, err := c.recoveryCredentialsForCloud(ctx, provider, sourceCloud, info.Region, info.Credential)
	if err != nil {
		return err
	}
	spec, err := cloudspec.MakeCloudSpec(sourceCloud, region, credentials.credential)
	if err != nil {
		return err
	}
	spec.IsControllerCloud = true
	controllerConfig, modelConfig, err := recoveryConfigs(info, provider)
	if err != nil {
		return errors.Annotate(err, "building recovery node configuration")
	}
	if err := provider.ValidateCloud(ctx, spec); err != nil {
		return err
	}
	env, err := environs.Open(ctx, provider, environs.OpenParams{ControllerUUID: info.ControllerUUID, Cloud: spec, Config: modelConfig}, environs.NoopCredentialInvalidator())
	if err != nil {
		return err
	}
	stdCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	provisionCtx := environscmd.BootstrapContext(stdCtx, ctx)
	provisioner, ok := env.(environs.RecoveryMachineProvisioner)
	if !ok {
		return errors.NotSupportedf("machine recovery provisioning for %q", info.CloudType)
	}
	if err := provisioner.PrepareForRecovery(provisionCtx, environs.RecoveryControllerParams{
		ControllerUUID: info.ControllerUUID, ControllerName: info.ControllerName,
		ControllerModelUUID: info.ControllerModelUUID,
	}); err != nil {
		return err
	}
	attempt, err := uuid.NewUUID()
	if err != nil {
		return err
	}
	physicalID := strconv.FormatUint(binary.BigEndian.Uint64(attempt[:8])&((1<<63)-1), 10)
	if physicalID == "0" || physicalID == info.MachineName {
		return errors.New("replacement provisioning identity collides with the source")
	}

	keyFiles := ssh.PrivateKeyFiles()
	if len(keyFiles) == 0 {
		return errors.New("no Juju SSH keys available for recovery")
	}
	sort.Strings(keyFiles)
	keys, err := jujussh.PublicKeysForPrivateKeyFiles(keyFiles)
	if err != nil {
		return err
	}
	sourceConstraints := info.Constraints
	if sourceConstraints.Arch != nil && *sourceConstraints.Arch != bundle.Tools.Version.Arch {
		return errors.New("archived agent architecture differs from source constraints")
	}
	sourceConstraints.Arch = &bundle.Tools.Version.Arch
	cons, err := internalrecovery.SizeRecoveryDisk(info, sourceConstraints)
	if err != nil {
		return err
	}
	pools := make(map[string]storage.Attrs, len(info.StoragePools))
	for name, recorded := range info.StoragePools {
		attrs := make(storage.Attrs, len(recorded))
		for key, value := range recorded {
			attrs[key] = value
		}
		pools[name] = attrs
	}
	// The physical resource name belongs to this attempt; the installed
	// agent retains the archived logical machine identity in a later step.
	var started instance.Id
	params, err := bootstrap.PrepareRecoveryMachine(provisionCtx, env, environs.BootstrapParams{
		StoragePools: pools,
		CloudName:    info.CloudName, CloudRegion: info.Region,
		ControllerConfig: controllerConfig, AuthorizedKeys: keys,
		BootstrapConstraints: cons, AvailableTools: tools.List{bundle.Tools},
		BootstrapBase: sourceBase, SupportedBootstrapBases: []base.Base{sourceBase},
		InstanceStarted: func(id instance.Id) { started = id },
	})
	if err != nil {
		return err
	}
	if _, err := provisioner.ProvisionRecoveryMachine(provisionCtx, environs.RecoveryMachineParams{BootstrapParams: params, MachineID: physicalID}); err != nil {
		return err
	}
	if started == "" {
		return errors.New("provider returned no replacement instance identity")
	}
	ctx.Infof(`Recovery target allocated
  provider instance: %s
  recovery phase: target allocation complete; controller import is not enabled`, started)
	// The replacement stays in place: retention is the default until a
	// cleanup policy is introduced.
	return errors.New("controller recovery import is not enabled in this build")
}

func recoveryConfigs(info *recovery.ArchiveInfo, provider environs.EnvironProvider) (controller.Config, *config.Config, error) {
	fields, _, err := controller.ConfigSchema.ValidationSchema()
	if err != nil {
		return nil, nil, err
	}
	ctrlAttrs := make(map[string]any)
	for key, value := range info.ControllerConfig {
		if field, ok := fields[key]; ok {
			coerced, err := field.Coerce(value, []string{key})
			if err != nil {
				return nil, nil, err
			}
			ctrlAttrs[key] = coerced
		}
	}
	ctrl, err := controller.NewConfig(info.ControllerUUID, info.CACert, ctrlAttrs)
	if err != nil {
		return nil, nil, err
	}
	attrs := make(map[string]any)
	for key, value := range info.ModelConfig {
		attrs[key] = value
	}
	attrs[config.UUIDKey] = info.ControllerModelUUID
	attrs["name"] = "controller"
	attrs["type"] = info.CloudType
	attrs[config.AgentVersionKey] = info.AgentVersion.String()
	// Model config coercion accepts persisted string representations; provider
	// fields are validated when opening the provider.
	modelConfig, err := config.New(config.UseDefaults, attrs)
	return ctrl, modelConfig, err
}
