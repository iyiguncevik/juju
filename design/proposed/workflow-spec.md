## Abstract

In the controller-as-a-snap initiative, controller responsibilities are separated from machine/unit responsibilities at both worker and binary levels, so controller machines run both `jujud` and `juju-controllerd` instead of a single combined agent process. This removes the current bootstrap role-transition "dance" where one agent mode is stopped and another is started, and it enables controller-agent lifecycle control through the controller charm. During bootstrap, however, we still need an initial API server before the machine agent can create and manage charms, so the startup sequence must explicitly account for that chicken-and-egg constraint.

## Rationale

This document exists to align the team on the three workflows that matter for this initiative: bootstrap, controller HA scale-out, and controller upgrade. Its purpose is to define those technical workflows before implementation starts, without mixing in project planning, organization details, or delivery process.

Compared to the current system, the target state in this document moves controller-specific behavior away from generic agent paths and into controller-charm-managed controller workflows. That gives clearer ownership boundaries between `jujud` (machine/unit agent responsibilities) and `juju-controllerd` (controller responsibilities).

This is intentionally not the end-state architecture for Juju as a whole. It is a scoped target for the current project: agree concrete workflow contracts, reduce cross-component ambiguity, and keep the initiative focused enough to deliver meaningful outcomes in a reasonable timeframe.

## Specification

In this target workflow, controller machines run `jujud` and `juju-controllerd` concurrently. `jujud` keeps machine/unit-agent responsibilities, while `juju-controllerd` runs controller-specific services and workers. This explicit coexistence replaces the current bootstrap role-switch pattern and makes controller lifecycle ownership through the controller charm unambiguous.

### Components

#### `juju-controllerd` (snap payload)

- Installed as the `juju-controllerd` snap on controller machines.
- Runs controller-specific services and workers.
- Does not run machine/unit-agent responsibilities.

#### `jujud` (machine/unit agent binary)

- Continues to be distributed through existing simplestreams mechanisms.
- Runs on controller machines to provide machine-agent responsibilities.
- Does not include controller-specific bootstrap logic, API server, dq-lite dependency.

#### Controller charm

- Declares and manages controller snap lifecycle as part of controller application operations.
- Ensures the correct `juju-controllerd` snap revision is installed and running on controller units.
- Coordinates controller-unit lifecycle behavior via normal charm operations.

#### Cloud-init bootstrap scripts

- Initialize controller machine bootstrap steps.
- Start baseline agent processes required to reach controller charm management.
- Apply environment-specific artifact acquisition flow (store/proxy/local).

### Workflow: bootstrap (initial controller)

1. Juju client provisions the controller machine and renders cloud-init configuration.
2. Cloud-init acquires and starts both `juju-controllerd` and `jujud`:
   - Production:
     - the target version is resolved from simplestreams;
     - `juju-controllerd` is downloaded from the snap store;
     - `jujud` is downloaded from simplestreams.
   - Development:
     - both the `juju-controllerd` snap and `jujud` are provided by Juju client.
   - Airgapped:
     - `juju-controllerd` is downloaded from the snap proxy;
     - `jujud` is downloaded through the simplestreams proxy.
3. Cloud-init waits until `juju-controllerd` has started successfully.
4. The `juju-controllerd` bootstrap worker triggers deployment of the controller charm.
5. The `juju-controllerd` bootstrap worker stores the snap and assert in object store.
6. The `juju-controllerd` bootstrap worker stores tools in the object store.
7. The controller charm verifies peer relations and confirms that the `juju-controllerd` snap version matches the charm version.

Notes:

- `juju-controllerd` is snap-managed from first successful bootstrap.
- `jujud` runs as machine-agent along with `juju-controllerd` and is not replaced by the snap.
- The controller charm understand the bootstrap process for the `controller/0` is different than the others.

### Controller scale process (HA)
1. Juju initiates controller scale-out with `juju add-unit` command.
2. `juju-controllerd` provisions a new machine and configures cloud-init.
3. Cloud-init starts `jujud` on the new machine.
4. `jujud` (via the uniter) determines that it must deploy the controller charm.
5. The charm determines that this unit is **not** `controller/0`, so downloads the controller snap from the object store.
6. The charm installs the snap, writes the agent configuration for `juju-controllerd`, and starts `juju-controllerd`.

### Controller upgrade process
1. Juju initiates the upgrade with `juju upgrade-controller`, which internally triggers a controller charm refresh as part of the workflow.
2. The controller charm downloads the target snap revision.
3. `juju-controllerd` stores the snap and assert in object store.
4. The controller charm installs the snap and restarts `juju-controllerd`.
5. After `juju-controllerd` is upgraded and restarted, the Juju client starts controller model upgrade using the existing model-upgrade flow.

Notes:
- Model upgrade process is not changed in this project, so details are not included here.
- Upgrade of the controller and the controller model are decoupled. Juju client triggers controller upgrade first, then triggers model upgrade after controller is upgraded and restarted. This way, we keep the two upgrade processes independent and avoid coupling model upgrade with controller upgrade. However, this also means Juju client needs to orchestrate the two upgrade processes together to keep them in sync.

## Decisions

**Service ownership split.** `juju-controllerd` carries controller services; `jujud` remains machine-agent-only. Workflow definitions must preserve this boundary.

**Client-Side Orchestration for UX Consistency**: While the controller and model upgrades are technically decoupled to prevent process interference, the Juju client will orchestrate both sequentially. This avoids forcing users to manually trigger two separate upgrades—preserving the existing "single-command" experience while benefiting from the stability of independent execution phases.

**Charm-centered lifecycle control**: After initial cloud-init bootstrap, steady state controller snap lifecycle (convergence and upgrades) is controlled by Juju workflows (charm/worker), not by jujud or juju-controllerd.

**Environment parity principle**: Production, development, and airgapped flows may differ in artifact source, but they must converge to the same runtime topology and upgrade authority model.

**Required Proxy Services**: Standardize on a dual-proxy configuration for air-gapped environments. Deployments in these environments must provide both a Snap Proxy and a SimpleStreams Proxy to facilitate secure, internal access to all necessary resources.

## Open Questions

1. During HA scale-out, what is the exact failure-handling policy when snap installation fails on a candidate controller unit?
2. How is the target version determined during upgrade? Does charm finds the target version from simplestreams? How about the build-agent case? How about the user-specified version case?

## Further Information
