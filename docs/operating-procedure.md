# Operating Procedure for Implementation Work

**Purpose.** The standing rules every implementation subagent inherits. Implementation prompts reference this document; they do not restate it.

## Worktree isolation

Every implementation subagent runs with `isolation: "worktree"`. The agent's branch follows the pattern `feat/<component>` or `feat/<component>-<short-slug>`. The agent commits to its branch; the parent (the orchestrator) merges to `main` only after verification.

## Test-Driven Development

TDD is mandatory. The order is fixed:

1. Read the LLD and relevant ADRs first. The doc set is the spec.
2. Write failing tests first. Unit tests covering every documented behavior, contract, error path, and idempotency invariant. Integration tests where the LLD calls them out. Tests must compile and must fail when run before any implementation code is written.
3. Implement to green. Minimum code to pass the tests.
4. Refactor only with green tests.
5. Property tests for invariants the LLD calls out: idempotency under retry, monotonic ordering, hash-chain integrity, transactional outbox semantics.

Commit history must show the TDD order: at least one commit with failing tests before any commit with passing implementation.

## Mandatory test invocations

Every implementation PR runs:

- `go test -race ./...`
- `golangci-lint run`
- `go build ./...`

The race detector is mandatory. A failing race test blocks merge.

## Test fixtures

Conformance fixtures live in `testdata/`. They are shared across components. New fixtures land alongside the test that uses them.

## Verification before merge

The orchestrator does not merge an agent's branch to `main` until:

1. All mandatory test invocations pass on the agent's branch.
2. A separate verifier subagent reads the diff against the LLD and confirms the implementation matches.
3. The end-to-end test suite goes a step further green than it did before this branch.

## End-to-end is the proof

The e2e harness in `docs/low-level-design/e2e-harness.md` is the project's source of truth for "the bridge works." Unit-test coverage is necessary but not sufficient.

## What an implementation prompt MUST include

- The component being implemented and its LLD path.
- ADRs that bear on the component.
- Expected test files (paths) and what they cover.
- Expected source files (paths) and what they implement.
- Reference to this operating-procedure document.

## What an implementation prompt MUST NOT include

- Permission to skip TDD.
- Permission to merge to `main`.
- Permission to modify documentation outside the component's LLD.
- Permission to change another component's contract.

## Failure modes

If an implementation subagent fails, the parent does NOT auto-retry. The parent inspects, decides whether to send a continuation message, spawn a fresh agent with tightened context, or surface the failure to the user. A failing agent's branch is preserved.
