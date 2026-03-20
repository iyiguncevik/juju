## Abstract

In the controller-as-a-snap initiative, controller responsibilities are separated
from machine/unit responsibilities at both worker and binary levels, so
controller machines run both `jujud` and `jujud-controller` instead of a single
combined agent process. This removes the current bootstrap role-transition
"dance" where one agent mode is stopped and another is started, and it enables
controller-agent lifecycle control through the controller charm. During
bootstrap, however, we still need an initial API server before the machine agent
can create and manage charms, so the startup sequence must explicitly account
for that chicken-and-egg constraint.

## Rationale

This document exists to align the team on the three workflows that matter for
this initiative: bootstrap, controller HA scale-out, and controller upgrade.
Its purpose is to define those technical workflows before implementation starts,
without mixing in project planning, organization details, or delivery process.

Compared to the current system, the target state in this document moves
controller-specific behavior away from generic agent paths and into
controller-charm-managed controller workflows. That gives clearer ownership
boundaries between `jujud` (machine/unit agent responsibilities) and
`jujud-controller` (controller responsibilities).

This is intentionally not the end-state architecture for Juju as a whole. It is
a scoped target for the current project: agree concrete workflow contracts,
reduce cross-component ambiguity, and keep the initiative focused enough to
deliver meaningful outcomes in a reasonable timeframe.

## Specification

In this target workflow, controller machines run `jujud` and `jujud-controller`
concurrently. `jujud` keeps machine/unit-agent responsibilities, while
`jujud-controller` runs controller-specific services and workers. This explicit
coexistence replaces the current bootstrap role-switch pattern and makes
controller lifecycle ownership through the controller charm unambiguous.

### Components

#### `jujud-controller` (snap payload)

- Installed as the `jujud-controller` snap on controller machines.
- Runs controller-specific services and workers.
- Does not run machine/unit-agent responsibilities.

#### `jujud` (machine/unit agent binary)

- Continues to be distributed through existing machine-agent mechanisms.
- Runs on controller machines to provide machine-agent responsibilities.
- Does not include controller-specific bootstrap or controller-only business
  logic.

#### Controller charm

- Declares and manages controller snap lifecycle as part of controller
  application operations.
- Ensures the correct `jujud-controller` snap revision is installed and running
  on controller units.
- Coordinates controller-unit lifecycle behavior via normal charm operations.

#### Cloud-init bootstrap scripts

- Initialize controller machine bootstrap steps.
- Start baseline agent processes required to reach controller charm management.
- Apply environment-specific artifact acquisition flow (store/proxy/local).

### Workflow: bootstrap (initial controller)

1. juju creates the machines and configures cloud-init
2. cloud-init starts juju-controllerd and jujud
- Production:
  - version is retrieved from simplestreams
  - juju-controllerd is downloaded from snap store
  - jujud is downloaded from simplestreams
- Development:
    - Both juju-controllerd snap and jujud is uplaoded from juju
- Airgapped:
    - juju-controllerd is downloaded from snap proxy
    - jujud is downloaded via simplestreams proxy
3. cloud-init waits for juju-controllerd to be started
4. jujud-controllerd (bootstrap worker) triggers deployment of controller charm
5. jujud-controllerd (bootstrap worker) stores snap+assert as charm resource
6. jujud-controllerd (bootstrap worker) stores tools in object store
7. juju controller charm checks its peer relations and verifies version of juju-controllerd snap and charm are same

Notes:

- `jujud-controller` is snap-managed from first successful bootstrap.
- `jujud` remains a machine-agent binary and is not replaced by the snap.

### Controller scale process (HA)
1. juju initiates scale with add-unit
2. juju-controllerd creates a new machine and configures cloud-init
3. cloud-init starts jujud
4. jujud (uniter) understands it needs to deploy controller charm
7. charm understand it's not controller/0 so downloads snap from objectstore
8. charm installs the snap, writes agent config file for juju-controllerd and starts juju-controllerd 

### Controller upgrade process
1. juju initiates upgrade with upgrade-controller but calls refresh charm underneath
2. juju controller charm downloads the latest snap
3. juju-controllerd stores the new version of snap in objectstore
4. juju controller charm installs the snap and triggers restart of juju-controllerd
5. juju client initiates upgrade of the controller model (the old model upgrade flow) 

## Decisions

When controller is upgraded, controller model is also upgraded. As we have controller and agent separate, they can be upgraded independently, but we want to keep them in sync to avoid confusion and mismatch between the two. So we will trigger controller model upgrade as part of controller upgrade process.

Controller model upgrade will be triggered by juju client after juju-controllerd is upgraded and restarted. This way, model upgrade can be decoupled from controller agent and controller charm.

**Service ownership split.** `jujud-controller` carries controller services;
`jujud` remains machine-agent-only. Workflow definitions must preserve this
boundary.

**Charm-centered lifecycle control.** After initial cloud-init bootstrap, steady
state controller snap lifecycle (convergence and upgrades) is controlled by Juju
workflows (charm/worker), not by ad-hoc host automation.

**Environment parity principle.** Production, development, and airgapped flows
may differ in artifact source, but they must converge to the same runtime
topology and upgrade authority model.

## Open Questions

1. During HA scale-out, what is the exact failure-handling policy when snap
   installation fails on a candidate controller unit?


## Further Information
