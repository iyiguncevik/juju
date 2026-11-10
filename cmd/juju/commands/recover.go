// Copyright 2026 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package commands

import (
	"context"
	"time"

	"github.com/juju/errors"
	"github.com/juju/names/v6"

	jujucloud "github.com/juju/juju/cloud"
	jujucmd "github.com/juju/juju/cmd"
	"github.com/juju/juju/cmd/cmd"
	"github.com/juju/juju/cmd/juju/backups"
	"github.com/juju/juju/cmd/juju/common"
	"github.com/juju/juju/cmd/modelcmd"
	"github.com/juju/juju/domain/recovery"
	"github.com/juju/juju/environs"
	internalrecovery "github.com/juju/juju/internal/recovery"
)

func newRecoveryCommand() cmd.Command {
	c := &recoveryCommand{}
	c.CanClearCurrentModel = true
	return modelcmd.Wrap(c, modelcmd.WrapSkipModelFlags, modelcmd.WrapSkipDefaultModel)
}

type recoveryCommand struct {
	modelcmd.ModelCommandBase
	archivePath   string
	assertionPath string
}

func (c *recoveryCommand) Info() *cmd.Info {
	return jujucmd.Info(&cmd.Info{
		Name: "recover", Args: "<backup-file> <assertion-file>",
		Purpose: "Recover a controller from a backup archive.",
		Doc: `This build allocates a replacement controller machine and does not
import state into it. Kubernetes recovery is not enabled in this build.

Recover a replacement controller using the cloud, region and controller name
recorded in the archive. The controller UUID, model UUIDs and controller CA are
preserved. The replacement runs the exact agent version recorded in the backup.
Machine agents come from the archive; the CLI version does not select them.
Source constraints and observed hardware determine the replacement's capacity,
with additional space reserved for archive staging and database import.

Fence the source controller before recovery. If its name is registered on this
client, run juju unregister first.

The archive and its assertion file are given explicitly. The unsigned
assertion, the "<archive>.assert" file written by create-backup, supplies
the expected SHA-256 checksum, which the archive must match.
Local credentials are tried first; if none
are available, the credential archived for the controller cloud is used. An ambiguous
credential selection or authentication failure stops recovery.

A failed or incomplete recovery retains the replacement resources for
diagnosis and manual repair.`,
		Examples: "    juju recover juju-backup.tar.gz juju-backup.tar.gz.assert",
		SeeAlso:  []string{"bootstrap", "create-backup", "unregister"},
	})
}

func (c *recoveryCommand) Init(args []string) error {
	switch {
	case len(args) == 0 || args[0] == "":
		return errors.New("a backup archive is required")
	case len(args) == 1 || args[1] == "":
		return errors.New("a backup assertion file is required")
	}
	if err := cmd.CheckEmpty(args[2:]); err != nil {
		return err
	}
	c.archivePath = args[0]
	c.assertionPath = args[1]
	return nil
}

func (c *recoveryCommand) Run(ctx *cmd.Context) error {
	archivePath := ctx.AbsPath(c.archivePath)
	assertion, err := c.readAssertion(ctx.AbsPath(c.assertionPath))
	if err != nil {
		return err
	}
	info, err := internalrecovery.ValidateArchive(ctx.Context, logger, archivePath, assertion.Checksum)
	if err != nil {
		return errors.Annotate(err, "invalid recovery archive")
	}
	// This build supports machine clouds only, and admits
	// single-controller sources only: Kubernetes and HA recovery are
	// refused until their later steps expand the supported paths.
	if info.CloudType == jujucloud.CloudTypeKubernetes {
		return errors.NotSupportedf("Kubernetes recovery in this build")
	}
	if info.HANodes > 1 {
		return errors.NotSupportedf("recovery of HA controllers in this build")
	}
	targetCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	return c.provisionRecoveryController(ctx.With(targetCtx), info)
}

func recoveryCloud(info *recovery.ArchiveInfo) jujucloud.Cloud {
	source := info.Cloud
	cloud := jujucloud.Cloud{
		Name: source.Name, Type: source.Type, Endpoint: source.Endpoint,
		IdentityEndpoint: source.IdentityEndpoint, StorageEndpoint: source.StorageEndpoint,
		CACertificates: source.CACertificates, SkipTLSVerify: source.SkipTLSVerify,
	}
	for _, auth := range source.AuthTypes {
		cloud.AuthTypes = append(cloud.AuthTypes, jujucloud.AuthType(auth))
	}
	for _, region := range source.Regions {
		endpoint, identityEndpoint, storageEndpoint := region.Endpoint, region.IdentityEndpoint, region.StorageEndpoint
		if endpoint == "" {
			endpoint = cloud.Endpoint
		}
		if identityEndpoint == "" {
			identityEndpoint = cloud.IdentityEndpoint
		}
		if storageEndpoint == "" {
			storageEndpoint = cloud.StorageEndpoint
		}
		cloud.Regions = append(cloud.Regions, jujucloud.Region{
			Name: region.Name, Endpoint: endpoint, IdentityEndpoint: identityEndpoint, StorageEndpoint: storageEndpoint,
		})
	}
	return cloud
}

func (c *recoveryCommand) recoveryCredentialsForCloud(ctx *cmd.Context, provider environs.EnvironProvider, cloud jujucloud.Cloud, sourceRegion string, source *recovery.CredentialInfo) (bootstrapCredentials, string, error) {
	credential, name, detected, err := c.localRecoveryCredential(ctx, provider, cloud, sourceRegion)
	if err != nil {
		return bootstrapCredentials{}, "", errors.Annotate(err, "resolving local recovery credentials; configure a default with juju default-credential")
	}
	if credential != nil {
		ctx.Infof("Using local credentials for cloud %q", cloud.Name)
		creds := bootstrapCredentials{credential: credential, name: name}
		if detected {
			creds.detectedName, creds.name = name, ""
		}
		return creds, sourceRegion, nil
	}
	if source == nil {
		return bootstrapCredentials{}, "", errors.New("no local or archived controller credential is available; use juju add-credential")
	}
	if source.Revoked || source.Invalid {
		return bootstrapCredentials{}, "", errors.New("archived controller credential is revoked or invalid; configure local credentials")
	}
	if !names.IsValidCloudCredentialName(source.Name) {
		return bootstrapCredentials{}, "", errors.NotValidf("archived credential name %q", source.Name)
	}
	archived := jujucloud.NewCredential(jujucloud.AuthType(source.AuthType), source.Attributes)
	region, err := common.ChooseCloudRegion(cloud, sourceRegion)
	if err != nil {
		return bootstrapCredentials{}, "", err
	}
	credential, err = provider.FinalizeCredential(ctx, environs.FinalizeCredentialParams{
		Credential: archived, CloudName: cloud.Name, CloudEndpoint: region.Endpoint,
		CloudIdentityEndpoint: region.IdentityEndpoint, CloudStorageEndpoint: region.StorageEndpoint,
	})
	if err != nil {
		return bootstrapCredentials{}, "", errors.Annotate(err, "finalising archived recovery credential")
	}
	ctx.Infof("Using archived controller credentials for cloud %q", cloud.Name)
	return bootstrapCredentials{credential: credential, name: source.Name}, sourceRegion, nil
}

// Select a credential before finalising it. A failure while processing a
// selected credential must never be mistaken for an absent credential.
func (c *recoveryCommand) localRecoveryCredential(ctx *cmd.Context, provider environs.EnvironProvider, cloud jujucloud.Cloud, sourceRegion string) (*jujucloud.Credential, string, bool, error) {
	stored, err := c.ClientStore().CredentialForCloud(cloud.Name)
	if err != nil && !errors.Is(err, errors.NotFound) {
		return nil, "", false, err
	}
	detected := false
	if err != nil || len(stored.AuthCredentials) == 0 {
		stored, err = provider.DetectCredentials(cloud.Name)
		if errors.Is(err, errors.NotFound) {
			return nil, "", false, nil
		}
		if err != nil {
			return nil, "", false, err
		}
		if stored == nil || len(stored.AuthCredentials) == 0 {
			return nil, "", false, nil
		}
		detected = true
	}
	name := stored.DefaultCredential
	if name == "" {
		if len(stored.AuthCredentials) > 1 {
			return nil, "", false, modelcmd.ErrMultipleCredentials
		}
		for key := range stored.AuthCredentials {
			name = key
		}
	}
	selected, ok := stored.AuthCredentials[name]
	if !ok {
		return nil, "", false, errors.Errorf("local default credential %q is unavailable", name)
	}
	if !names.IsValidCloudCredentialName(name) {
		return nil, "", false, errors.NotValidf("local credential name %q", name)
	}
	credential, err := modelcmd.FinalizeFileContent(&selected, provider)
	if err != nil {
		return nil, "", false, err
	}
	region, err := common.ChooseCloudRegion(cloud, sourceRegion)
	if err != nil {
		return nil, "", false, err
	}
	credential, err = provider.FinalizeCredential(ctx, environs.FinalizeCredentialParams{
		Credential: *credential, CloudName: cloud.Name, CloudEndpoint: region.Endpoint,
		CloudIdentityEndpoint: region.IdentityEndpoint, CloudStorageEndpoint: region.StorageEndpoint,
	})
	return credential, name, detected, err
}

// readAssertion opens and parses the backup assertion file. Every open,
// read and close failure carries the assertion filename; a parse error
// wins over a close failure.
func (c *recoveryCommand) readAssertion(assertionPath string) (*backups.Assertion, error) {
	file, err := c.Filesystem().Open(assertionPath)
	if err != nil {
		return nil, errors.Annotatef(err, "reading backup assertion %q", assertionPath)
	}
	assertion, err := backups.ReadAssertion(file)
	closeErr := file.Close()
	if err != nil {
		return nil, errors.Annotatef(err, "reading backup assertion %q", assertionPath)
	}
	if closeErr != nil {
		return nil, errors.Annotatef(closeErr, "reading backup assertion %q", assertionPath)
	}
	return assertion, nil
}
