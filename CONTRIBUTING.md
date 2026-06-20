# Contributing to fhir-ehr-subscriptions-service

Thank you for your interest in contributing. This project is a healthcare
integration server built to a published, spec-driven design. The contribution
workflow exists to keep that design honest as the code grows.

## Read the design docs first

Before opening a pull request that changes behavior, read the design layer that
covers what you are touching:

- [`docs/high-level-design/README.md`](docs/high-level-design/README.md) — the
  contracts and decisions every component is built against. Decisions live
  under [`docs/high-level-design/decisions/`](docs/high-level-design/decisions/);
  they are accepted records, not proposals.
- [`docs/low-level-design/README.md`](docs/low-level-design/README.md) — the
  component-by-component implementation designs. Each LLD pins module
  decomposition, public surface, internal data structures, transactional
  invariants, the metric set, and the test plan for one component.

If your change contradicts an LLD, the LLD is the source of truth — fix the LLD
first in the same pull request and let the code follow. If your change
contradicts an HLD or an accepted decision record, open a separate
documentation pull request first.

## TDD is mandatory

Every implementation pull request comes with failing tests written first, then
green. The workflow is non-negotiable:

1. Write the test that captures the new behavior. Watch it fail.
2. Implement the minimum code to make it pass.
3. Refactor with the test still green.
4. Run the full test suite before pushing.

Property-based tests are encouraged for invariants the LLDs name explicitly —
parser round-trips, canonicalization stability, scheduler ordering, retry
back-off curves, and the cancel-and-replace pairing state machine are good
candidates. Property tests live alongside example-based tests; both are
expected for any non-trivial component.

Tests that exercise the durable transactional outbox pattern (input row
mark-processed and output row INSERT in one Postgres transaction) are required
for any component that participates in the pipeline. The patterns are described
in the cross-component conventions of the LLD README.

## Run the test suite

The project uses the standard Go toolchain.

```
go test -race ./...
golangci-lint run
```

The race detector is mandatory in CI; please run it locally before pushing.
The lint configuration lives in `.golangci.yml`.

## CI gates

Every PR runs the following gates without any opt-in label:

- **Unit tests** with the race detector (`ci.yml`).
- **Coverage threshold** — `tools/covergate` parses `cover.out` and
  fails the build below the per-package floor in
  `.coverage-thresholds.json` (default 80%).
- **govulncheck** — `golang.org/x/vuln/cmd/govulncheck ./...`. Adding
  a known-CVE dep blocks merge.
- **Integration tests** (`integration.yml`, `-tags integration`).
- **e2e smoke** (`integration.yml`, `-tags e2e_smoke`, `e2e/smoke/...`)
  — the ~3-minute subset that exercises probe-only boot and `/healthz`
  against the binary.
- **Image smoke** (`ci.yml`, `docker` job) — the built image is
  exercised via `docker run --version`, `--check-config`, and a
  `/healthz` probe so entrypoint, ldflag, and probe regressions fail
  CI before merge.

Optional gates (label-driven):

- **Full e2e** (`integration.yml` `e2e` job) — runs on pushes to `main`
  and on PRs labeled `full-e2e`. The smoke job covers the default-PR
  budget; the full suite is gated to keep PR turnaround fast.

Updates to dependencies arrive via Dependabot (`.github/dependabot.yml`)
on a weekly cadence covering Go modules, GitHub Actions, and Docker
base images.

## Spec compliance

The external compliance bar for this project is **Inferno** (the ONC reference
test suite for FHIR-based standards) and **Touchstone** (HL7's FHIR conformance
service). Either is a hard gate before a release is cut.

Inside the repository, the project also ships its own conformance fixtures
under `testdata/` — captured `SubscriptionTopic` resources, sample HL7 v2
messages with vendor Z-segments, golden-file notification bundles, and the RFC
8785 (JCS) canonicalization test vectors. Component LLDs reference these
fixtures by name; please add to them when you add behavior they should cover.

## Commit style

Commit messages use a conventional-commits-lite style: a type prefix and an
optional component scope, then a short imperative summary.

```
feat(mllp): add framing parser
fix(topic-matcher): reject FHIRPath beyond traversal limit
docs(lld): update channels.md retry semantics
test(scheduler): add property test for backoff curve
```

One commit per logical change. If you need to fix a typo or address review
feedback, prefer a follow-up commit; squash on merge if the project decides to.

## Sign-off (DCO)

This project requires the Developer Certificate of Origin. Sign every commit
with `git commit -s`; that adds a `Signed-off-by:` trailer to the commit
message and is the contributor's certification that they have the right to
submit the change under the project's license.

## Code review

A pull request is ready to merge when:

- Tests are green (including the race detector).
- The lint pass is clean.
- The change is consistent with the relevant LLD, or the LLD has been updated
  in the same pull request.
- Any new behavior is covered by tests and, where appropriate, fixtures under
  `testdata/`.
- The commit history is readable.

Reviewers will check the design alignment first and the implementation second.
A change that reaches review with no design fit will be sent back to the
docs.

## License

By contributing, you agree that your contributions will be licensed under the
Apache License, Version 2.0. See [`LICENSE`](LICENSE).
