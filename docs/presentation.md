# FHIR Subscriptions Sidecar

# A 2-minute Talk



































## 1. The Problem

FHIR Subscriptions are a great spec — but they aren't being implemented in the
primary EHRs used by hospitals and clinics today.

If you're an external application, registry, or analytics platform that wants
to react to clinical events in an EHR, you're stuck waiting on the vendor's
roadmap. That wait can be years.

































---

## 2. The Idea

After Gino's Subscriptions talk on Tuesday Morning, we got to talking and landed on a
simple idea:

> **A standalone, open-source "sidecar" server that sits between FHIR
> Subscribers on one side and the facility's EHR on the other.**

- **Upstream** it speaks the standard FHIR Subscriptions API.
- **Downstream** it talks to the EHR using whatever the EHR actually
  provides — HL7 v2, FHIR REST polling, vendor APIs, etc.

Facilities get FHIR Subscriptions today, without waiting on their EHR vendor.



























---

## 3. Progress So Far

Over the course of this conference I was able to:

- Write out the design (high-level concept, architecture, and component-level
  design docs are all in the repo).
- Stand up a **proof-of-concept implementation** of the service — the FHIR
  Subscriptions surface, the adapter SPI, and the channel SPI are all in
  place.





























---

## 4. Next Steps

- **Better documentation** for implementing EHR adapters — a clear,
  opinionated guide so a vendor or community contributor can build an adapter
  for a new EHR without reverse-engineering the codebase.
- **Implementing and testing EHR adapters** Getting more and more adapters built and tested against various EHRs with real data.



































---

## 5. Call for Help from the Community

Two specific asks:

1. **EHR adapter contributors** — folks who can build adapters for EHRs we haven't covered yet.
2. **Adapter testers** — folks with access to a given EHR who can validate an adapter against real (or sandboxed) EHR instances.





























---

## 6. Get Involved

- **Repo:** https://github.com/bzimbelman/fhir-ehr-subscriptions-service
- **Chat:** find us on [chat.fhir.org](https://chat.fhir.org) with questions
- **Me:** Brian Zimbelman — bzimbelman@natera.com

























