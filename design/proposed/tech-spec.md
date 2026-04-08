## Abstract

This document defines the technical implementation for the bootstrap flow of
the controller snap path. It specifies the bootstrap-time contracts between:

- juju client
- cloud init
- controller agent
- charm

For artifact selection, `--agent-version` is dropped in favour of snap channel
semantics: the published-binaries path installs the latest revision in the
`<major.minor>/stable` channel (with `--controller-snap-revision` for explicit
pinning), and the local-build path accepts a user-provided snap via
`--controller-snap-path`. If `--controller-snap-assert-path` is also provided
the snap is installed normally; if omitted, the absence of an assertion is taken
as an explicit opt-in to dangerous mode. In both paths, machine agent binaries
are resolved from simplestreams using the current `major.minor` track,
selecting the newest available patch.

## Rationale

`design/proposed/functional-spec.md` describes sequence. This document defines
the concrete technical contracts needed to implement that sequence without
duplicating logic across layers.

The main driver is the controller agent worker topology in
`cmd/jujud-controller/agent/model/manifolds.go`:

- several workers still call the API server even when running inside the
  controller agent process;
- controller-only workers can read/write state through local services directly;
- machine-agent-only workers must not remain in controller model manifolds;
- workers needed in both contexts require an explicit per-worker decision:
  controller-specific copy, or a shared worker behind a data-access facade.

Without this contract, bootstrap behavior remains harder to reason about,
slower than needed, and more failure-prone.

## Scope

This project handles only the bootstrap path for the first controller machine
(`controller/0`) until control is handed to normal charm-managed operations.

### In scope

- Bootstrap-time responsibilities for controller snap acquisition, delivery,
  install, and first-start readiness.
- Bootstrap command/config/API contracts.
- Controller-agent worker loading and data-access strategy for model manifolds.
- Artifact metadata and distribution contracts needed by bootstrap.

### Out of scope

- Build system design or implementation (`make`, linking strategy, build jobs).
- Snap packaging and release pipeline design.
- CI/CD job definitions.
- Post-bootstrap lifecycle behavior.

## Technical Specification (Bootstrap Flow)

### 1. juju client

No major changes in following areas:
- Machine provisioning → non-goal —future work


Bootstrap contract:

- provision the initial controller machine;
- resolve controller snap artifacts for the selected Juju version;
- pass all controller snap install inputs into instance configuration;
- upload/register required artifacts before bootstrap completion.

#### Version and artifact resolution

Acquire artifacts:
- `snapstore`: `.snap` and `.assert` will be downloaded by cloud-init
- `local`: upload provided `.snap` and `.assert` files
- `local` (no assert): upload `.snap` only; snap will be installed in dangerous mode

##### Bootstrap with published binaries

###### Bootstrap with latest version

Snap channels use a `<major.minor>/<risk>` format (e.g. `4.0/stable`). There is
no built-in mechanism to install a specific patch release (e.g. `4.0.3`) by
version number alone, which makes `--agent-version` semantically incompatible
with snap as an artifact source.

**Selected approach**: drop `--agent-version` for the snap path and align with
snap semantics. A channel always resolves to the current latest revision for
that track, so Juju installs the latest revision in `<major.minor>/stable` by
default. Without `--agent-version`, the client queries simplestreams for the
current `major.minor` track and selects the newest available patch for machine
agents via `availableTools.Newest()`. This is already implemented and requires
no new mechanism.

The following alternatives were considered and rejected:

- **Per-patch channels** (e.g. `4.0.3/stable`): Technically possible but
  diverges from snap conventions and creates significant channel management
  overhead in the release pipeline.

- **Hardcoded revision map**: Maintain a curated mapping of Juju versions to
  snap store revision numbers. This becomes fragile quickly: revisions differ
  per architecture, and the mapping must be updated for every patch release
  across every snap in the install set (controller snap, toolchain snap, etc.).

- **Bundle machine agent tools inside the controller snap**: Ship `jujud`
  binaries for all supported bases and architectures inside the controller snap
  (or a companion snap) and have cloud-init extract them locally, eliminating
  the simplestreams dependency entirely. Removes the version-coupling problem
  but significantly increases snap size and diverges from the existing tools
  distribution model.

###### Bootstrap with a snap revision

When a user specifies `--controller-snap-revision` to pin to a particular snap
revision, the actual Juju version carried by that revision is not known in
advance. A snap revision is an opaque integer with no embedded version string,
so the version can only be determined by downloading the snap from the store and
inspecting its metadata. A further consequence of pinning to a revision is that
the controller snap will not receive automatic updates from the store; any
future upgrade must explicitly supply either a new revision via
`--controller-snap-revision` or a channel.

**Selected approach**: when `--controller-snap-revision` is provided, the client
downloads the snap from the store during bootstrap, interrogates the downloaded
file to discover the Juju version it carries, and then uploads it to the
controller — following the same path as a locally supplied snap via
`--controller-snap-path`. The discovered version is then used to constrain the
simplestreams search for machine agent binaries. Upgrades from a
revision-pinned controller require the operator to supply either a new
`--controller-snap-revision` or a channel to override the pin.

The following alternatives were considered and rejected:

- **Derive version from the installed controller snap**: After the controller
  snap installs on the bootstrap machine, read its version (e.g. via
  `snap info`) and use that exact version to constrain the simplestreams query.
  Enforces strict version lockstep but requires the simplestreams query to run
  from the bootstrap machine rather than the client, which we want to avoid as
  it adds sequencing complexity and couples the tools fetch to cloud-init
  completion.

- **Require `--agent-version` alongside `--controller-snap-revision`**: Allow
  the user to specify the agent version explicitly. Avoids any discovery step
  but is inconsistent with the other bootstrap paths, which do not require
  `--agent-version`, and places an unnecessary burden on the operator to know
  the exact version carried by a given revision.

- **Use the latest available agent version**: Query simplestreams for the
  newest `major.minor` patch without resolving the revision's version. Requires
  no download or store query but does not allow the operator to reproduce an
  earlier agent version, and risks a version mismatch between the machine agent
  and the controller snap installed from the pinned revision.

###### Bootstrap with a snap channel

When bootstrapping with a snap channel, the controller snap is installed
directly from the store by snapd on the bootstrap machine. The agent version
must still be determined on the client side before the controller snap installs,
so that compatible machine agent binaries can be fetched from simplestreams.
Unlike the revision case, the version can be resolved without downloading the
snap: `snap info juju-controller --channel=<channel>` returns the version
currently published in that channel.

**Selected approach**: run `snap info` for the given channel on the client
during bootstrap to resolve the published version without downloading the snap.
The returned version string is used to constrain the simplestreams query for
machine agent binaries, ensuring the machine agent version is consistent with
the controller snap that will be installed.

The following alternatives were considered and rejected:

- **Use simplestreams newest without version resolution**: Without resolving the
  channel version, always fetch the newest `major.minor` patch from
  simplestreams. Simpler, but risks a version mismatch between the controller
  snap and machine agents if the two sources publish patch releases at different
  times.

- **Derive version from the installed controller snap**: After the controller
  snap installs on the bootstrap machine, read its version (e.g. via
  `snap info`) and use that exact version to constrain the simplestreams query.
  Enforces strict lockstep but requires cloud-init to complete before the tools
  query can run, adding sequencing complexity to the bootstrap flow.

##### Bootstrap with local build

In this path the user provides the controller snap file directly via
`--controller-snap-path`. The snap file is available locally, so there is no
need to download it from the store. The approach to discovering the agent
version and handling the snap installation depends on whether a snap assertion
file is also provided.

**Local snap with assert file**

When `--controller-snap-assert-path` is supplied alongside
`--controller-snap-path`, the snap has a valid store-signed assertion. This case
is equivalent to "Bootstrap with a snap revision" except the snap is already
present on disk — there is no need to download it. The snap file is interrogated
directly to extract the embedded Juju version string, which is then used to
constrain the simplestreams search for compatible machine agent binaries.

**Selected approach**: read the version from the local snap file metadata, use
it to pin the simplestreams query to the matching `major.minor.patch`, then
upload and install the snap with its assertion. The agent version is fully
determined without any store interaction.

**Local snap without assert file (development build)**

When only `--controller-snap-path` is provided with no assertion file, the snap
is a locally built development or pre-release artifact. The snap must be
installed with `snap install --dangerous` (no signature verification, no
interface auto-connections). Because the snap may carry a version string that
does not yet exist in simplestreams, strict version pinning can cause a hard
failure.

Snap interface connections that are normally granted automatically via store
declarations (e.g. The focus is implementation behavior from artifact selection
through first controller start, with controller-agent worker design as the
primary concern. `snapd-control`, `lxd`, `system-observe`) are not established
in dangerous mode. The controller snap relies on several of these interfaces to
manage machines and interact with the system. Without them the controller may
start but fail at runtime when it attempts to use those capabilities.

**Selected approach**: attempt to read the version from the local snap file
metadata and use it to upload matching agent binaries if they are available.
When no matching binaries exist locally for the exact version, fall back to the
newest available patch in the `major.minor` track. This avoids a hard failure
while keeping the behaviour as close as possible to the snap's actual version.

After a dangerous-mode install, emit `snap connect` commands for every
interface the controller snap requires. This partially restores functionality
but requires maintaining a hardcoded list of interfaces that must stay in sync
with the snap's `snapcraft.yaml`, and some privileged interfaces cannot be
connected without store assertions regardless.


#### Command surface

- `--agent-version`
  Deprecated for snap-based bootstrap paths. Machine agent version is resolved
  automatically: when `--controller-snap-revision` or `--controller-snap-path`
  is provided the version is read from the snap file; when
  `--controller-snap-channel` is used the version is resolved via `snap info`;
  otherwise the newest available patch in the current `major.minor` track is
  selected from simplestreams.

- `--build-agent`
  Builds machine/unit `jujud` tools from local source and uploads them to the
  bootstrap instance. Has no effect on how the controller snap is sourced or
  installed.

- `--controller-snap-path`
  Path to a locally built controller snap file (`.snap`). The snap is uploaded
  to the bootstrap instance. If `--controller-snap-assert-path` is not also
  provided, the snap is installed in dangerous mode (no signature verification,
  no interface auto-connections — intended for development use only).

- `--controller-snap-assert-path`
  Path to the snap assertion file (`.assert`) for the snap provided via
  `--controller-snap-path`. When supplied, the snap is installed via
  `snap ack` + `snap install` with full signature verification and interface
  auto-connections. Omitting this flag is an explicit opt-in to dangerous mode.

- `--controller-snap-channel`
  Snap store channel from which to install the controller snap
  (e.g. `4.0/stable`, `4.0/edge`). Juju resolves the published version for
  the channel at bootstrap time using `snap info` and uses it to fetch
  compatible machine agent binaries from simplestreams. Defaults to
  `<major.minor>/stable` if neither this flag nor
  `--controller-snap-revision` is provided.

- `--controller-snap-revision` 
  Specific snap store revision to install (e.g. `34345`). When provided, Juju
  downloads the snap from the store, reads its embedded version, and uploads it
  to the bootstrap instance. The controller snap will not receive automatic store
  updates; future upgrades must supply a new `--controller-snap-revision` or a
  `--controller-snap-channel`. Cannot be combined with
  `--controller-snap-channel`.

- `--controller-charm-path`
  No changes.

- `--controller-charm-channel`
  No changes.

### 2. cloud init

Primary implementation files:

- `internal/cloudconfig/userdatacfg.go`
- `internal/cloudconfig/instancecfg/instancecfg.go`
- `internal/cloudconfig/cloudinit/cloudinit_ubuntu.go`

Current bootstrap behavior (baseline):

- `ConfigureJuju` downloads `jujud` tools (`addDownloadToolsCmds`), runs
  `jujud bootstrap-state` (`configureBootstrap`), and starts the machine agent
  (`addMachineAgentToBoot`).
- Snap logic only configures snap proxy/store assertions
  (`snap ack /etc/snap.assertions`, `snap set core proxy.store=...`).
- There is no `juju-controllerd` snap install/start/version-check step.

Bootstrap responsibilities:

- carry source-mode and artifact inputs from `juju bootstrap`;
- carry snap proxy inputs (`SnapStoreAssertions`, `SnapStoreProxyID`, `SnapStoreProxyURL`);
- execute deterministic `snap ack` / install / service start flow;
- fail explicitly when assertion handling or snap install fails.

Required changes for bootstrap flow:

1. **Bootstrap parameter plumbing**
   - Extend `StateInitializationParams` and `stateInitializationParamsInternal`
     with controller snap fields for source mode, expected version/channel, and
     optional inline snap/assert blobs.
   - Update `Marshal`/`Unmarshal` and propagation from bootstrap CLI/config into
     `InstanceConfig.Bootstrap`.
2. **Controller snap install command generation**
   - Add controller-snap command generation in `ConfigureJuju` with
     mode-specific flow:
     - `snapstore`: `snap download juju-controllerd --channel=<major.minor>/stable`,
       then `snap ack`, then `snap install`;
     - `local` (with assert): write embedded `.snap` and `.assert`, then
       `snap ack`, then `snap install`;
     - `local` (no assert): write embedded `.snap`, then
       `snap install --dangerous`.
   - For all modes, execute `snap refresh --hold juju-controllerd`
     immediately after install.
3. **Bootstrap safety gates**
   - Add version coherence check: installed snap version must match target Juju
     version before bootstrap handoff.
   - Add explicit readiness gate for `juju-controllerd` before cloud-init
     reports bootstrap handoff complete.
4. **Failure semantics**
   - Keep fail-fast behavior (`set -xe`) and do not silently fall back across
     source modes.
   - Emit progress/error logs and remove temporary snap/assert files after use.

### 3. controller agent

This is the primary implementation section.

Primary focus file: `cmd/jujud-controller/agent/model/manifolds.go`.

#### 3.1 Bootstrap artifact contracts

Controller artifacts are stored in object store with explicit metadata:

- target Juju version;
- snap revision/channel;
- artifact kind (`controller-snap`, `controller-assert`, `agent-tools`);
- hashes (SHA256/SHA384), size, and object-store identity;
- source mode (`snapstore`, `local`); presence of assert blob indicates whether dangerous mode applies.

Integrity rules:

- verify hash before metadata registration;
- keep metadata immutable for `(version, revision, kind)`;
- use deterministic write/cleanup behavior on partial failures.

#### 3.2 Worker classification for `model/manifolds.go`

1. **Controller-only worker**
   - Worker exists only for controller responsibilities.
   - Action: keep in controller manifolds.

  1a. **Controller-only worker requiring data-access change**
    - Worker is controller-only but currently uses `APICallerName` for
      model-local data.
    - Action: remove controller-local API round-trips and switch to direct
      state/domain services access.

2. **Machine-agent-only worker**
   - Worker belongs to machine agent responsibilities and must not be loaded
     in controller model manifolds.
   - Action: remove from controller model manifold wiring.

3. **Shared worker (controller + machine)**
   - Worker is needed in both contexts.
   - Does not use `APICallerName`
   - Action: keep in both manifolds

  3a. **Shared worker requiring controller-side data-access change**
    - Worker is shared, and the controller-side path currently uses
      `APICallerName` for model-local data.
    - Action: decide case by case:
      - a controller-specific copy using local state/domain services; or
      - a facade-backed shared worker with controller-local and machine-API
        implementations.

#### 3.3 Manifold inventory from `model/manifolds.go`

Classification by manifold is:

This inventory is scoped to `cmd/jujud-controller/agent/model/manifolds.go`
only (the per-model engine).

It does not include controller machine-level manifolds in
`cmd/jujud-controller/agent/machine/manifolds.go` such as
`object-store`, `watcher-registry`, or `model-worker-manager`.
Those run in the machine engine and can also spawn additional child workers,
which is why observed runtime worker counts can be much higher than this list.

- `commonManifolds`:
  - `agent` → `3. Shared worker`
  - `clock` → `3. Shared worker`
  - `api-config-watcher` → `3. Shared worker`
  - `api-caller` → `3. Shared worker`
  - `provider-service-factories` → `1. Controller-only worker`
  - `domain-services` → `1. Controller-only worker`
  - `lease-manager` → `1. Controller-only worker`
  - `http-client` → `1. Controller-only worker`
  - `api-remote-relation-caller` → `1. Controller-only worker`
  - `log-sink` → `1. Controller-only worker`
  - `logging-config-updater` → `3a. Shared worker requiring controller-side data-access change` ⚠️
  - `not-dead-flag` → `1. Controller-only worker`
  - `is-responsible-flag` → `1. Controller-only worker`
  - `valid-credential-flag` → `3a. Shared worker requiring controller-side data-access change` ⚠️
  - `migration-fortress` → `3. Shared worker`
  - `migration-inactive-flag` → `3a. Shared worker requiring controller-side data-access change` ⚠️
  - `migration-master` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `charm-revisioner` → `1. Controller-only worker`
  - `remote-relation-consumer` → `1. Controller-only worker`
  - `removal` → `1. Controller-only worker`
  - `provider-tracker` → `1. Controller-only worker`
  - `storage-provisioner` → `3a. Shared worker requiring controller-side data-access change` ⚠️
  - `async-charm-downloader` → `1. Controller-only worker`
  - `secrets-pruner` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `user-secrets-drain-worker` → `3a. Shared worker requiring controller-side data-access change` ⚠️
  - `operation-pruner` → `1. Controller-only worker`
  - `change-stream-pruner` → `3. Shared worker`

- `IAASManifolds` additions:
  - `compute-provisioner` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `firewaller` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `instance-poller` → `1. Controller-only worker`
  - `agent-binary-fetcher` → `1. Controller-only worker`

- `CAASManifolds` additions:
  - `caas-firewaller` → `1. Controller-only worker`
  - `caas-model-operator` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `caas-model-config-manager` → `1a. Controller-only worker requiring data-access change` ⚠️
  - `caas-application-provisioner` → `1a. Controller-only worker requiring data-access change` ⚠️

No current entries in `model/manifolds.go` are classified as
`2. Machine-agent-only worker`.

Expected direction for bootstrap implementation from this classification:

- workers already driven by `DomainServicesName`, provider services, or local value workers remain on local service paths;
- workers using `APICallerName` are reviewed individually to remove controller-local API round-trips where not required;
- workers that fundamentally require remote calls keep explicit API usage;
- no machine-agent-only responsibilities are introduced into this manifold.

### 4. charm

The charm is responsible for bootstrap-time controller unit convergence:

- consume selected controller artifact metadata;
- ensure the controller snap is installed and services are started;
- expose clear blocked/error states for bootstrap failures;
- keep operations retry-safe and idempotent.

Bootstrap readiness requires successful charm-driven convergence on
`controller/0`.

## Design Decisions

1. **Version mismatch policy**
   - Decide whether version mismatch is warning or fatal.
   - Recommended: mismatch is fatal before bootstrap worker handoff.

2. **Snap storage location**
   - Decide whether snaps/assertions are stored as charm resource or not.
