# Demo SubscriptionTopic catalog

This directory holds illustrative `SubscriptionTopic` resources that drive
the subscription-sidecar demo (`docs/subscription-sidecar-demo.md`). The
demo publisher (`cmd/demo-publisher`) and demo subscriber
(`cmd/demo-subscriber`) reference the topic URLs declared here; the
bridge loads these files into its topic catalog at startup so the demo
runs against the same code path production deployments use.

## Files

| File | URL | Resource | Purpose |
|---|---|---|---|
| `lab-results.json` | `http://demo.org/topics/lab-results` | `Observation` | Final laboratory results (ORU^R01 walkthroughs). |
| `encounter-admit.json` | `http://demo.org/topics/encounter-admit` | `Encounter` | Encounters entering the in-progress state (ADT^A01 walkthroughs). |
| `vitals.json` | `http://demo.org/topics/vitals` | `Observation` | Final vital-sign observations; second `Observation` topic so demos can show topic-level routing. |

## Loading

The bridge loader (`cmd/fhir-subs/topics.go`) walks a configured
directory non-recursively and treats every `*.json` file as one
operator-precedence raw topic, then hands them to
`internal/topics/catalog.Load`. Point the `topics.dir` config at this
directory (or copy these files into your operator catalog dir) to load
the demo topics.

`internal/topics/catalog/demo_topics_test.go` loads every file in this
directory and asserts the catalog accepts all of them. Run it with:

```
go test ./internal/topics/catalog/ -run TestDemoTopicCatalogLoads
```

## Adding a topic

1. Drop a new `*.json` file in this directory. The filename does not
   matter; the canonical URL inside does.
2. The body must be a `SubscriptionTopic` resource that satisfies
   `internal/topics/catalog/schemas/subscription_topic.schema.json`. In
   particular:
   - `resourceType` MUST be `"SubscriptionTopic"`.
   - `url` and `version` are required and form the (url, version)
     identity used by the catalog.
   - `status` must be one of `draft | active | retired | unknown`.
3. `queryCriteria.previous` and `queryCriteria.current` may only
   reference search parameters from the matcher's whitelisted shortlist
   (see `catalog.SupportedSearchParameters()`). At time of writing the
   shortlist is:

   `status, subject, patient, code, category, name, _lastUpdated`

   Any other parameter is rejected at catalog load (B-23) — the topic
   never enters the catalog and the demo will fail loud rather than
   silently never matching.
4. `canFilterBy.filterParameter` is constrained by the same shortlist.
5. `notificationShape` may declare at most one entry; multi-entry
   shapes are rejected at load (S-11.3).
6. Re-run the demo-topic test above. A green test means the production
   loader will accept the file unchanged.

## YAML?

The `docs/subscription-sidecar-demo.md` Gap 6 example is written in YAML
for readability, but the production loader consumes JSON only. If you
want to author topics in YAML, convert them to JSON before checking
them in here.
