# POC Completion Epic — Coverage Matrix

This document maps every finding from the four-pass production-readiness audit (#1–#199) to the OpenProject story (or stories) that owns it. Generated alongside the epic decomposition.

## Epic

- **Epic ID:** [#91](https://op.bzonfhir.com/work_packages/91)
- **Title:** Finish the POC — wire, harden, and verify the production binary
- **Total stories:** 143

## Audit sources

- `docs/production-readiness-honesty-audit.md` — findings #1–#50
- `docs/production-readiness-honesty-audit-supplement.md` — findings #51–#107 (table extends to #127; #108–#127 in this file are superseded by sup2)
- `docs/production-readiness-honesty-audit-supplement-2.md` — findings #108–#157
- `docs/production-readiness-honesty-audit-supplement-3.md` — findings #158–#199

## Story counts by priority

| Priority | Count |
|----------|-------|
| P0 | 44 |
| P1 | 44 |
| P2 | 34 |
| P3 | 21 |
| **Total** | **143** |

## Stories in priority order

### P0 — blockers preventing the binary from doing its core job

| Story | Title |
|-------|-------|
| [#92](https://op.bzonfhir.com/work_packages/92) | Wire RegisterAdminRoutes into production router |
| [#93](https://op.bzonfhir.com/work_packages/93) | Wire RegisterPublicRoutes for unauthenticated /metadata |
| [#94](https://op.bzonfhir.com/work_packages/94) | Wire observability.Start (metrics + OTel + audit + dead-letter reporter) |
| [#95](https://op.bzonfhir.com/work_packages/95) | Wire storage.Start (partition maintainer + retention sweeper) |
| [#96](https://op.bzonfhir.com/work_packages/96) | Wire FhirScanRunner worker into production binary |
| [#97](https://op.bzonfhir.com/work_packages/97) | Wire VendorAPIClient worker into production binary |
| [#98](https://op.bzonfhir.com/work_packages/98) | Wire HydrationService into production binary |
| [#99](https://op.bzonfhir.com/work_packages/99) | Wire adapter Supervisor framework |
| [#100](https://op.bzonfhir.com/work_packages/100) | Wire WebhookHandler ingress |
| [#101](https://op.bzonfhir.com/work_packages/101) | Wire WebSocket channel into ChannelRegistry and Lifecycle |
| [#102](https://op.bzonfhir.com/work_packages/102) | Wire Email channel into ChannelRegistry and Lifecycle |
| [#103](https://op.bzonfhir.com/work_packages/103) | Wire Message channel into ChannelRegistry and Lifecycle |
| [#104](https://op.bzonfhir.com/work_packages/104) | Wire per-client rate limiters (subscription create + WS bind-token mint) |
| [#105](https://op.bzonfhir.com/work_packages/105) | Replace PgAuditStore placeholder with hash-chained writer |
| [#106](https://op.bzonfhir.com/work_packages/106) | Fix audit_log schema mismatch (code vs migration) |
| [#107](https://op.bzonfhir.com/work_packages/107) | Fix audit chain canonical input vs on-disk timestamp |
| [#108](https://op.bzonfhir.com/work_packages/108) | Fix audit chain re-anchor and hex-encoded prior_hash |
| [#109](https://op.bzonfhir.com/work_packages/109) | Bind AAD into codec AES-GCM envelope |
| [#110](https://op.bzonfhir.com/work_packages/110) | Make JTI replay check atomic (verifier + token endpoint) |
| [#111](https://op.bzonfhir.com/work_packages/111) | Serve TLS on the HTTP listener |
| [#112](https://op.bzonfhir.com/work_packages/112) | Plumb MLLP TLS / mTLS into buildMLLPListener |
| [#113](https://op.bzonfhir.com/work_packages/113) | Register all vendor adapters at startup |
| [#114](https://op.bzonfhir.com/work_packages/114) | Implement real activation handshake (websocket + email) |
| [#115](https://op.bzonfhir.com/work_packages/115) | Plumb topics catalog directory in Helm chart |
| [#116](https://op.bzonfhir.com/work_packages/116) | Tighten Validate() to require database/codec/auth/MLLP/topics |
| [#117](https://op.bzonfhir.com/work_packages/117) | Remove probe-only fallback or make it explicit-opt-in |
| [#118](https://op.bzonfhir.com/work_packages/118) | Align Helm probe port with binary listener |
| [#119](https://op.bzonfhir.com/work_packages/119) | Implement ${env:VAR} and ${file:/path} placeholder interpolation |
| [#120](https://op.bzonfhir.com/work_packages/120) | Rewrite Helm chart config schema to match Config struct |
| [#145](https://op.bzonfhir.com/work_packages/145) | Replace 11 t.Skip merge-gate scenarios with real e2e tests |
| [#146](https://op.bzonfhir.com/work_packages/146) | Run real verifier.Middleware in e2e harness |
| [#147](https://op.bzonfhir.com/work_packages/147) | Replace stubChannelActivator with real activators in e2e harness |
| [#148](https://op.bzonfhir.com/work_packages/148) | Compare hash bytes in PgAuditStore integration test |
| [#150](https://op.bzonfhir.com/work_packages/150) | Stop bypassing handlers in RegisterSubscriber e2e helper |
| [#154](https://op.bzonfhir.com/work_packages/154) | Make demo's documented happy path actually deliver |
| [#162](https://op.bzonfhir.com/work_packages/162) | Rewrite docs/architecture.md to match actual Config |
| [#182](https://op.bzonfhir.com/work_packages/182) | Re-validate subscriber URL at delivery time |
| [#190](https://op.bzonfhir.com/work_packages/190) | Bound parseRetryAfter (max + min floor) |
| [#197](https://op.bzonfhir.com/work_packages/197) | Filter $events by client_id |
| [#206](https://op.bzonfhir.com/work_packages/206) | Plumb caller ctx into lifecycle sequencer |
| [#207](https://op.bzonfhir.com/work_packages/207) | Register production shutdown hooks against lifecycle phases |
| [#209](https://op.bzonfhir.com/work_packages/209) | Fix release ldflag main.Version casing |
| [#210](https://op.bzonfhir.com/work_packages/210) | Add build-time ldflags to Dockerfile |
| [#212](https://op.bzonfhir.com/work_packages/212) | Wire make migrate-up Makefile target |

### P1 — high-severity gaps with silent data corruption / security holes

| Story | Title |
|-------|-------|
| [#121](https://op.bzonfhir.com/work_packages/121) | Mount /metrics service + ServiceMonitor only after metrics endpoint exists |
| [#122](https://op.bzonfhir.com/work_packages/122) | Tighten NetworkPolicy default ingress to specific pod selectors |
| [#126](https://op.bzonfhir.com/work_packages/126) | Always run integration tests on default PRs |
| [#127](https://op.bzonfhir.com/work_packages/127) | Always run e2e suite on default PRs |
| [#129](https://op.bzonfhir.com/work_packages/129) | Sign release images with cosign |
| [#131](https://op.bzonfhir.com/work_packages/131) | Add dependabot/renovate plus govulncheck |
| [#138](https://op.bzonfhir.com/work_packages/138) | Add key_version column to dead_letters |
| [#149](https://op.bzonfhir.com/work_packages/149) | E2E exercises every vendor adapter MapToFHIR |
| [#155](https://op.bzonfhir.com/work_packages/155) | Strip hardcoded AES key out of demo/config.yaml |
| [#160](https://op.bzonfhir.com/work_packages/160) | Honor cfg.Deployment.LogFormat |
| [#163](https://op.bzonfhir.com/work_packages/163) | Rewrite OTel exporter recipes runbook |
| [#164](https://op.bzonfhir.com/work_packages/164) | Rewrite dead-letters runbook to match wired metrics |
| [#165](https://op.bzonfhir.com/work_packages/165) | Rewrite horizontal-scale runbook to match wired workers |
| [#168](https://op.bzonfhir.com/work_packages/168) | Implement vendor MapToFHIR for Cerner adapter |
| [#169](https://op.bzonfhir.com/work_packages/169) | Implement vendor MapToFHIR for Epic adapter |
| [#170](https://op.bzonfhir.com/work_packages/170) | Implement vendor MapToFHIR for Athena adapter |
| [#171](https://op.bzonfhir.com/work_packages/171) | Implement vendor MapToFHIR for NextGen adapter |
| [#172](https://op.bzonfhir.com/work_packages/172) | Implement vendor MapToFHIR for Meditech adapter |
| [#173](https://op.bzonfhir.com/work_packages/173) | Implement vendor MapToFHIR for Allscripts adapter |
| [#174](https://op.bzonfhir.com/work_packages/174) | Implement default-adapter MapToFHIR |
| [#176](https://op.bzonfhir.com/work_packages/176) | Wire Deps.Metrics into handlers |
| [#177](https://op.bzonfhir.com/work_packages/177) | Wire Deps.Logger into handlers |
| [#178](https://op.bzonfhir.com/work_packages/178) | Wire Deps.* tunable knobs (page sizes, byte caps, FHIR version, JWKSURL) |
| [#179](https://op.bzonfhir.com/work_packages/179) | Set BaseURL/WSBaseURL respecting insecure and bind interface |
| [#181](https://op.bzonfhir.com/work_packages/181) | Mount /.well-known/jwks.json |
| [#183](https://op.bzonfhir.com/work_packages/183) | Block CG-NAT, IPv6 site-local, broadcast, IDN homoglyph hosts |
| [#184](https://op.bzonfhir.com/work_packages/184) | Decouple AllowHTTP from AllowInsecureJWKS |
| [#189](https://op.bzonfhir.com/work_packages/189) | Tighten parseCountParam from Sscanf to strconv.Atoi |
| [#191](https://op.bzonfhir.com/work_packages/191) | Hard-delete subscription on DELETE or return 410 Gone |
| [#192](https://op.bzonfhir.com/work_packages/192) | Add searchParam[] entries to CapabilityStatement |
| [#193](https://op.bzonfhir.com/work_packages/193) | Use absolute extension URLs in CapabilityStatement |
| [#194](https://op.bzonfhir.com/work_packages/194) | Honor MSH-2 escape characters in MSH-7 parser |
| [#195](https://op.bzonfhir.com/work_packages/195) | Match MSH segment ID case-insensitively |
| [#196](https://op.bzonfhir.com/work_packages/196) | Parse HL7 v2 timestamps with sub-second + offset support |
| [#198](https://op.bzonfhir.com/work_packages/198) | Add Close() to Channel SPI |
| [#200](https://op.bzonfhir.com/work_packages/200) | Lock recoverStuck against multi-pod double-increment |
| [#201](https://op.bzonfhir.com/work_packages/201) | Move pipeline workers under pipelineWG before MarkStartupComplete |
| [#203](https://op.bzonfhir.com/work_packages/203) | Unify TLS minimum version across rest-hook activator/delivery and email STARTTLS |
| [#204](https://op.bzonfhir.com/work_packages/204) | Logging redactor: cover endpoint paths and value-side PHI |
| [#214](https://op.bzonfhir.com/work_packages/214) | Make every migration body idempotent |
| [#216](https://op.bzonfhir.com/work_packages/216) | Surface Postgres pool tunables |
| [#224](https://op.bzonfhir.com/work_packages/224) | Remove or wire phantom infra packages (wakeup, queue) |
| [#225](https://op.bzonfhir.com/work_packages/225) | Remove or wire phantom engine packages (heartbeat, topicmatcher, topics/filter) |
| [#231](https://op.bzonfhir.com/work_packages/231) | Make audit-verify CLI work against the production audit chain |

### P2 — medium-severity correctness/robustness gaps

| Story | Title |
|-------|-------|
| [#125](https://op.bzonfhir.com/work_packages/125) | Rename or repurpose migrationInit init container |
| [#128](https://op.bzonfhir.com/work_packages/128) | Enforce coverage threshold in CI |
| [#130](https://op.bzonfhir.com/work_packages/130) | Cross-platform image build on every PR |
| [#132](https://op.bzonfhir.com/work_packages/132) | Pin and tune golangci-lint config |
| [#133](https://op.bzonfhir.com/work_packages/133) | Add nightly failure alerting |
| [#135](https://op.bzonfhir.com/work_packages/135) | Smoke-run built image in CI |
| [#139](https://op.bzonfhir.com/work_packages/139) | Either persist deliveries.bundle or drop the column |
| [#140](https://op.bzonfhir.com/work_packages/140) | Move schema_migrations.checksum into a numbered migration |
| [#151](https://op.bzonfhir.com/work_packages/151) | Wire SIGHUP config reload to whole config |
| [#152](https://op.bzonfhir.com/work_packages/152) | Wire WatchSecretFiles in production startup |
| [#153](https://op.bzonfhir.com/work_packages/153) | Replace tests of unwired packages with integration tests |
| [#166](https://op.bzonfhir.com/work_packages/166) | Document or remove the soft-deferred dead-letters CLI |
| [#167](https://op.bzonfhir.com/work_packages/167) | Allow --set on remaining auth knobs |
| [#175](https://op.bzonfhir.com/work_packages/175) | Resolve direct-adapter capability lie |
| [#180](https://op.bzonfhir.com/work_packages/180) | Make WSBindingTTL config-driven |
| [#185](https://op.bzonfhir.com/work_packages/185) | Plumb URLValidator.AllowHosts from config |
| [#186](https://op.bzonfhir.com/work_packages/186) | Pass request context to URL validator DNS lookup |
| [#188](https://op.bzonfhir.com/work_packages/188) | Add LIMIT to PgSubscriptionsStore.ListByClient |
| [#199](https://op.bzonfhir.com/work_packages/199) | Surface scheduler tunables (RecoveryInterval, StuckThreshold, DispatchConcurrency) |
| [#202](https://op.bzonfhir.com/work_packages/202) | Single-flight JWKS fetch under burst |
| [#208](https://op.bzonfhir.com/work_packages/208) | Close rest-hook activator HTTP transport on shutdown |
| [#213](https://op.bzonfhir.com/work_packages/213) | Drop dead direct-require packages from go.mod |
| [#215](https://op.bzonfhir.com/work_packages/215) | Stop fixing partition trigger on now() instead of created_at |
| [#217](https://op.bzonfhir.com/work_packages/217) | Surface hardcoded ActivationTimeout, scheduler retry, processor poll |
| [#219](https://op.bzonfhir.com/work_packages/219) | Remove orphan documentation pages or add to nav |
| [#221](https://op.bzonfhir.com/work_packages/221) | Streamline diagnosticForReason to cover all error paths |
| [#222](https://op.bzonfhir.com/work_packages/222) | Replace string-match isPermanentBuildError with typed errors |
| [#223](https://op.bzonfhir.com/work_packages/223) | Add upper bound to listDeadLetters Atoi parsing |
| [#226](https://op.bzonfhir.com/work_packages/226) | Make TrustedIssuers actually filter JWKS lookups |
| [#227](https://op.bzonfhir.com/work_packages/227) | Tighten MLLP framer pending-buffer cap check |
| [#228](https://op.bzonfhir.com/work_packages/228) | Document MLLP startRead readCh buffering invariant |
| [#232](https://op.bzonfhir.com/work_packages/232) | Scope StreamActiveByTopic to prevent cross-tenant secret disclosure |
| [#233](https://op.bzonfhir.com/work_packages/233) | Honor strong-ETag If-Match in subscription update |
| [#234](https://op.bzonfhir.com/work_packages/234) | Populate additionalContext on full-resource notification Bundles |

### P3 — low-severity polish

| Story | Title |
|-------|-------|
| [#123](https://op.bzonfhir.com/work_packages/123) | Replace personal GHCR repo default in Helm chart |
| [#124](https://op.bzonfhir.com/work_packages/124) | Resolve replicaCount-vs-HPA collision in Helm chart |
| [#134](https://op.bzonfhir.com/work_packages/134) | Pin Go toolchain version exactly |
| [#136](https://op.bzonfhir.com/work_packages/136) | Drop unused id-token release scope |
| [#137](https://op.bzonfhir.com/work_packages/137) | Add mkdocs build --strict on PRs |
| [#141](https://op.bzonfhir.com/work_packages/141) | Add NextEventNumber field to SubscriptionRow |
| [#142](https://op.bzonfhir.com/work_packages/142) | Wire subscription_topics retire SQL or drop retired_at |
| [#143](https://op.bzonfhir.com/work_packages/143) | Drop redundant subscription_topics_url_idx |
| [#144](https://op.bzonfhir.com/work_packages/144) | Document or unify events_since_subscription_start vs next_event_number |
| [#156](https://op.bzonfhir.com/work_packages/156) | Fix demo-publisher and demo-subscriber port mismatches |
| [#157](https://op.bzonfhir.com/work_packages/157) | Pick R5 OR R4B channel shape in demo-subscriber |
| [#158](https://op.bzonfhir.com/work_packages/158) | Stop importing e2e mocks from demo CLIs |
| [#159](https://op.bzonfhir.com/work_packages/159) | Document buildx requirement for compose multi-arch |
| [#161](https://op.bzonfhir.com/work_packages/161) | Time-out / cap http.DefaultClient usage in demo CLIs |
| [#187](https://op.bzonfhir.com/work_packages/187) | Use request context for handler bookkeeping writes |
| [#205](https://op.bzonfhir.com/work_packages/205) | Strict yaml.Decoder.KnownFields for config |
| [#211](https://op.bzonfhir.com/work_packages/211) | Make Version/Commit immutable build-info constants |
| [#218](https://op.bzonfhir.com/work_packages/218) | Remove channel.Channel(nil) dummy reference |
| [#220](https://op.bzonfhir.com/work_packages/220) | Drop GenesisHash deprecation or document migration |
| [#229](https://op.bzonfhir.com/work_packages/229) | Pin mkdocs and transitive doc-build dependencies |
| [#230](https://op.bzonfhir.com/work_packages/230) | Add demo bridge healthcheck |

## Finding → Story coverage matrix

Every finding ID in the union across all four audit files appears below with the story (or stories) that owns it.

| Finding | Story IDs | Story title (first owner) |
|---------|-----------|----------------------------|
| #1 | [#96](https://op.bzonfhir.com/work_packages/96), [#97](https://op.bzonfhir.com/work_packages/97), [#98](https://op.bzonfhir.com/work_packages/98) | Wire FhirScanRunner worker into production binary |
| #2 | [#96](https://op.bzonfhir.com/work_packages/96) | Wire FhirScanRunner worker into production binary |
| #3 | [#97](https://op.bzonfhir.com/work_packages/97) | Wire VendorAPIClient worker into production binary |
| #4 | [#99](https://op.bzonfhir.com/work_packages/99) | Wire adapter Supervisor framework |
| #5 | [#100](https://op.bzonfhir.com/work_packages/100) | Wire WebhookHandler ingress |
| #6 | [#98](https://op.bzonfhir.com/work_packages/98) | Wire HydrationService into production binary |
| #7 | [#92](https://op.bzonfhir.com/work_packages/92) | Wire RegisterAdminRoutes into production router |
| #8 | [#93](https://op.bzonfhir.com/work_packages/93) | Wire RegisterPublicRoutes for unauthenticated /metadata |
| #9 | [#105](https://op.bzonfhir.com/work_packages/105) | Replace PgAuditStore placeholder with hash-chained writer |
| #10 | [#94](https://op.bzonfhir.com/work_packages/94) | Wire observability.Start (metrics + OTel + audit + dead-letter reporter) |
| #11 | [#106](https://op.bzonfhir.com/work_packages/106) | Fix audit_log schema mismatch (code vs migration) |
| #12 | [#106](https://op.bzonfhir.com/work_packages/106) | Fix audit_log schema mismatch (code vs migration) |
| #13 | [#95](https://op.bzonfhir.com/work_packages/95) | Wire storage.Start (partition maintainer + retention sweeper) |
| #14 | [#101](https://op.bzonfhir.com/work_packages/101) | Wire WebSocket channel into ChannelRegistry and Lifecycle |
| #15 | [#102](https://op.bzonfhir.com/work_packages/102) | Wire Email channel into ChannelRegistry and Lifecycle |
| #16 | [#103](https://op.bzonfhir.com/work_packages/103) | Wire Message channel into ChannelRegistry and Lifecycle |
| #17 | [#101](https://op.bzonfhir.com/work_packages/101), [#102](https://op.bzonfhir.com/work_packages/102), [#103](https://op.bzonfhir.com/work_packages/103) | Wire WebSocket channel into ChannelRegistry and Lifecycle |
| #18 | [#104](https://op.bzonfhir.com/work_packages/104) | Wire per-client rate limiters (subscription create + WS bind-token mint) |
| #19 | [#111](https://op.bzonfhir.com/work_packages/111) | Serve TLS on the HTTP listener |
| #20 | [#112](https://op.bzonfhir.com/work_packages/112) | Plumb MLLP TLS / mTLS into buildMLLPListener |
| #21 | [#94](https://op.bzonfhir.com/work_packages/94) | Wire observability.Start (metrics + OTel + audit + dead-letter reporter) |
| #22 | [#94](https://op.bzonfhir.com/work_packages/94) | Wire observability.Start (metrics + OTel + audit + dead-letter reporter) |
| #23 | [#94](https://op.bzonfhir.com/work_packages/94) | Wire observability.Start (metrics + OTel + audit + dead-letter reporter) |
| #24 | [#168](https://op.bzonfhir.com/work_packages/168), [#169](https://op.bzonfhir.com/work_packages/169), [#170](https://op.bzonfhir.com/work_packages/170), [#171](https://op.bzonfhir.com/work_packages/171), [#172](https://op.bzonfhir.com/work_packages/172), [#173](https://op.bzonfhir.com/work_packages/173) | Implement vendor MapToFHIR for Cerner adapter |
| #25 | [#174](https://op.bzonfhir.com/work_packages/174) | Implement default-adapter MapToFHIR |
| #26 | [#113](https://op.bzonfhir.com/work_packages/113) | Register all vendor adapters at startup |
| #27 | [#114](https://op.bzonfhir.com/work_packages/114) | Implement real activation handshake (websocket + email) |
| #28 | [#179](https://op.bzonfhir.com/work_packages/179) | Set BaseURL/WSBaseURL respecting insecure and bind interface |
| #29 | [#180](https://op.bzonfhir.com/work_packages/180) | Make WSBindingTTL config-driven |
| #30 | [#178](https://op.bzonfhir.com/work_packages/178) | Wire Deps.* tunable knobs (page sizes, byte caps, FHIR version, JWKSURL) |
| #31 | [#181](https://op.bzonfhir.com/work_packages/181) | Mount /.well-known/jwks.json |
| #32 | [#179](https://op.bzonfhir.com/work_packages/179) | Set BaseURL/WSBaseURL respecting insecure and bind interface |
| #33 | [#112](https://op.bzonfhir.com/work_packages/112) | Plumb MLLP TLS / mTLS into buildMLLPListener |
| #34 | [#216](https://op.bzonfhir.com/work_packages/216) | Surface Postgres pool tunables |
| #35 | [#101](https://op.bzonfhir.com/work_packages/101) | Wire WebSocket channel into ChannelRegistry and Lifecycle |
| #36 | [#95](https://op.bzonfhir.com/work_packages/95) | Wire storage.Start (partition maintainer + retention sweeper) |
| #37 | [#224](https://op.bzonfhir.com/work_packages/224) | Remove or wire phantom infra packages (wakeup, queue) |
| #38 | [#225](https://op.bzonfhir.com/work_packages/225) | Remove or wire phantom engine packages (heartbeat, topicmatcher, topics/filter) |
| #39 | [#115](https://op.bzonfhir.com/work_packages/115) | Plumb topics catalog directory in Helm chart |
| #40 | [#166](https://op.bzonfhir.com/work_packages/166) | Document or remove the soft-deferred dead-letters CLI |
| #41 | [#116](https://op.bzonfhir.com/work_packages/116), [#117](https://op.bzonfhir.com/work_packages/117) | Tighten Validate() to require database/codec/auth/MLLP/topics |
| #42 | [#217](https://op.bzonfhir.com/work_packages/217) | Surface hardcoded ActivationTimeout, scheduler retry, processor poll |
| #43 | [#217](https://op.bzonfhir.com/work_packages/217) | Surface hardcoded ActivationTimeout, scheduler retry, processor poll |
| #44 | [#217](https://op.bzonfhir.com/work_packages/217) | Surface hardcoded ActivationTimeout, scheduler retry, processor poll |
| #45 | [#104](https://op.bzonfhir.com/work_packages/104) | Wire per-client rate limiters (subscription create + WS bind-token mint) |
| #46 | [#208](https://op.bzonfhir.com/work_packages/208) | Close rest-hook activator HTTP transport on shutdown |
| #47 | [#184](https://op.bzonfhir.com/work_packages/184) | Decouple AllowHTTP from AllowInsecureJWKS |
| #48 | [#226](https://op.bzonfhir.com/work_packages/226) | Make TrustedIssuers actually filter JWKS lookups |
| #49 | [#218](https://op.bzonfhir.com/work_packages/218) | Remove channel.Channel(nil) dummy reference |
| #50 | [#101](https://op.bzonfhir.com/work_packages/101), [#102](https://op.bzonfhir.com/work_packages/102), [#114](https://op.bzonfhir.com/work_packages/114) | Wire WebSocket channel into ChannelRegistry and Lifecycle |
| #51 | [#118](https://op.bzonfhir.com/work_packages/118) | Align Helm probe port with binary listener |
| #52 | [#119](https://op.bzonfhir.com/work_packages/119) | Implement ${env:VAR} and ${file:/path} placeholder interpolation |
| #53 | [#120](https://op.bzonfhir.com/work_packages/120) | Rewrite Helm chart config schema to match Config struct |
| #54 | [#118](https://op.bzonfhir.com/work_packages/118) | Align Helm probe port with binary listener |
| #55 | [#121](https://op.bzonfhir.com/work_packages/121) | Mount /metrics service + ServiceMonitor only after metrics endpoint exists |
| #56 | [#115](https://op.bzonfhir.com/work_packages/115) | Plumb topics catalog directory in Helm chart |
| #57 | [#111](https://op.bzonfhir.com/work_packages/111) | Serve TLS on the HTTP listener |
| #58 | [#125](https://op.bzonfhir.com/work_packages/125) | Rename or repurpose migrationInit init container |
| #59 | [#122](https://op.bzonfhir.com/work_packages/122) | Tighten NetworkPolicy default ingress to specific pod selectors |
| #60 | [#123](https://op.bzonfhir.com/work_packages/123) | Replace personal GHCR repo default in Helm chart |
| #61 | [#124](https://op.bzonfhir.com/work_packages/124) | Resolve replicaCount-vs-HPA collision in Helm chart |
| #62 | [#118](https://op.bzonfhir.com/work_packages/118) | Align Helm probe port with binary listener |
| #63 | [#126](https://op.bzonfhir.com/work_packages/126) | Always run integration tests on default PRs |
| #64 | [#127](https://op.bzonfhir.com/work_packages/127) | Always run e2e suite on default PRs |
| #65 | [#128](https://op.bzonfhir.com/work_packages/128) | Enforce coverage threshold in CI |
| #66 | [#129](https://op.bzonfhir.com/work_packages/129) | Sign release images with cosign |
| #67 | [#130](https://op.bzonfhir.com/work_packages/130) | Cross-platform image build on every PR |
| #68 | [#131](https://op.bzonfhir.com/work_packages/131) | Add dependabot/renovate plus govulncheck |
| #69 | [#132](https://op.bzonfhir.com/work_packages/132) | Pin and tune golangci-lint config |
| #70 | [#133](https://op.bzonfhir.com/work_packages/133) | Add nightly failure alerting |
| #71 | [#134](https://op.bzonfhir.com/work_packages/134) | Pin Go toolchain version exactly |
| #72 | [#135](https://op.bzonfhir.com/work_packages/135) | Smoke-run built image in CI |
| #73 | [#136](https://op.bzonfhir.com/work_packages/136) | Drop unused id-token release scope |
| #74 | [#137](https://op.bzonfhir.com/work_packages/137) | Add mkdocs build --strict on PRs |
| #75 | [#138](https://op.bzonfhir.com/work_packages/138) | Add key_version column to dead_letters |
| #76 | [#139](https://op.bzonfhir.com/work_packages/139) | Either persist deliveries.bundle or drop the column |
| #77 | [#140](https://op.bzonfhir.com/work_packages/140) | Move schema_migrations.checksum into a numbered migration |
| #78 | [#141](https://op.bzonfhir.com/work_packages/141) | Add NextEventNumber field to SubscriptionRow |
| #79 | [#142](https://op.bzonfhir.com/work_packages/142) | Wire subscription_topics retire SQL or drop retired_at |
| #80 | [#106](https://op.bzonfhir.com/work_packages/106) | Fix audit_log schema mismatch (code vs migration) |
| #81 | [#107](https://op.bzonfhir.com/work_packages/107) | Fix audit chain canonical input vs on-disk timestamp |
| #82 | [#143](https://op.bzonfhir.com/work_packages/143) | Drop redundant subscription_topics_url_idx |
| #83 | [#144](https://op.bzonfhir.com/work_packages/144) | Document or unify events_since_subscription_start vs next_event_number |
| #84 | [#145](https://op.bzonfhir.com/work_packages/145) | Replace 11 t.Skip merge-gate scenarios with real e2e tests |
| #85 | [#146](https://op.bzonfhir.com/work_packages/146) | Run real verifier.Middleware in e2e harness |
| #86 | [#114](https://op.bzonfhir.com/work_packages/114), [#147](https://op.bzonfhir.com/work_packages/147) | Implement real activation handshake (websocket + email) |
| #87 | [#105](https://op.bzonfhir.com/work_packages/105), [#148](https://op.bzonfhir.com/work_packages/148) | Replace PgAuditStore placeholder with hash-chained writer |
| #88 | [#149](https://op.bzonfhir.com/work_packages/149), [#168](https://op.bzonfhir.com/work_packages/168), [#169](https://op.bzonfhir.com/work_packages/169), [#170](https://op.bzonfhir.com/work_packages/170), [#171](https://op.bzonfhir.com/work_packages/171), [#172](https://op.bzonfhir.com/work_packages/172), [#173](https://op.bzonfhir.com/work_packages/173) | E2E exercises every vendor adapter MapToFHIR |
| #89 | [#113](https://op.bzonfhir.com/work_packages/113), [#149](https://op.bzonfhir.com/work_packages/149) | Register all vendor adapters at startup |
| #90 | [#150](https://op.bzonfhir.com/work_packages/150) | Stop bypassing handlers in RegisterSubscriber e2e helper |
| #91 | [#146](https://op.bzonfhir.com/work_packages/146) | Run real verifier.Middleware in e2e harness |
| #92 | [#151](https://op.bzonfhir.com/work_packages/151) | Wire SIGHUP config reload to whole config |
| #93 | [#152](https://op.bzonfhir.com/work_packages/152) | Wire WatchSecretFiles in production startup |
| #94 | [#168](https://op.bzonfhir.com/work_packages/168), [#169](https://op.bzonfhir.com/work_packages/169), [#170](https://op.bzonfhir.com/work_packages/170), [#171](https://op.bzonfhir.com/work_packages/171), [#172](https://op.bzonfhir.com/work_packages/172), [#173](https://op.bzonfhir.com/work_packages/173) | Implement vendor MapToFHIR for Cerner adapter |
| #95 | [#175](https://op.bzonfhir.com/work_packages/175) | Resolve direct-adapter capability lie |
| #96 | [#98](https://op.bzonfhir.com/work_packages/98) | Wire HydrationService into production binary |
| #97 | [#161](https://op.bzonfhir.com/work_packages/161) | Time-out / cap http.DefaultClient usage in demo CLIs |
| #98 | [#186](https://op.bzonfhir.com/work_packages/186) | Pass request context to URL validator DNS lookup |
| #99 | [#187](https://op.bzonfhir.com/work_packages/187) | Use request context for handler bookkeeping writes |
| #100 | [#188](https://op.bzonfhir.com/work_packages/188) | Add LIMIT to PgSubscriptionsStore.ListByClient |
| #101 | [#153](https://op.bzonfhir.com/work_packages/153) | Replace tests of unwired packages with integration tests |
| #102 | [#160](https://op.bzonfhir.com/work_packages/160) | Honor cfg.Deployment.LogFormat |
| #103 | [#154](https://op.bzonfhir.com/work_packages/154) | Make demo's documented happy path actually deliver |
| #104 | [#154](https://op.bzonfhir.com/work_packages/154) | Make demo's documented happy path actually deliver |
| #105 | [#154](https://op.bzonfhir.com/work_packages/154) | Make demo's documented happy path actually deliver |
| #106 | [#114](https://op.bzonfhir.com/work_packages/114) | Implement real activation handshake (websocket + email) |
| #107 | [#120](https://op.bzonfhir.com/work_packages/120), [#162](https://op.bzonfhir.com/work_packages/162) | Rewrite Helm chart config schema to match Config struct |
| #108 | [#119](https://op.bzonfhir.com/work_packages/119), [#209](https://op.bzonfhir.com/work_packages/209) | Implement ${env:VAR} and ${file:/path} placeholder interpolation |
| #109 | [#94](https://op.bzonfhir.com/work_packages/94), [#163](https://op.bzonfhir.com/work_packages/163), [#210](https://op.bzonfhir.com/work_packages/210) | Wire observability.Start (metrics + OTel + audit + dead-letter reporter) |
| #110 | [#167](https://op.bzonfhir.com/work_packages/167), [#212](https://op.bzonfhir.com/work_packages/212) | Allow --set on remaining auth knobs |
| #111 | [#93](https://op.bzonfhir.com/work_packages/93), [#116](https://op.bzonfhir.com/work_packages/116) | Wire RegisterPublicRoutes for unauthenticated /metadata |
| #112 | [#116](https://op.bzonfhir.com/work_packages/116), [#182](https://op.bzonfhir.com/work_packages/182) | Tighten Validate() to require database/codec/auth/MLLP/topics |
| #113 | [#109](https://op.bzonfhir.com/work_packages/109), [#176](https://op.bzonfhir.com/work_packages/176) | Bind AAD into codec AES-GCM envelope |
| #114 | [#110](https://op.bzonfhir.com/work_packages/110), [#177](https://op.bzonfhir.com/work_packages/177) | Make JTI replay check atomic (verifier + token endpoint) |
| #115 | [#110](https://op.bzonfhir.com/work_packages/110), [#178](https://op.bzonfhir.com/work_packages/178) | Make JTI replay check atomic (verifier + token endpoint) |
| #116 | [#101](https://op.bzonfhir.com/work_packages/101), [#155](https://op.bzonfhir.com/work_packages/155), [#198](https://op.bzonfhir.com/work_packages/198) | Wire WebSocket channel into ChannelRegistry and Lifecycle |
| #117 | [#92](https://op.bzonfhir.com/work_packages/92), [#156](https://op.bzonfhir.com/work_packages/156) | Wire RegisterAdminRoutes into production router |
| #118 | [#157](https://op.bzonfhir.com/work_packages/157), [#162](https://op.bzonfhir.com/work_packages/162), [#192](https://op.bzonfhir.com/work_packages/192) | Pick R5 OR R4B channel shape in demo-subscriber |
| #119 | [#158](https://op.bzonfhir.com/work_packages/158), [#203](https://op.bzonfhir.com/work_packages/203) | Stop importing e2e mocks from demo CLIs |
| #120 | [#101](https://op.bzonfhir.com/work_packages/101), [#159](https://op.bzonfhir.com/work_packages/159) | Wire WebSocket channel into ChannelRegistry and Lifecycle |
| #121 | [#94](https://op.bzonfhir.com/work_packages/94), [#101](https://op.bzonfhir.com/work_packages/101), [#164](https://op.bzonfhir.com/work_packages/164) | Wire observability.Start (metrics + OTel + audit + dead-letter reporter) |
| #122 | [#95](https://op.bzonfhir.com/work_packages/95), [#108](https://op.bzonfhir.com/work_packages/108), [#165](https://op.bzonfhir.com/work_packages/165) | Wire storage.Start (partition maintainer + retention sweeper) |
| #123 | [#108](https://op.bzonfhir.com/work_packages/108), [#164](https://op.bzonfhir.com/work_packages/164) | Fix audit chain re-anchor and hex-encoded prior_hash |
| #124 | [#116](https://op.bzonfhir.com/work_packages/116), [#118](https://op.bzonfhir.com/work_packages/118) | Tighten Validate() to require database/codec/auth/MLLP/topics |
| #125 | [#116](https://op.bzonfhir.com/work_packages/116), [#162](https://op.bzonfhir.com/work_packages/162), [#213](https://op.bzonfhir.com/work_packages/213) | Tighten Validate() to require database/codec/auth/MLLP/topics |
| #126 | [#94](https://op.bzonfhir.com/work_packages/94), [#167](https://op.bzonfhir.com/work_packages/167), [#213](https://op.bzonfhir.com/work_packages/213) | Wire observability.Start (metrics + OTel + audit + dead-letter reporter) |
| #127 | [#102](https://op.bzonfhir.com/work_packages/102), [#162](https://op.bzonfhir.com/work_packages/162) | Wire Email channel into ChannelRegistry and Lifecycle |
| #128 | [#183](https://op.bzonfhir.com/work_packages/183) | Block CG-NAT, IPv6 site-local, broadcast, IDN homoglyph hosts |
| #129 | [#182](https://op.bzonfhir.com/work_packages/182) | Re-validate subscriber URL at delivery time |
| #130 | [#184](https://op.bzonfhir.com/work_packages/184) | Decouple AllowHTTP from AllowInsecureJWKS |
| #131 | [#196](https://op.bzonfhir.com/work_packages/196) | Parse HL7 v2 timestamps with sub-second + offset support |
| #132 | [#196](https://op.bzonfhir.com/work_packages/196) | Parse HL7 v2 timestamps with sub-second + offset support |
| #133 | [#183](https://op.bzonfhir.com/work_packages/183) | Block CG-NAT, IPv6 site-local, broadcast, IDN homoglyph hosts |
| #134 | [#185](https://op.bzonfhir.com/work_packages/185) | Plumb URLValidator.AllowHosts from config |
| #135 | [#102](https://op.bzonfhir.com/work_packages/102) | Wire Email channel into ChannelRegistry and Lifecycle |
| #136 | [#101](https://op.bzonfhir.com/work_packages/101) | Wire WebSocket channel into ChannelRegistry and Lifecycle |
| #137 | [#191](https://op.bzonfhir.com/work_packages/191) | Hard-delete subscription on DELETE or return 410 Gone |
| #138 | [#95](https://op.bzonfhir.com/work_packages/95) | Wire storage.Start (partition maintainer + retention sweeper) |
| #139 | [#215](https://op.bzonfhir.com/work_packages/215) | Stop fixing partition trigger on now() instead of created_at |
| #140 | [#109](https://op.bzonfhir.com/work_packages/109) | Bind AAD into codec AES-GCM envelope |
| #141 | [#104](https://op.bzonfhir.com/work_packages/104) | Wire per-client rate limiters (subscription create + WS bind-token mint) |
| #142 | [#202](https://op.bzonfhir.com/work_packages/202) | Single-flight JWKS fetch under burst |
| #143 | [#101](https://op.bzonfhir.com/work_packages/101) | Wire WebSocket channel into ChannelRegistry and Lifecycle |
| #144 | [#227](https://op.bzonfhir.com/work_packages/227) | Tighten MLLP framer pending-buffer cap check |
| #145 | [#228](https://op.bzonfhir.com/work_packages/228) | Document MLLP startRead readCh buffering invariant |
| #146 | [#101](https://op.bzonfhir.com/work_packages/101) | Wire WebSocket channel into ChannelRegistry and Lifecycle |
| #147 | [#221](https://op.bzonfhir.com/work_packages/221) | Streamline diagnosticForReason to cover all error paths |
| #148 | [#95](https://op.bzonfhir.com/work_packages/95) | Wire storage.Start (partition maintainer + retention sweeper) |
| #149 | [#220](https://op.bzonfhir.com/work_packages/220) | Drop GenesisHash deprecation or document migration |
| #150 | [#211](https://op.bzonfhir.com/work_packages/211) | Make Version/Commit immutable build-info constants |
| #151 | [#229](https://op.bzonfhir.com/work_packages/229) | Pin mkdocs and transitive doc-build dependencies |
| #152 | [#229](https://op.bzonfhir.com/work_packages/229) | Pin mkdocs and transitive doc-build dependencies |
| #153 | [#230](https://op.bzonfhir.com/work_packages/230) | Add demo bridge healthcheck |
| #154 | [#106](https://op.bzonfhir.com/work_packages/106) | Fix audit_log schema mismatch (code vs migration) |
| #155 | [#107](https://op.bzonfhir.com/work_packages/107) | Fix audit chain canonical input vs on-disk timestamp |
| #156 | [#213](https://op.bzonfhir.com/work_packages/213) | Drop dead direct-require packages from go.mod |
| #157 | [#213](https://op.bzonfhir.com/work_packages/213) | Drop dead direct-require packages from go.mod |
| #158 | [#214](https://op.bzonfhir.com/work_packages/214) | Make every migration body idempotent |
| #159 | [#214](https://op.bzonfhir.com/work_packages/214) | Make every migration body idempotent |
| #160 | [#206](https://op.bzonfhir.com/work_packages/206) | Plumb caller ctx into lifecycle sequencer |
| #161 | [#207](https://op.bzonfhir.com/work_packages/207) | Register production shutdown hooks against lifecycle phases |
| #162 | [#93](https://op.bzonfhir.com/work_packages/93) | Wire RegisterPublicRoutes for unauthenticated /metadata |
| #163 | [#197](https://op.bzonfhir.com/work_packages/197) | Filter $events by client_id |
| #164 | [#190](https://op.bzonfhir.com/work_packages/190) | Bound parseRetryAfter (max + min floor) |
| #165 | [#92](https://op.bzonfhir.com/work_packages/92) | Wire RegisterAdminRoutes into production router |
| #166 | [#92](https://op.bzonfhir.com/work_packages/92) | Wire RegisterAdminRoutes into production router |
| #167 | [#92](https://op.bzonfhir.com/work_packages/92) | Wire RegisterAdminRoutes into production router |
| #168 | [#189](https://op.bzonfhir.com/work_packages/189) | Tighten parseCountParam from Sscanf to strconv.Atoi |
| #169 | [#195](https://op.bzonfhir.com/work_packages/195) | Match MSH segment ID case-insensitively |
| #170 | [#194](https://op.bzonfhir.com/work_packages/194) | Honor MSH-2 escape characters in MSH-7 parser |
| #171 | [#191](https://op.bzonfhir.com/work_packages/191) | Hard-delete subscription on DELETE or return 410 Gone |
| #172 | [#193](https://op.bzonfhir.com/work_packages/193) | Use absolute extension URLs in CapabilityStatement |
| #173 | [#162](https://op.bzonfhir.com/work_packages/162), [#192](https://op.bzonfhir.com/work_packages/192) | Rewrite docs/architecture.md to match actual Config |
| #174 | [#231](https://op.bzonfhir.com/work_packages/231) | Make audit-verify CLI work against the production audit chain |
| #175 | [#204](https://op.bzonfhir.com/work_packages/204) | Logging redactor: cover endpoint paths and value-side PHI |
| #176 | [#204](https://op.bzonfhir.com/work_packages/204) | Logging redactor: cover endpoint paths and value-side PHI |
| #177 | [#160](https://op.bzonfhir.com/work_packages/160) | Honor cfg.Deployment.LogFormat |
| #178 | [#200](https://op.bzonfhir.com/work_packages/200) | Lock recoverStuck against multi-pod double-increment |
| #179 | [#200](https://op.bzonfhir.com/work_packages/200) | Lock recoverStuck against multi-pod double-increment |
| #180 | [#201](https://op.bzonfhir.com/work_packages/201) | Move pipeline workers under pipelineWG before MarkStartupComplete |
| #181 | [#162](https://op.bzonfhir.com/work_packages/162), [#219](https://op.bzonfhir.com/work_packages/219) | Rewrite docs/architecture.md to match actual Config |
| #182 | [#92](https://op.bzonfhir.com/work_packages/92), [#162](https://op.bzonfhir.com/work_packages/162), [#219](https://op.bzonfhir.com/work_packages/219) | Wire RegisterAdminRoutes into production router |
| #183 | [#92](https://op.bzonfhir.com/work_packages/92), [#223](https://op.bzonfhir.com/work_packages/223) | Wire RegisterAdminRoutes into production router |
| #184 | [#102](https://op.bzonfhir.com/work_packages/102) | Wire Email channel into ChannelRegistry and Lifecycle |
| #185 | [#102](https://op.bzonfhir.com/work_packages/102), [#203](https://op.bzonfhir.com/work_packages/203) | Wire Email channel into ChannelRegistry and Lifecycle |
| #186 | [#93](https://op.bzonfhir.com/work_packages/93) | Wire RegisterPublicRoutes for unauthenticated /metadata |
| #187 | [#214](https://op.bzonfhir.com/work_packages/214) | Make every migration body idempotent |
| #188 | [#214](https://op.bzonfhir.com/work_packages/214) | Make every migration body idempotent |
| #189 | [#232](https://op.bzonfhir.com/work_packages/232) | Scope StreamActiveByTopic to prevent cross-tenant secret disclosure |
| #190 | [#233](https://op.bzonfhir.com/work_packages/233) | Honor strong-ETag If-Match in subscription update |
| #191 | [#104](https://op.bzonfhir.com/work_packages/104) | Wire per-client rate limiters (subscription create + WS bind-token mint) |
| #192 | [#234](https://op.bzonfhir.com/work_packages/234) | Populate additionalContext on full-resource notification Bundles |
| #193 | [#222](https://op.bzonfhir.com/work_packages/222) | Replace string-match isPermanentBuildError with typed errors |
| #194 | [#190](https://op.bzonfhir.com/work_packages/190) | Bound parseRetryAfter (max + min floor) |
| #195 | [#162](https://op.bzonfhir.com/work_packages/162), [#219](https://op.bzonfhir.com/work_packages/219) | Rewrite docs/architecture.md to match actual Config |
| #196 | [#199](https://op.bzonfhir.com/work_packages/199) | Surface scheduler tunables (RecoveryInterval, StuckThreshold, DispatchConcurrency) |
| #197 | [#205](https://op.bzonfhir.com/work_packages/205) | Strict yaml.Decoder.KnownFields for config |
| #198 | [#183](https://op.bzonfhir.com/work_packages/183) | Block CG-NAT, IPv6 site-local, broadcast, IDN homoglyph hosts |
| #199 | [#204](https://op.bzonfhir.com/work_packages/204) | Logging redactor: cover endpoint paths and value-side PHI |

## Notes on duplicate finding numbers

Findings #108–#127 appear in BOTH `production-readiness-honesty-audit-supplement.md` and `production-readiness-honesty-audit-supplement-2.md` with DIFFERENT content. Per the user's authoritative numbering (file 2: #51–#107; file 3: #108–#157), the supplement-2 version is canonical for #108+. Where the supplement-1 version of those IDs was a distinct finding (e.g., the architecture.md fiction in supplement-1's #107/#108), it is covered by the docs/architecture.md rewrite story (#162).

## Verification

Run `~/cz/claude-helper-cli/scripts/op-board.sh tree 91` to inspect the live epic.