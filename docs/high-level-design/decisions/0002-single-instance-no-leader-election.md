# ADR 0002: One container, one process, no leader election

**Status.** Accepted.

**Reader's prerequisites.** Read [../overview.md](../overview.md) and `../../architecture.md` (sections "High-Level Topology", "Backpressure and overload behavior", "Concurrency inside the service", "Out of Scope").

## Context

A single deployment of `fhir-subscriptions-foss` is **single-tenant** — one container per facility, one EHR. The architecture's first stated constraint is operational simplicity: "A deployment is one container plus Postgres. No leader election, no replica coordination, no cross-service choreography. If the service falls behind, it catches up from durable state."

Common reflexes for production systems include running multiple replicas behind a load balancer with a coordination layer (leader election via etcd / Consul / Postgres advisory locks, distributed locks for shared work, replicated in-memory state). Those reflexes are appropriate when:

- the workload is read-heavy and benefits from horizontal read scaling,
- the workload requires sub-second failover that exceeds a single instance's restart time,
- the deployment terminates user-facing traffic (a load balancer's job is unambiguous),
- the work itself fans out faster than a single instance can absorb.

None of those apply to this server's workload at the scale a single facility produces. The architecture's words on backpressure: "this is near impossible to outrun from any reasonable EHR running on a decently sized node, because the cost per [HL7] message is one row insert."

The cost of multi-instance deployment, on the other hand, is a long list of bugs that only exist *because* of multi-instance coordination:

- **Split-brain.** Two instances both believing they are leader; both writing partly-finished state.
- **Stale caches.** An instance holding old subscription state for some time after another instance updates it; deliveries miss the update.
- **Race conditions on subscription update mid-delivery.** One instance is mid-batch on a subscription whose `filterBy` is being changed by another instance; the in-flight batch reflects the old filter and the new instance starts processing under the new filter.
- **Distributed-lock holders dying with the lock held.** Lock TTLs trade safety for liveness; getting the trade-off wrong loses work or duplicates it.
- **Cross-instance ordering.** `eventsSinceSubscriptionStart` is monotonic per subscription; ensuring it is monotonic across instances requires coordinated state.

Removing the coordination removes the bugs.

## Decision

A deployment is **one container, one process, no leader election, no replica coordination**.

- The MLLP listener listens on its bound ports, the adapter sub-components run in goroutines / fibers / threads in the same process, the engine runs in the same process, the channels run in the same process.
- Concurrency inside the process uses `SELECT FOR UPDATE SKIP LOCKED` so multiple worker fibers can claim distinct rows without external coordination. This is intra-process concurrency, not inter-instance coordination.
- Postgres is the durability seam. A crash recovers from durable rows.
- High-availability — if a deployment requires it — comes from running the container in a managed scheduler (Kubernetes, ECS, Nomad) that restarts the failed pod. Restart from durable state is acceptable per the workload (HL7 v2 protocol holds messages on the EHR side; FHIR-side subscribers use `$events` to catch up).

The architecture's "Out of Scope" section is explicit: "Multi-instance coordination, leader election, distributed locks" are not part of the design.

## Consequences

### Positive

- **No coordination bugs.** Every class of bug listed above is not just rare in this architecture — it is structurally impossible. A bug that requires two instances to manifest cannot manifest in a system that has only one instance.
- **Simpler operations.** No etcd / Consul / Zookeeper to run. No advisory-lock cleanup on instance death. No "which instance has the lock right now" investigations.
- **Simpler reasoning.** A single process with worker fibers reading durable rows is straightforward to reason about; a multi-replica system with a leader-election layer is an order of magnitude more complex.
- **Subscription cursor monotonicity is trivial.** `eventsSinceSubscriptionStart` is assigned by the one process that handles the subscription. There are no cross-instance ordering questions.
- **Restart semantics are durable.** A restart re-claims pending rows from the input tables. Crash mid-row leaves the row unclaimed (`SKIP LOCKED` semantics let the next worker fiber pick it up).

### Negative

- **Restart latency is recovery latency.** When the process restarts, no work happens during the restart. For typical container schedulers this is seconds; for the FHIR Subscriptions workload (where the spec already says delivery is best-effort and `$events` covers catch-up) seconds of delay is acceptable. Operators that need stricter RTO must deploy a hot-standby pattern that is outside this design.
- **Vertical scaling only.** A facility that genuinely outgrows one container has to scale up (more CPU, more memory) rather than out. Given that the cost per HL7 message is one row insert and the cost per delivery is a single HTTPS POST, a single decently-sized container handles a lot of facility traffic. We accept this trade.
- **No multi-region active-active.** If a deployment needs cross-region resilience, it is run in one region with a backup region failover (different deployment topology), not active-active. Same trade: complexity vs. workload.
- **MLLP listener restart is visible to the EHR.** During a restart, the listener is not accepting new connections; the EHR's interface engine queues messages and re-sends. Per the HL7 v2 protocol this is correct behavior, but operators must size the EHR's queue depth appropriately.

### Neutral

- **Postgres can still be HA.** This decision is about the application server, not Postgres. Operators run Postgres with replication, backups, and failover per their normal practice. The application server connects to a primary at a time; failover affects readiness, not safety.
- **Future versions are not constrained.** If the workload demonstrably requires multi-instance coordination, that is a future architectural decision and would be a major version of the project. The current design does not preclude it; it simply does not implement it.
- **The decision is enforced by the architecture, not by configuration.** There is no "single instance mode" knob. The codebase does not contain a leader-election or distributed-lock layer at all.
