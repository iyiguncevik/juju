## Abstract

Juju is built and distributed using custom tooling and pipelines that have grown
increasingly complex over time, for example through static linking of
dependencies, while also falling out of alignment with Canonical best practices.
The long-term direction is to move away from simplestreams and static linking
toward snaps, rocks, and charms. This is not a trivial change, so a step-by-step
approach is required. This specification defines the first step: separating the
controller agent from the machine and unit agents, and delivering the controller
via snap. Subsequent specifications will define the steps that follow.

## Rationale

The current controller delivery model relies on bespoke tooling and static build
constraints that increase operational overhead and slow delivery. It also limits
standard upgrade controls expected in Ubuntu environments.

At the company level, this work is needed to:
align Juju controller lifecycle management with platform-standard snap
operations; simplify the controller build process, improving developer
experience and reducing friction in CI jobs; reduce build/distribution
complexity by clarifying controller vs machine-agent concerns;

## Specification

### Problem statement

The current IAAS controller binary distribution has several interrelated
problems:

- Musl toolchain and static build complexity. The build requires musl-gcc and
  pre-built static C libraries (dqlite, raft,libuv) downloaded into _deps/ from
  S3 at build time. This complicates developer setup, diverges development build
  from CI builds and ties the binary to a static-only distribution model.
- No binary separation. Both jujud (machine/unit agent) and jujud-controller (
  controller) link against the same packages, including dqlite and domain
  services. Machine agents carry significant controller-only dependencies
  unnecessarily.
- Non-standard binary distribution. Controllers receive binary updates through a
  bespoke tools-tarball mechanism backed by simplestreams, a metadata discovery
  system designed for cloud images. There is no standard rollback, hold, or
  upgrade-gate mechanism.
- HA transition is coupled to binary identity. When a machine agent promotes to
  a controller today, it restarts the same binary with a different
  configuration. This requires special custom handling of the controller agent
  in the machine agent, i.e. leaking the logic that should reside in the
  controller charm into the machine agent.

### Goals

- Make snap the standard delivery and upgrade mechanism for new IAAS controllers
  agents
- jujud (machine/unit agent) and jujud-controller (controller snap) are truly
  separate binaries; jujud no longer links dqlite or API server.
- Preserve functional parity across supported deployment patterns, including HA
  and airgapped environments.
- The musl-gcc toolchain and _deps/ static C library downloads are removed from
  the build system.

### Scope

#### In scope:

- Controllers are bootstrapped using a new controller snap without relying on
  binaries from simplestreams.
- Controllers can be upgraded by upgrading the snap revision; snap auto-updates
  are disabled so that upgrades only happen when explicitly initiated by the
  user.
- HA clusters work: machine agents detect the controller role, install the snap,
  and transition cleanly to `jujud-controller`.
- Airgap deployments work via a snap store proxy or via pre-seeded snap and
  assert blobs in the object store.

#### Out of scope:

- Broad removal of simplestreams beyond controller-binary usage. Although the
  long-term vision is to remove simplestreams entirely for agent binaries, this
  project focuses on the controller binary path.
- Using snap store to determine upgrade versions. Simplestreams will still be
  the source of truth for agent version number during upgrade and bootstrap.
- Support for HA in CAAS controllers.
- Replacing caas/Dockerfile with Rockcraft. Rockcraft is Canonical's standard
  toolchain for OCI images; the jujud-controller rock would likely take the
  jujud-controller snap as a build dependency. This will be addressed in a
  subsequent specification.

### Decisions

- Functional requirements are covered in JU179 - Controller Snap Functional
  Requirements
- Snap and OCI must use the same binary. The juju-controllerd binary embedded in
  the snap and in the CAAS OCI image must have an identical SHA256 hash. This
  can be achieved in CI build either by extracting the controller binary from
  snap during or by downloading the same pre-built binary from S3.
- Auto-refresh is held. The upgrader worker controls all snap upgrades
  explicitly.
- Assert files are stored in the object store. The .assert file produced by snap
  download is stored alongside the .snap blob in the object store, ensuring both
  are available to all controller units via raft replication.
- HA scale-out goes through the object store. Juju controller snap and assert
  file will be downloaded from object store and installed in the new controller
  units.
- Upgrades are coordinated by charm. The controller charm will download and
  install the new revision of the controller snap. The machine agent will not
  have any controller upgrade specific logic.
- CAAS is unaffected by the snap path. CAAS controllers run in OCI containers
  and do not use the snap install/upgrade flow.
- musl and _deps removed late. The musl-gcc toolchain and the _deps/ pre-built
  static C libraries downloaded from S3 at build time are removed in the final
  phase, after the snap path is fully the default. The S3 release artifacts (
  built binaries: jujud-controller, jujud, jujuc, etc.) are kept — they are used
  by juju-release-jenkins jobs.
- Binary separation is delayed. Binary separation is deferred until the last
  stage to avoid a transition period where two separate binaries must be
  maintained in simplestreams before snap takes over. Once the build and
  distribution of the controller binary is fully decoupled from the machine/unit
  agent binary, we can create a separate jujud-controller binary and refactor
  the agent binaries to exclude dqlite and api server dependencies.
- Feature Flag: A new ControllerSnap feature flag gates all snap-specific
  runtime behavior throughout the codebase. This allows the snap path to be
  developed, tested, and refined while keeping the existing tools.tar.gz path as
  the default. The flag is checked at key decision points: bootstrap, upgrade,
  HA operations, and binary distribution. Once the snap path is proven stable,
  the flag default flips to true, and eventually the flag is removed entirely
  along with the legacy code paths.

### Implementation stages

Stage 1 — Bootstrap This stage delivers controller bootstrap on the new path:
feature-flagged rollout, controller/machine agent coexistence, dedicated
controller snap adoption, production/dev/airgap source-resolution behavior, and
machine/controller binary split. Stage 2 — HA and controller upgrade This stage
establishes HA scale-out charm-driven for non-`controller/0` units with required
checks, and controller upgrade follows explicit snap-distribution sequencing.
Stage 3 — Release and clean up This stage finalizes rollout by making the new
path default, removing feature-flag branching, then performing post-flag
machine-binary cleanup (jujud without controller-only API server/dqlite
dependencies).

### Risks

- In juju we never tested the dynamic linking of the dqlite and raft libraries,
  so there is a risk that removing the musl-gcc toolchain can take longer than
  expected. As this happens at the last stage, it should not impact the rest of
  the deliverables.
- Building a Snap is a more resource-intensive operation than simply compiling a
  binary, which can slow down the local development experience. Potential
  workarounds exist but require further investigation and testing.

### Affected Code Areas

This section describes the major code areas that will be modified during the
snap migration. Understanding these areas helps with planning and executing the
work.

**Snap Build Infrastructure**: Because the current build system is limited to
one snap per repository, we must decide between creating a standalone repository
for the controller snap or employing a workaround. A separate repository would
allow the main repo to maintain its CLI snap on Launchpad while the controller
snap utilizes GitHub Actions and S3-hosted binaries. A final decision on which
approach to take will be made later in a different specification.

**Cloud-Init and Bootstrap**: The cloud-init configuration generates shell
scripts that run on newly created controller machines to install and configure
the agent. Currently it downloads and extracts tools.tar.gz archives. With the
snap path, it needs to support two modes: snap download from the store (
production), snap install from local file (development). For airgapped
installations snap proxy can be used as well.

**Agent Binary Storage Domain**: The agent binary domain manages metadata and
storage of agent binaries in the controller's dqlite database and object store.
This includes DDL schema, domain services, and repository interfaces. The schema
needs to be extended to track snap revisions, assert file paths, and binary
hashes. New service methods handle storing and retrieving snap+assert blobs from
the object store, which is replicated across all HA controller units via dqlite
raft.

**Bootstrap Worker**: The bootstrap worker orchestrates the controller bootstrap
sequence, including uploading the initial agent binary to the object store. It
needs a new code path to handle snap+assert files along with tools.tar.gz
archives. The worker detects whether the snap flag is enabled and dispatches to
either the legacy PopulateAgentBinary function or the new
PopulateSnapAgentBinary function. This keeps both paths functional during the
transition period.

**Upgrader Worker**: The upgrader worker runs on every agent and watches for new
versions, downloads binaries, and triggers restarts. For controllers using the
snap path, snap and assert files also need to be downloaded. Moreover, some of
this functionality will be moved from the upgrader worker to the controller
charm.

**API Binary Endpoints**: The controller API serves agent binaries to machines
via HTTP endpoints in apiserver/tools.go. New endpoints are needed to serve snap
and assert files separately from the object store. These endpoints authenticate
requests, read blobs from the object store, and stream them to clients. The
existing tools.tar.gz endpoints remain for machine and unit agents, while
controller agents use the new snap endpoints when the flag is enabled.

**Simplestreams Binary Discovery**: Simplestreams is used to discover and
download agent binaries from public mirrors when the controller doesn't have
them in its object store. Within this project, the goal is to remove the
controller (jujud-controller) binary from simplestreams — controllers will be
distributed exclusively via snap. The machine/unit agent (jujud) binary remains
in simplestreams and is out of scope for this project. A future project may
address full simplestreams removal for agent binaries, but the replacement
distribution mechanism for machine/unit agents is an open question.

**Binary Separation**: Currently both jujud and jujud-controller link against
all packages including dqlite and API server. True separation means separation
of concerns, i.e. the juju-controller will not have machine workers. Machine
agents will run a smaller jujud binary without dqlite dependencies.

**HA Snap Installation Logic**: In the HA scale-out workflow, juju add-unit
creates a new controller candidate machine through juju-controllerd, and
cloud-init starts jujud on that machine. jujud (via the uniter) determines that
the controller charm must be deployed. For units other than controller/0, the
charm downloads the controller snap from object store, installs it, writes the
juju-controllerd agent configuration, and starts juju-controllerd.

**Makefile and Build System**: The Makefile currently uses musl-gcc for static
linking and downloads pre-built dqlite/raft/libuv static libraries into the _
deps/ folder from S3 at build time to speed up compilation. During the
transition, both the static binary (for legacy path) and dynamic binary (for
snap) need to be built. Eventually the musl toolchain and the _deps/ S3
downloads are removed, switching to system provided dynamic libraries. Note:
the S3 release artifacts (final built binaries: jujud-controller, jujud, jujuc,
etc.) are distinct from the build-time static libs — the release artifacts
remain in S3 and continue to be used by juju-release-jenkins jobs. The Makefile
also needs to stop renaming jujud-controller to jujud, allowing both binaries to
coexist with their proper names.

**CI/CD Binary Distribution**: The juju-release-jenkins jobs orchestrate
building, testing, and publishing releases. These jobs need to handle building
two separate binaries (jujud and jujud-controller), uploading both to S3 with
distinct names and paths, updating simplestreams metadata to include both, and
coordinating with the snap repository to ensure the snap build downloads the
correct controller binary. Hash verification checks ensure the snap and OCI
images use identical controller binaries.

**OCI Image Build**: The CAAS controller runs in Kubernetes as an OCI container
image, currently built via a Dockerfile that copies pre-built binaries from the
local build directory. The Dockerfile will be updated to reference the renamed
jujud-controller binary and download it from S3, ensuring the OCI image and the
snap use the exact same binary with identical SHA256 hash. A future migration to
Rockcraft (Canonical's OCI build tool) is an open question.

### Open Questions

How to eliminate simplestreams for machine/unit agent binaries? This is out of
scope for this project but needs a dedicated decision. Options include bundling
agent binaries in the controller snap, a separate snap, or charm payload.