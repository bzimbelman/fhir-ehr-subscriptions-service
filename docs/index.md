# fhir-ehr-subscriptions-service

A free and open source server that bridges **FHIR Subscriptions** on one side
and **Electronic Health Record (EHR) systems** on the other. External
applications, registries, and analytics platforms subscribe to clinical events
in an EHR using the standard FHIR Subscriptions API. The server speaks the
FHIR Subscriptions protocol upstream and translates downstream into whatever
the EHR actually provides — typically HL7 v2 messaging, FHIR REST APIs, and/or
vendor-proprietary APIs.

!!! warning "Pre-alpha"
    The project is pre-alpha. Component implementations are landing now. The
    documentation here is the source of truth for how the system is intended
    to behave; treat divergence between docs and code as a bug in whichever
    side has not caught up yet.

## Where to start

The documentation is layered. Pick the altitude that matches your question:

- **[Concept](high-level-concept.md)** — *the why.* Goals, deployment model,
  the two sides of the bridge. Read this first if you have not seen the
  project before.
- **[Architecture](architecture.md)** — *the what.* Module boundaries,
  contracts between modules, the five-stage pipeline, the Adapter SPI, the
  Channel SPI, runtime model.
- **[High-Level Design](high-level-design/README.md)** — *per-domain
  responsibilities.* Domain-by-domain contracts (SPIs, table shapes, protocol
  contracts) and the load-bearing
  [Architecture Decision Records](high-level-design/decisions/index.md).
- **[Low-Level Design](low-level-design/README.md)** — *per-component
  implementation designs.* Each LLD is what the implementation lands against.

Operators and SREs:

- **[Operating Procedure](operating-procedure.md)** — what running the
  service looks like.
- **[Dead-letters Runbook](operations/dead-letters-runbook.md)**,
  **[Horizontal Scale](operations/horizontal-scale.md)**,
  **[OTel Exporter Recipes](operations/otel-exporter-recipes.md)** — the
  operational handbook.
- **[Production Readiness Audit](production-readiness-audit.md)** — the
  living checklist of resolved and open items between current state and
  general availability.

Contributors and reviewers:

- **[Repository Layout](repository-layout.md)** — how the design documents
  map onto the directories in the repository.
- **[Future Work](future-work.md)** — the backlog of intentionally deferred
  scope.

## How the documents relate

The arrows of intent point downstream: concept frames the architecture, the
architecture frames the HLD, the HLD frames the LLDs, and the LLDs frame the
code. None of those documents replicate each other; each has a specific job
and a specific audience.

| Layer | Audience | Question it answers |
|---|---|---|
| Concept | Anyone evaluating the project | *Why does this exist? What problem does it solve?* |
| Architecture | Architects, integrators | *What are the moving parts and how do they fit?* |
| HLD | Domain owners, reviewers | *What does each domain promise to do, and through which contracts?* |
| LLD | Implementers | *How does this component actually work, and how is it tested?* |
| Operations | SREs, on-call | *How do I run this in production? What do I do when it breaks?* |

## Project status and resources

- **[Status](status.md)** — current implementation status, by component.
- **[GitHub repository](https://github.com/bzimbelman/fhir-ehr-subscriptions-service)**
  — source code, issues, pull requests.
- **License** — Apache 2.0. See `LICENSE` and `NOTICE` in the repository.
