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

Snap channels use a `<major.minor>/<risk>` format (e.g. `4.0/stable`). There is
no built-in mechanism to install a specific patch release (e.g. `4.0.3`) by
version number alone, which makes `--agent-version` semantically incompatible
with snap as an artifact source.

**Selected approach**: drop `--agent-version` for the snap path and align with
snap semantics. A channel always resolves to the current latest revision for
that track, so Juju installs the latest revision in `<major.minor>/stable` by
default. Users who require a specific revision pass `--controller-snap-revision`
directly instead of a version string.

The following alternatives for the controller snap were considered and rejected:

- **Per-patch channels** (e.g. `4.0.3/stable`): Technically possible but
  diverges from snap conventions and creates significant channel management
  overhead in the release pipeline.

- **Hardcoded revision map**: Maintain a curated mapping of Juju versions to
  snap store revision numbers. This becomes fragile quickly: revisions differ
  per architecture, and the mapping must be updated for every patch release
  across every snap in the install set (controller snap, toolchain snap, etc.).

###### Machine agent version without `--agent-version`

Removing `--agent-version` raises a secondary question: machine agents deployed
to provisioned machines are still downloaded from simplestreams (legacy path),
so something must determine which version to fetch.

Currently, when `--agent-version` is not supplied, the bootstrap code in
`environs/bootstrap/tools.go` (`findPackagedTools`) passes a nil version to the
simplestreams lookup. `findBootstrapTools` then builds a general constraint
scoped to the current `major.minor` and selects the newest available patch via
`availableTools.Newest()`.

**Selected approach**: make this existing fallback the only path. Without
`--agent-version`, the client always queries simplestreams for the current
`major.minor` track and installs the newest available patch. This is already
implemented and requires no new mechanism. The accepted trade-off is that the
machine agent patch version is not explicitly pinned — it follows whatever is
newest in simplestreams at bootstrap time, consistent with snap channel
semantics.

The following alternatives were considered and rejected:

- **Derive version from the installed controller snap**: After the controller
  snap installs on the bootstrap machine, read its version (e.g. via
  `snap info`) and use that exact version to constrain the simplestreams query.
  This enforces strict version lockstep between controller and machine agents
  but requires cloud-init to complete the snap install before the tools query
  can run, adding sequencing complexity to the bootstrap flow.

- **Resolve version from snap revision via snap store API**: When
  `--controller-snap-revision` is provided, query the snap store to resolve the
  Juju version that revision carries, then use that version to pin the
  simplestreams search. Retains precise pinning without a separate version flag
  but introduces a snap store API call from the client at bootstrap time.

- **Bundle machine agent tools inside the controller snap**: Ship `jujud`
  binaries for all supported bases and architectures inside the controller snap
  (or a companion snap) and have cloud-init extract them locally, eliminating
  the simplestreams dependency entirely. Removes the version-coupling problem
  but significantly increases snap size and diverges from the existing tools
  distribution model.

##### Bootstrap with local build

In this path the user provides the controller snap file directly via
`--controller-snap-path`. The snap version is known from the file itself, so there
is no ambiguity about which controller version will be installed.

###### Machine agent version

The same question from the published-binaries path applies here: without
`--agent-version`, something must determine which machine agent version to fetch
from simplestreams.

**Selected approach**: same as the published-binaries path — query simplestreams
for the current `major.minor` track and select the newest available patch. The
local snap carries an embedded version string; that string could in principle be
used to pin the simplestreams query to an exact patch. However, doing so
reintroduces explicit version coupling without a clear benefit, since the local
snap is typically a development or pre-release build where the corresponding
simplestreams entry may not yet exist. Using the newest-available patch in the
`major.minor` track keeps the behavior consistent with the published-binaries
path and avoids a hard failure when an exact match is absent.

The alternatives from the published-binaries path (derive version from snap
metadata, snap store API lookup, bundled tools) apply equally here and are
rejected for the same reasons.

- **Require the user to supply agent binaries alongside the controller snap**:
  When `--controller-snap-path` is provided, also require `--build-agent` or an
  explicit agent tools archive so that the machine agent version is fully
  determined by the user rather than resolved at runtime. This eliminates any
  version ambiguity but significantly increases the burden on the user, who must
  build or obtain matching agent binaries for every supported base and
  architecture. It also conflates two independent concerns — controller snap
  distribution and machine agent tools distribution — that are better kept
  separate. Rejected in favour of the simplestreams newest-patch fallback.

- **Always embed agent binaries inside the locally built snap**: Establish a
  convention that locally built snaps always include the machine agent binaries
  as part of the snap payload. Bootstrap extracts them from the snap rather than
  fetching from simplestreams, making the agent version entirely determined by
  the snap build. Rejected because this would make the local-build bootstrap
  path diverge significantly from the production path — developers would be
  testing a meaningfully different bootstrap flow, reducing confidence that
  local validation reflects production behaviour.

###### Snap assertions and dangerous mode

A snap assertion file (`.assert`) is the store-signed certificate that
establishes the snap's identity, confinement policy, and interface
auto-connections. When the user provides `--controller-snap-path` without a
corresponding `--controller-snap-assert-path`, no assertion is available and the
snap must be installed with `snap install --dangerous`.

Dangerous mode has two significant consequences:

1. **No signature verification**: The snap is installed without cryptographic
   proof of origin. This is acceptable in development and air-gapped scenarios
   but is a deliberate security downgrade in production.

2. **Interface auto-connections are not applied**: Snap interface connections
   that are normally granted automatically via store declarations (e.g. The
focus is implementation behavior from artifact selection through first
controller start, with controller-agent worker design as the primary concern.
`snapd-control`, `lxd`, `system-observe`) are not established in dangerous
mode. The controller snap relies on several of these interfaces to manage
machines and interact with the system. Without them the controller may start
but fail at runtime when it attempts to use those capabilities.

The following options exist:

- **Derive install mode from provided arguments** (selected approach): if
`--controller-snap-assert-path` is provided alongside `--controller-snap-path`,
perform a normal `snap ack` + install. If only `--controller-snap-path` is
given with no assertion file, treat the omission as an explicit opt-in to
dangerous mode and install with `snap install --dangerous`. No separate flag or
config key is needed. The interface limitation is documented as a known
constraint of dangerous mode.

- **Silently fall back to dangerous mode**: Install without assertions whenever
the assert file is absent. Simple, but hides a significant behavioural change
from the user and makes interface failures hard to diagnose.

- **Manually connect required interfaces after install**: After a
dangerous-mode install, emit `snap connect` commands for every interface the
controller snap requires. This partially restores functionality but requires
maintaining a hardcoded list of interfaces that must stay in sync with the
snap's `snapcraft.yaml`, and some privileged interfaces cannot be connected
without store assertions regardless.


#### Command surface

- `--agent-version`
  Deprecated. Machine agent version is resolved automatically from
simplestreams using the current `major.minor` track, selecting the newest
available patch.
- `--build-agent`
  Builds machine/unit `jujud` tools only. Has no effect on how the controller
  snap is installed.
- `--controller-snap-path`
  Path to a locally built controller snap file. When provided, the client
  uploads the snap to the bootstrap instance. If `--controller-snap-assert-path`
  is not also provided, the snap is installed in dangerous mode.
- `--controller-snap-assert-path`
  Path to the snap assertion file for the locally provided snap. When supplied
  alongside `--controller-snap-path`, the snap is installed via `snap ack` +
  `snap install`. Omitting this file is an explicit opt-in to dangerous mode.
- `--controller-snap-channel`
  Snap store channel to install the controller snap from (e.g. `4.0/edge`).
  Used for the published-binaries path when no explicit revision is required.
- `--controller-snap-revision`
  Specific snap store revision to install. Use instead of (or alongside)
  `--controller-snap-channel` when a precise revision is required rather than
  the latest in a channel.
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
