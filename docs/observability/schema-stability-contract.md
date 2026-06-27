# Schema-stability CI gate — design

> Status: design sketch (ticket #397). The implementation is a follow-up
> ticket; this doc describes what the gate **will** do so the contract
> defined in `log-schema.md` and `metric-catalog.md` has an explicit
> enforcement plan.
>
> Today: `scripts/observability/check-log-schema.sh` is a stub that prints
> "not yet implemented" and exits 0. The doc-parser test in
> `scripts/observability/test-doc-parses.sh` does the lightweight parse
> sanity check that's actually wired into our checks.

## Motivation

`log-schema.md` and `metric-catalog.md` are the contract for what agent
parsers and downstream dashboards can rely on. Without enforcement, any
casual edit to a Logback config or a Micrometer registration can silently
violate the contract — drop a `customFields` entry, rename a metric, change
a label — and we'd only find out when a downstream parser breaks weeks
later.

The CI gate makes the contract executable. Drift between code and doc
fails the build.

## What the gate does

### For logs

1. **Parse `docs/observability/log-schema.md`** into a structured
   representation using the same parser that powers
   `scripts/observability/test-doc-parses.sh`. The parser already emits
   JSON; the gate consumes that JSON directly.
2. **Run the test suite** with logs piped to a capture buffer (already how
   `JsonLogLayoutTest` operates; the gate extends the pattern to a broader
   run — e.g. `./gradlew test --info` with a capture wrapper, or a small
   Testcontainers harness that drives a single message through the
   pipeline).
3. **For every captured JSON record**:
   - Assert every REQUIRED field (per the parsed matrix) is present.
   - Assert no field appears that is documented as removed or whose tier
     was changed to "removed" between this commit and the prior release.
4. **For each REQUIRED field**:
   - Assert at least one captured record exhibits it (else the field is
     documented but unreachable — a doc bug).
5. **Fail the build** with a clear diff if either check fails:
   ```
   FAIL: record at line 42 missing REQUIRED field 'schema_version':
     {"@timestamp": "...", "level": "INFO", ... }
   ```

### For metrics

1. **Parse `docs/observability/metric-catalog.md`** the same way.
2. **Boot the service** (Testcontainers, or just the local Gradle test
   harness) and **scrape `/actuator/prometheus`**.
3. **For every metric line returned**:
   - Assert every REQUIRED metric in the catalog appears in the scrape.
   - Assert no metric appears that is documented as removed.
   - Assert each REQUIRED metric carries its documented labels (just
     presence — value-level cardinality enforcement is harder and not in
     v1).
4. **Cardinality smoke check**: count the distinct label sets per metric
   and flag any REQUIRED metric whose cardinality exceeds the documented
   cap by more than 10x (catches accidental high-cardinality labels).
5. **Fail the build** with a per-metric diff if any check fails.

## What the gate does NOT do (v1)

- It doesn't enforce **value-level** label allowlists (e.g. checking
  `source_system` only contains documented enum values). That requires
  running real traffic through the system; v1 is a static-shape check.
- It doesn't generate the OpenAPI spec for the admin API (that's ticket
  #396's CI concern, separate contract).
- It doesn't catch breakages introduced by upstream libraries (Logback,
  Micrometer, Spring Boot). The promise is bounded to fields and metrics
  we own; defaults inherited from the framework are OPTIONAL by tier.
- It doesn't run on PRs that don't touch observability code or docs —
  ideally it's a fast check that runs always, but a path filter is
  acceptable if runtime becomes a problem.

## Where it runs

- **Local**: `bash scripts/observability/check-log-schema.sh` (when the
  stub is replaced with the real implementation)
- **CI**: a Gradle task that wraps the same check, invoked from the
  pipeline stage that runs alongside `./gradlew test`

The stub today returns 0 so it's safe to wire into CI immediately as a
non-blocking check; once the real implementation lands, the same script
becomes blocking.

## Implementation outline (for the follow-up ticket)

Roughly:

1. Generalize `scripts/observability/parse-log-schema.py` to also parse
   `metric-catalog.md`. Same table extractor, different table identifier.
2. Write `scripts/observability/check-log-schema.sh` (replacing the stub).
   It:
   - Invokes the doc parser, captures the structured JSON.
   - Runs `./gradlew :interface-engine:test --info` with stdout captured
     to a file.
   - Greps the file for `{"@timestamp":` JSON lines, parses each, runs
     the assertions described above.
   - For metrics: spins up a Testcontainers harness (already used by
     other tests), hits `/actuator/prometheus`, runs the assertions.
3. Add a Gradle task that calls the script and is part of the default
   `check` lifecycle.

Estimated effort: 1–2 days. The structural pieces (doc parser,
Testcontainers harness, JsonLogLayoutTest pattern) all exist.

## Failure modes the gate must NOT have

- **Flaky pass**: the gate must NOT pass on partial captures. If the test
  suite doesn't produce a record exercising every REQUIRED field, the
  gate fails closed.
- **Doc drift inside the catalog**: if a metric is in the catalog but
  isn't in the scrape, the gate fails (someone removed an implementation
  without updating the doc).
- **Implementation drift outside the catalog**: if a metric is in the
  scrape but NOT in the catalog and isn't whitelisted as
  Spring/Micrometer-default, the gate fails (someone added a metric
  without documenting it).

The whitelist for inherited framework metrics lives next to the gate as
`scripts/observability/framework-metrics-allowlist.txt`. It is hand-curated
and the catalog notes which families it covers.

---

## See also

- [`log-schema.md`](./log-schema.md) — the log contract the gate enforces
- [`metric-catalog.md`](./metric-catalog.md) — the metric contract the gate enforces
- `scripts/observability/parse-log-schema.py` — doc parser (already wired up)
- `scripts/observability/check-log-schema.sh` — stub for the future gate
- `scripts/observability/test-doc-parses.sh` — lightweight check that runs today
