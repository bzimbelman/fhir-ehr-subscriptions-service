# Contributing to subscription-service

Thanks for considering a contribution. This repository is the **engine** — the JVM pipeline that ingests HL7 v2 and FHIR messages, applies routing and transformation through the plugin SPI, and dispatches to subscribers.

This is the project's core repo. Companion repos extend it:

- [`subscription-service-profiles`](https://github.com/bzimbelman/subscription-service-profiles) — vendor profile catalog (manifests + StructureMaps).
- [`subscription-service-plugins-community`](https://github.com/bzimbelman/subscription-service-plugins-community) — community plugins implementing the SPI.
- [`subscription-service-examples`](https://github.com/bzimbelman/subscription-service-examples) — deployment recipes.

A change that *adds an extension point* or *changes a contract surface* (SPI, manifest schema, REST API, env-var contract) lands here. A change that *uses* an existing extension point lands in the companion repo it belongs to.

## Ground rules

1. **Keep the contract surface intentional.** The SPI (`plugins-spi`) and the profile manifest schema are public contracts. We add to them deliberately and we do not break them silently. If your PR changes a public contract, the PR description MUST call that out and propose a deprecation path.
2. **Tests before code.** Every behavioral change needs at least one test that fails before your change and passes after. Bug-fix PRs MUST include a regression test.
3. **No native or unsafe code in the hot path.** The pipeline runs in customers' production deployments. JNI, `Runtime.exec`, arbitrary classloading, and reflection-into-internals are red flags. A PR that introduces them needs explicit discussion in an issue first.
4. **No PHI, real or real-looking, in fixtures.** Test fixtures use obviously-synthetic identifiers: `MRN-EXAMPLE-001`, `Test Patient One`, out-of-range birthdates. Synthetic-looking-but-plausible PHI is treated as PHI for review purposes.
5. **Match the surrounding style.** Kotlin code uses the existing `ktlint` config; Java uses the existing `google-java-format` config. Run the formatter before pushing.

## What we accept

| Kind of PR | Where it lands | Bar |
|---|---|---|
| Bug fix to engine behavior | here | Regression test, narrow blast radius |
| New SPI extension point | here | Issue first to agree on the shape; SPI doc + tests + reference plugin |
| New ingest source (built-in) | here, under `plugins-builtin/` | Tests + docs; case for "built-in" vs "community" |
| New community plugin | [`subscription-service-plugins-community`](https://github.com/bzimbelman/subscription-service-plugins-community) | See that repo's CONTRIBUTING |
| New vendor profile | [`subscription-service-profiles`](https://github.com/bzimbelman/subscription-service-profiles) | See that repo's CONTRIBUTING |
| Deployment recipe / example | [`subscription-service-examples`](https://github.com/bzimbelman/subscription-service-examples) | See that repo's CONTRIBUTING |
| Doc / typo | here, or wherever the doc lives | Open a PR; CI will tell us |

If you are not sure where your change belongs, open a [GitHub Discussion](https://github.com/bzimbelman/subscription-service/discussions) and ask before you write code.

## Workflow

1. **Fork** the repo, clone, create a branch off `main` named `<short-slug>` (e.g., `fix-mllp-framing-edge-case`).
2. **Write your test first.** Run it; confirm it fails for the reason you expect.
3. **Implement** the change. Keep diffs focused — one logical change per PR.
4. **Build and test:**
   ```bash
   ./gradlew build
   ./gradlew test
   ```
5. **Run the formatter** so CI's style check passes the first time:
   ```bash
   ./gradlew ktlintFormat spotlessApply
   ```
6. **Open a PR** against `main`. Fill in the PR template. Link any related issue.
7. **Review:** the maintainer will review. Expect feedback; expect revisions. We will always tell you why we asked for a change.
8. **Merge:** the maintainer merges after CI is green and review is approved.

## Commit messages & sign-off

We use the **Developer Certificate of Origin (DCO)** — sign every commit:

```bash
git commit -s -m "interface-engine: tighten MLLP framing on partial reads"
```

The `-s` flag adds a `Signed-off-by:` trailer. The [DCO GitHub App](https://github.com/apps/dco) enforces this on every PR — unsigned commits will fail the DCO check. There is **no CLA**; the Apache 2.0 license plus the DCO sign-off is the entire contribution-rights story.

Commit subject format: `<area>: <short subject>` in imperative mood, lower-case subject, under ~70 chars. Examples:

- `plugins-spi: add ObservabilityEnricher SPI`
- `matchbox: pin to 3.7.2 for ValueSet $expand fix`
- `ui: render footer link from extension registry`

The body explains *why*, not *what*. The diff already says *what*.

## Governance & decision-making

See [GOVERNANCE.md](GOVERNANCE.md). Today the project has a single maintainer; a steering committee is planned once sustained external contribution arrives. The maintainer is the final decision-maker until then.

## Code of conduct

Participation is subject to the [Code of Conduct](CODE_OF_CONDUCT.md) (Contributor Covenant v2.1). The maintainer enforces it.

## Security

Do **not** file a public GitHub issue for a security vulnerability. Use the repository's **Security Advisory** tab to open a private report, or open a minimal public issue requesting a private contact channel. See `SECURITY.md` when present.

## Questions

Open a [GitHub Discussion](https://github.com/bzimbelman/subscription-service/discussions). We use one Discussions instance for the whole project so you don't have to guess which repo to ask in.
