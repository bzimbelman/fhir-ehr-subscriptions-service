# Facilitator Runbook

This is the host's playbook. Follow it end-to-end and the call runs itself.

## Roles

| Role | Owner | Responsibility |
|---|---|---|
| Facilitator | One maintainer | Runs the agenda, watches the clock, surfaces questions |
| Notetaker | One maintainer | Captures action items + deferred questions into the per-call doc |
| Recording lead | One maintainer | Confirms recording is rolling at start, stops it at end, handles upload |
| Maintainer presenters | 1-3 maintainers | Own the 25min product update |
| Spotlight presenter | 1 community member | Owns the 15min spotlight slot |

The facilitator and notetaker should not be the same person — they have competing attention loads.

## 4 weeks before the call

- [ ] Confirm the date and post it in [`communication-channels.md`](./communication-channels.md)'s channels (Discussions announcement, mailing list, site banner)
- [ ] Open / refresh the public registration form (see [`recording-setup.md`](./recording-setup.md))
- [ ] Open a "Spotlight call for Q<N>" thread in GitHub Discussions; invite contributors to volunteer
- [ ] Copy [`agenda-template.md`](./agenda-template.md) to `YYYY-Q<N>-call.md` and fill in date / link placeholders

## 2 weeks before the call

- [ ] Pick the spotlight presenter from the Discussions thread (first qualified volunteer, or rotate if more than one)
- [ ] Confirm the presenter has slides and can demo over the meeting platform; share the [Recording Setup](./recording-setup.md) prereqs
- [ ] Draft the "shipped this quarter" list — pull from the merged-PR list and release notes
- [ ] Identify 2-3 "in-flight" items the maintainers will discuss
- [ ] Post a second announcement linking to the registration form

## Spotlight prep

- [ ] Send the presenter the [Attendee Runbook](./attendee-runbook.md) for the basics
- [ ] Schedule a 15min tech check 3-5 days before the call (audio, screen share, slide handoff)
- [ ] Confirm they're OK being recorded and that the recording will be public on YouTube
- [ ] Make sure their slides — if any — are accessible (alt text on screenshots, readable contrast)

## 1 week before the call

- [ ] Final reminder in all announcement channels with the join link
- [ ] Triage submitted questions from the registration form — group duplicates, surface the 5-6 best for the Q&A
- [ ] Confirm the per-call doc is up to date with the agenda, presenter names, and submitted questions
- [ ] Verify the YouTube channel (or whichever upload destination is in [`recording-setup.md`](./recording-setup.md)) is reachable and the recording lead has upload rights

## Day of the call

### T-30 minutes

- [ ] Open the meeting platform, run an audio / video / screen-share check with maintainers and the spotlight presenter
- [ ] Confirm the recording lead has cloud recording enabled and transcript capture is on
- [ ] Open the per-call doc in a side-by-side window for live note-taking
- [ ] Pin a chat message with the Code of Conduct link and a reminder that the call is being recorded

### T-0 (start of call)

- [ ] Recording lead starts recording — verbally confirm "we are recording" on air
- [ ] Welcome attendees and walk through the agenda in 30 seconds
- [ ] Run the agenda in [`agenda-template.md`](./agenda-template.md); cut segments short rather than overrunning

### During the call

- [ ] Facilitator watches the clock — 25min for the update segment is a hard cap
- [ ] Notetaker types action items into the per-call doc's table in real time
- [ ] Notetaker captures any unanswered questions into the "Open questions (deferred)" section
- [ ] If the meeting platform's chat surfaces a question the facilitator missed, raise hand or interject between segments

### T+60 (end of call)

- [ ] Recap action items aloud
- [ ] Thank the spotlight presenter
- [ ] Announce the next call's date
- [ ] Recording lead stops the recording

## After the call (within 5 business days)

- [ ] Recording lead reviews + edits the recording (trim pre/post chatter; do not edit content)
- [ ] Upload to YouTube; double-check captions and the auto-generated transcript
- [ ] Update the per-call doc: paste the YouTube + transcript links into the "Links" section
- [ ] Update `docs.bzonfhir.com/community-calls` to link the new recording (see [`communication-channels.md`](./communication-channels.md) for who owns this)
- [ ] Post a recap in GitHub Discussions: link to recording, action items, deferred questions
- [ ] Open follow-up threads for any deferred Q&A and tag the original asker
- [ ] File issues / PRs for action items so they don't get lost

## After-action retro (within 2 weeks)

- [ ] 30min maintainer-only retro: what worked, what didn't, what to change for next quarter
- [ ] Update this runbook or the agenda template with anything that changed
- [ ] Confirm the next call's date is on the calendar and announced
