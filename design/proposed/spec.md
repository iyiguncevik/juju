## Abstract

Juju is built and distributed using custom tooling and pipelines that have grown increasingly complex over time, for example through static linking of dependencies, while also falling out of alignment with Canonical best practices. The long-term direction is to move away from simplestreams and static linking toward snaps, rocks, and charms. This is not a trivial change, so a step-by-step approach is required. This specification defines the first step: separating the controller agent from the machine and unit agents, and delivering the controller via snap. Subsequent specifications will define the steps that follow.

## Rationale

The current controller delivery model relies on bespoke tooling and static build constraints that increase operational overhead and slow delivery. It also limits standard upgrade controls expected in Ubuntu environments.

At the company level, this work is needed to:

- align Juju controller lifecycle management with platform-standard snap operations;
- simplify the controller build process, improving developer experience and reducing friction in CI jobs;
- reduce build/distribution complexity by clarifying controller vs machine-agent concerns;

### Problem statement

The current IAAS controller binary distribution has several interrelated problems:

**Musl toolchain and static build complexity.** The build requires musl-gcc and pre-built static C libraries (`dqlite`, `raft`, `libuv`) downloaded into `_deps/` from S3 at build time. This slows CI, complicates developer setup, and ties the binary to a static-only distribution model.

**No binary separation.** Both `jujud` (machine/unit agent) and `jujud-controller` (controller) link against the same packages, including dqlite and domain services. Machine agents carry significant controller-only dependencies unnecessarily.

**Non-standard binary distribution.** Controllers receive binary updates through a bespoke tools-tarball mechanism backed by simplestreams, a metadata discovery system designed for cloud images. There is no standard rollback, hold, or upgrade-gate mechanism.

**HA transition is coupled to binary identity.** When a machine agent promotes to a controller today, it restarts the same binary with a different command. This requires special custom handling of the controller agent in the machine agent, i.e. leaking the logic that should reside in the controller charm into the machine agent.

## Specification

### Goals

Make snap the standard delivery and upgrade mechanism for new IAAS controllers
`jujud` (machine/unit agent) and `jujud-controller` (controller snap) are truly separate binaries; `jujud` no longer links dqlite or domain services.
Preserve functional parity across supported deployment patterns, including HA and airgapped environments.
The musl-gcc toolchain and `_deps/` static C library downloads are removed from the build system.

### Scope

In scope:

- Controllers are bootstrapped using a new controller snap without relying on controller binaries from simplestreams.
- Controllers can be upgraded by upgrading the snap revision; snap auto-updates are disabled so that upgrades only happen when explicitly initiated by the user.
- HA clusters work with charm-managed controller lifecycle: new controller units
  start with `jujud`, then install/start `juju-controllerd` from
  object-store-provided snap artifacts.
- Airgap deployments work via a snap store proxy or via pre-seeded snap and assert blobs in the dqlite object store.

Out of scope:

- Broad removal of simplestreams beyond controller-binary usage. Although the long-term vision is to remove simplestreams entirely for agent binaries, this project focuses on the controller binary path.
- Using snap store to determine upgrade versions. Simplestreams will still be the source of truth for agent version number during upgrade and bootstrap.
- Moving jujud agent binaries away from simplestreams. We'll still rely on simplestreams for `jujud` binary distribution to machine/unit agents. This will be addressed in a subsequent project.
- Support for HA in CAAS controllers.
- Replacing `caas/Dockerfile` with Rockcraft. Rockcraft is Canonical's standard toolchain for OCI images; the `jujud-controller` rock would likely take the `jujud-controller` snap as a build dependency. This will be addressed in a subsequent specification.

## Decisions

**Separate snap repository.** Our release jobs can only handle one snap per repository. The `jujud-controller` snap is published from a new dedicated repository `github.com/juju/jujud-controller-snap`; the `juju` CLI snap remains in `github.com/juju/juju` repository.

**Snap and OCI must use the same binary.** The `jujud-controller` binary embedded in the snap and in the CAAS OCI image must have an identical SHA256 hash. This can be achieved in CI build either by extracting the controller binary from snap during or by downloading the same pre-built binary from S3.

**Binary distribution via S3.** The main repository CI builds `jujud-controller` and uploads it to S3. The snap repository and the OCI build both download and package that same binary.

**`snap install --dangerous` is a dev-only tool.** It is acceptable in Stage 1 for local developer workflow. The Stage 2 production path uses `snap download` → `snap ack` → `snap install ./` from the snap store.

**Auto-refresh is held.** Snap upgrades are explicitly controlled by Juju's
controller-upgrade workflow (client orchestration plus charm-managed snap
lifecycle), not by snapd auto-refresh.

**Assert files are stored in the object store.** The `.assert` file produced by `snap download` is stored alongside the `.snap` blob in the dqlite object store, ensuring both are available to all controller units via raft replication.

**Upgrades go through Juju workflow + object store.** During
`juju upgrade-controller`, the controller charm resolves and downloads the
target snap revision, `juju-controllerd` stores snap+assert in the dqlite
object store, and the charm performs install/restart on controller units.

**CAAS is unaffected by the snap path.** CAAS controllers run in OCI containers and do not use the snap install/upgrade flow.

**musl and `_deps` removed late.** The musl-gcc toolchain and the `_deps/` pre-built static C libraries downloaded from S3 at build time are removed in the final phase, after the snap path is fully the default. The S3 release artifacts ( built binaries: `jujud-controller`, `jujud`, `jujuc`, etc.) are **kept** — they are used by `juju-release-jenkins` jobs.

**Binary separation is a prerequisite.** The target workflow assumes
controller machines run machine-agent and controller services as separate
processes (`jujud` + `juju-controllerd`) from the beginning of the rollout.
`jujud` remains distributed through simplestreams and excludes
controller-specific services/dependencies.

## Implementation stages

### Stage 1 — Runtime Topology + Dev Bootstrap

This stage establishes the agreed runtime topology in development workflows:
controller machines run `jujud` and `juju-controllerd` concurrently, with
controller lifecycle owned by the controller snap/charm path. Cloud-init can
bootstrap the initial controller with both binaries and the bootstrap worker
stores required snap/assert/tools artifacts in object store for later reuse.

### Stage 2 — IaaS Production Bootstrap Parity

This stage makes bootstrap production-ready across production, development, and
airgapped environments while preserving one workflow contract. Artifact sources
vary by environment (store/proxy/local), but runtime topology remains the same.
Simplestreams remains in scope for target-version resolution and for `jujud`
distribution.

### Stage 3 — HA Scale-out via Charm Workflow

This stage enables controller HA scale-out using the agreed charm-centered flow.
New controller units start with `jujud`, then the controller charm installs
`juju-controllerd` from object-store-provided snap/assert and starts it.
Role-switch transition logic in `jujud` remains out of scope because the target
model is process coexistence, not binary/mode replacement.

### Stage 4 — Controller Upgrade Orchestration

This stage finalizes the agreed upgrade workflow: `juju upgrade-controller`
orchestrates controller upgrade first, charm logic resolves/downloads target
snap revision, `juju-controllerd` stores snap/assert in object store, and charm
applies snap install/restart on controller units. Model upgrade continues
through existing flow afterward.

### Stage 5 — Flag Default ON & Full Integration Tests

This stage makes the snap path the default for new IaaS controllers and
validates it through full end-to-end testing. Operator guidance for bootstrap,
upgrade, HA, and airgap workflows is finalized. Although controller snap is used
by default, the feature flag still allows falling back to legacy distribution.

### Stage 6 — Legacy Removal

This stage removes the legacy controller distribution path so snap becomes the
only controller delivery and upgrade mechanism. Transitional flags and old
build/distribution steps are retired. Replacing machine/unit agent distribution
through simplestreams remains out of scope for this project.

## Risks

- In juju we never tested the dynamic linking of the dq-lite and raft libraries,
  so there is a risk that removing the musl-gcc toolchain can take longer than
  expected. As this happens at the last stage, it should not impact the rest of the
  deliverables.
- During HA scale-out, failure handling for controller snap installation on a
  candidate controller unit needs careful design. Retry/recovery paths must
  avoid leaving the unit in a partially converged state.

## Affected Code Areas

This section describes the major code areas that will be modified during the
snap migration. Understanding these areas helps with planning and executing the
work.

**Snap Build Infrastructure:** The snap build system needs to move to a
separate repository because Launchpad can only publish one snap per repository.
The new `github.com/juju/jujud-controller-snap` repo will contain the snapcraft
definition, CI workflows for building and publishing to the snap store, and
logic to download the pre-built `jujud-controller` binary from S3 during snap
build. This separation allows the main repo to continue building the CLI snap
on Launchpad while the controller snap is built via GitHub Actions.

**Feature Flag System:** A new `ControllerSnap` feature flag gates all
snap-specific runtime behavior throughout the codebase. This allows the snap
path to be developed, tested, and refined while keeping the existing
tools.tar.gz path as the default. The flag is checked at key decision points:
bootstrap, upgrade, HA operations, and binary distribution. Once the snap path
is proven stable, the flag default flips to true, and eventually the flag is
removed entirely along with the legacy code paths.

**Cloud-Init and Bootstrap:** The cloud-init configuration generates shell
scripts that run on newly created controller machines to install and configure
the agent. Currently it downloads and extracts tools.tar.gz archives. With the
snap path, it needs to support three modes: snap download from the store
(production), snap install from local file (development), and snap install via
snap proxy (airgap). The bootstrap process also needs to store the snap and
assert files in the dqlite object store for HA replication.

**Agent Binary Storage Domain:** The agent binary domain manages metadata and
storage of agent binaries in the controller's dqlite database and object store.
This includes DDL schema, domain services, and repository interfaces. The
schema needs to be extended to track snap revisions, assert file paths, and
binary hashes. New service methods handle storing and retrieving snap+assert
blobs from the object store, which is replicated across all HA controller units
via dqlite raft.

**Bootstrap Worker:** The bootstrap worker orchestrates the controller
bootstrap sequence, including uploading the initial agent binary to the object
store. It needs a new code path to handle snap+assert files instead of
tools.tar.gz archives. The worker detects whether the snap flag is enabled and
dispatches to either the legacy PopulateAgentBinary function or the new
PopulateSnapAgentBinary function. This keeps both paths functional during the
transition period.

**Upgrader Worker:** The upgrader worker runs on every agent and watches for
new versions, downloads binaries, and triggers restarts. For controllers using
the snap path, it needs to download snap+assert files from the object store
(via API endpoints), run snap ack and snap install commands, and apply snap
refresh hold to prevent automatic upgrades. The upgrade coordination between
multiple HA controller units remains unchanged since the object store already
handles replication.

**API Binary Endpoints:** The controller API serves agent binaries to machines
via HTTP endpoints in apiserver/tools.go. New endpoints are needed to serve
snap and assert files separately from the object store. These endpoints
authenticate requests, read blobs from the object store, and stream them to
clients. The existing tools.tar.gz endpoints remain for machine and unit
agents, while controller agents use the new snap endpoints when the flag is
enabled.

**Simplestreams Binary Discovery:** Simplestreams is used to discover and
download agent binaries from public mirrors when the controller doesn't have
them in its object store. Within this project, the goal is to remove the
**controller** (`jujud-controller`) binary from simplestreams — controllers will
be distributed exclusively via snap. The machine/unit agent (`jujud`) binary
remains in simplestreams and is out of scope for this project. A future project
may address full simplestreams removal for agent binaries, but the replacement
distribution mechanism for machine/unit agents is an open question.

**Binary Separation:** Currently both `jujud` and `jujud-controller` link
against all packages including dqlite and domain services. True separation
means refactoring cmd/jujud to exclude controller-only imports, updating
Makefile build tags and linkage, and stopping the renaming of jujud-controller to
jujud. Machine agents will run a smaller jujud binary without dqlite
dependencies. In the target workflow, controller machines run both `jujud` and
`juju-controllerd` concurrently rather than switching one binary between machine
and controller modes.

**HA Snap Installation Logic:** During HA scale-out, new units start with
`jujud`; the controller charm then installs the controller snap from
object-store-provided snap+assert artifacts, writes configuration for
`juju-controllerd`, and starts it. This flow requires robust error handling for
installation/convergence failures so units do not remain partially converged.

**Makefile and Build System:** The Makefile currently uses musl-gcc for static
linking and downloads pre-built dqlite/raft/libuv static libraries into the
`_deps/` folder from S3 at build time to speed up compilation. During the
transition, both the static binary (for legacy path) and dynamic binary (for
snap) need to build. Eventually the musl toolchain and the `_deps/` S3
downloads are removed, switching to apt-provided dynamic libraries. Note: the
S3 release artifacts (final built binaries: `jujud-controller`, `jujud`, `jujuc`,
etc.) are distinct from the build-time static libs — the release artifacts
remain in S3 and continue to be used by `juju-release-jenkins` jobs. The
Makefile also needs to stop renaming jujud-controller to jujud, allowing both
binaries to coexist with their proper names.

**CI/CD Binary Distribution:** The juju-release-jenkins jobs orchestrate
building, testing, and publishing releases. These jobs need to handle building
two separate binaries (jujud and jujud-controller), uploading both to S3 with
distinct names and paths, updating simplestreams metadata to include both, and
coordinating with the snap repository to ensure the snap build downloads the
correct controller binary. Hash verification checks ensure the snap and OCI
images use identical controller binaries.

**OCI Image Build:** The CAAS controller runs in Kubernetes as an
OCI container image, currently built via a Dockerfile that copies pre-built
binaries from the local build directory. The Dockerfile will be updated to
reference the renamed `jujud-controller` binary and download it from S3,
ensuring the OCI image and the snap use the exact same binary with identical
SHA256 hash. A future migration to Rockcraft (Canonical's OCI build tool) is
an open question.

---

## Open Questions

**How are controller/0 and non-controller/0 bootstrap paths represented in charm
logic?** The contract is agreed at a workflow level, but the exact relation
data and state signaling details still need to be finalized.

**Upgrade target-resolution policy:** The high-level upgrade workflow is agreed,
but target-version resolution policy still needs detail (simplestreams source,
build-agent behavior, and user-specified version handling).

**How are assert files handled in fully airgapped upgrades?** When there is no
snap proxy, the admin must supply snap and assert via
`juju upgrade-controller --controller-snap`. The exact workflow needs
documenting.

**What happens if snap install fails during HA scale-out convergence?** Retry
logic, error handling, and rollback strategy must be designed. A failed
convergence must not leave the machine in an inconsistent state.

**Should `caas/Dockerfile` be replaced with Rockcraft?** Rockcraft is
Canonical's standard toolchain for OCI images. The Dockerfile is simpler to
maintain short-term but diverges from the direction of the wider platform team.

**How to eliminate simplestreams for machine/unit agent binaries?** This is out
of scope for this project but needs a dedicated decision. Options include
bundling agent binaries in the controller snap, a separate snap, or charm
payload.

## Further Information
