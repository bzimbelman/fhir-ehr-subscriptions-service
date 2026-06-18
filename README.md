# fhir-ehr-subscriptions-service

`fhir-ehr-subscriptions-service` is a free and open source server that bridges
**FHIR Subscriptions** on one side and **Electronic Health Record (EHR) systems**
on the other. It allows external applications, registries, and analytics
platforms to subscribe to clinical events in an EHR using the standard FHIR
Subscriptions API, even when the underlying EHR does not natively expose FHIR
Subscriptions, or only supports them in a limited way. The server speaks the
FHIR Subscriptions protocol upstream and translates downstream into whatever
the EHR actually provides — typically HL7 v2 messaging, FHIR REST APIs, and/or
vendor-proprietary APIs.

## Status

**Pre-alpha. No implementation has been written yet.** The repository currently
contains the design documents under `docs/` and a Go module skeleton that locks
in the directory shape so component implementation can begin. There are no
binaries to run, no tests to invoke, and the surface area described in the
design documents is not yet wired up.

## Documentation map

The design is split across three layers, each at a different altitude.

- [`docs/high-level-concept.md`](docs/high-level-concept.md) — the why: goals,
  deployment model, the two sides of the bridge.
- [`docs/architecture.md`](docs/architecture.md) — the what: module
  boundaries, contracts between modules, the five-stage pipeline, the
  Adapter SPI, the Channel SPI, runtime model.
- [`docs/high-level-design/`](docs/high-level-design/) — per-domain
  responsibilities, contracts (SPIs, table shapes, protocol contracts),
  and the load-bearing decision records under
  [`decisions/`](docs/high-level-design/decisions/).
- [`docs/low-level-design/`](docs/low-level-design/) — per-component
  implementation-level designs. Start at the
  [LLD README](docs/low-level-design/README.md).
- [`docs/repository-layout.md`](docs/repository-layout.md) — how the design
  documents map onto the directories in this repository.

The arrows of intent point downstream: concept frames the architecture, the
architecture frames the HLD, the HLD frames the LLDs, and the LLDs frame the
code. None of those documents replicate each other; each has a job.

## Quickstart

There is no runnable code yet. To start contributing:

1. Read [`CONTRIBUTING.md`](CONTRIBUTING.md) for the contribution workflow,
   including the project's TDD requirement and the spec-conformance bar.
2. Read [`docs/low-level-design/README.md`](docs/low-level-design/README.md)
   for the component-by-component design that implementation work lands
   against.
3. Pick a component whose LLD is in a `drafted` state and follow the
   conventions described there.

When the first component begins implementation, this section will grow to
include build, test, and run commands.

## License

This project is licensed under the Apache License, Version 2.0. See
[`LICENSE`](LICENSE) for the full text and [`NOTICE`](NOTICE) for the
required attribution notice.
