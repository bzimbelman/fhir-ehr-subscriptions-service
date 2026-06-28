# Governance

This document describes how decisions are made in this project and how the project is organized.

## Project scope

This repository is the engine for the bzonfhir subscription-service: the JVM pipeline that ingests HL7 v2 and FHIR messages, applies routing and transformation, and dispatches to subscribers. Companion repositories (`subscription-service-profiles`, `subscription-service-plugins-community`, `subscription-service-examples`, `bzonfhir-site`) extend and document this engine. The same governance model applies across all of them.

## Maintainership

Today the project has a **single maintainer**: Brian Zimbelman ([@bzimbelman](https://github.com/bzimbelman)). The maintainer is the final decision-maker on:

- What changes are accepted into `main`.
- Release timing and version numbers.
- The set of supported extension points and SPI contracts.
- The published roadmap.

Single-maintainer governance is intentional at this stage: the project is young, the contract surface is still being stabilized, and a single point of accountability lets the design stay coherent. It also means responses may be slower than a multi-maintainer project — please be patient.

## Planned scale-up

When sustained external contribution arrives — concretely, when at least three external contributors have each landed three or more substantive PRs over a 90-day window — the maintainer will propose a **steering committee** to take over governance of this repo and the companions. The committee will be a small group (three to five members) chartered to:

- Resolve technical disputes the maintainer cannot resolve alone.
- Approve breaking changes to the SPI / manifest schema / public contracts.
- Add and remove committee members.
- Update this document.

The steering-committee bylaws will be drafted at that time and published as a follow-up to this file. Until then, the maintainer makes those calls.

## Decision-making in the meantime

For day-to-day decisions:

- **Accepting a change**: the maintainer reviews, requests changes if needed, and merges when CI is green.
- **Rejecting a change**: the maintainer says no with a reason. Common reasons: out of scope, security concern, breaks a published contract without a transition plan, missing tests.
- **Disagreement**: open a GitHub Discussion. The maintainer will respond. If you still disagree, you are welcome to fork. The intent is to make disagreement cheap, not punitive.

## Contribution sign-off (DCO, not CLA)

All commits MUST be signed off under the [Developer Certificate of Origin](https://developercertificate.org/):

```bash
git commit -s -m "your message"
```

The `-s` flag appends a `Signed-off-by:` trailer to the commit, which is your assertion that you have the right to contribute the code under the project's license (Apache 2.0).

We do **not** use a Contributor License Agreement. The DCO + Apache 2.0 combination keeps the contribution path simple: no separate paperwork, no clickwrap, no corporate signatory required.

DCO enforcement on pull requests is provided by the [DCO GitHub App](https://github.com/apps/dco). The app installation is an org-level setting; the maintainer is responsible for keeping it installed and configured for this repository. The per-repo config lives in `.github/dco.yml`.

## Code of conduct

All participation in this project is subject to the [Code of Conduct](CODE_OF_CONDUCT.md) (Contributor Covenant v2.1). The maintainer enforces it.

## Security

Security issues should be reported privately via the GitHub Security Advisory page for the repository, or by opening a minimal public issue asking for a private contact channel. Do **not** file a public issue containing exploit detail. See `SECURITY.md` for details when present.

## Licensing

Code is Apache 2.0. Documentation is CC BY 4.0 unless a directory says otherwise. By submitting a contribution you agree it is licensed under the same terms as the surrounding file.

## Amending this document

Today: the maintainer amends this file by PR-and-merge. After the steering committee stands up: changes require a majority vote of the committee, recorded in the PR conversation.
