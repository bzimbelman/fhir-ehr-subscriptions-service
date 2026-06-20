# Admin UI — Product Requirements Document

**Status:** Draft · 2026-06-18
**Source ticket:** [OpenProject #38](https://op.bzonfhir.com/work_packages/38)
**Audience:** the architect/engineer who builds this; the hospital IT operator who uses it.

---

## 1. Overview

A self-contained operator console the `fhir-ehr-subscriptions-service` sidecar. It allows the IT admin user to view the status of both the subscriptions (what subscriptions do we have active and what messages were sent to them) and the ehr adapter interfaces (activity statuses things like messages in the last x period of time, connected, etc.).

The goals of this tool are:

- To quickly answer the question is the subscriptions sidecar up and working.
- To provide a way to trace what happened to a specific event, what subscriptions did it go to, when were the messages sent, etc.
- To provide a way to debug what happened to a specific event for a specific subscription (Was the subscription active at that time? Did it match? Did an event get created? Did it get sent successfully? Etc.)

---

## 2. EHR Adapter health

Each EHR adapter will have at least one of the inbound mechanisms and most will have many of them, some will have all of them. Some will have many HL7 interfaces, while others will only have one. We should be able to handle all of these scenarios in an interface that is easy to consume. I.e. we should be able to rollup the interface for the entire adapter, is the entire adapter working correctly (only yes if every expected connection is connected and receiving data, partially if some connections are having issues, no if the entire adapter is down). If the user wants more detail they should be able to drill down into each individual sub-system hl7, FHIR, custom apis, etc. Each system should show the same amount of detail. If this is the lowest level for this interface, it should have numbers associated with it and detailed views of the traffic. If not, then it should have statuses for each sub interface and the user should be able to drill down again until they get to the lowest level.

---

## 3. Subscriptions

We should be able to show a status of the subscriptions how many are connected (if their a connected type, email would be connected by default although we could show the email configuration is or is not working). Custom connections should provide a mechanism for getting the information we would need for is it connected.

Then we should be able to see all of the subscriptions in more detail so if there are five subscriptions we should be able to see things like when the subscription last processed a message, did the message succeed, etc. It would be nice to be able to see all messages sent on this subscription during a period of time (within a rolling window of course).

---

## 4. Dead-letter Triage

We should show if we have any messages in the dead letter queue. If so, then we should be able to review when the message was received and all the attempts that were made to process the message and the condition of the message. We should also be able to:

- Hand edit/modify the message
- Kill the message
- Resubmit the message for processing

---

## 5. Logs viewer

We only want to have this if it is better than just viewing the logs in the standard output log viewer the company will automatically have. 

---

## 6. Tracing tool

Being able to select a event or message and trace it back to the source EHR adapter and message and forward to the subscription messages created. 

---

## 7. Matching tool

Allows the user to select a subscription and a prior message and view how it goes through the matching process, where it fails to match, etc.

---


