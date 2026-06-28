# Recording Setup

The Community Call is built on a deliberately small, boring toolchain — the goal is "we never debate tools during the call." Anything here can be replaced, but only with a maintainer PR and a community-call discussion.

## Meeting platform

**Choice: Google Meet (default), Zoom (fallback).**

Google Meet is the default because:

- Free for the meeting itself
- One-click join from the browser, no client install required
- Built-in live captions
- Recording + auto-transcript on Google Workspace plans the maintainers already have
- Calendar integration is trivial

Zoom is the fallback when the call expects > 100 attendees (Meet's free tier caps lower and Workspace caps vary), or when a presenter has a hard requirement (e.g., needs Zoom-specific screen-share quality).

The active link goes in the per-call document (e.g., [`2026-Q3-call.md`](./2026-Q3-call.md)). Do not put the long-lived link in this file — it should rotate per call to limit zoom-bombing risk.

## Registration form

**Choice: Google Forms.**

Google Forms wins over Eventbrite for this use case because:

- No charge, no signup wall for attendees
- Forms responses land in a Google Sheet the facilitator can sort / dedupe questions from
- Confirmation email + calendar invite can be wired via a small Apps Script — see the snippet below
- The maintainers already have Workspace, so there's no new vendor

Eventbrite would be considered if we later want paid sponsorships, sponsor visibility, or stricter attendance caps.

### Form fields (minimum)

- Name (required)
- Email (required, for the join link)
- Organization (optional)
- "Are you OK being on a recording posted to YouTube if you speak during Q&A?" (Yes / No / Only audio)
- "Any questions for the maintainers?" (free text, optional)
- "Want to be considered for a future community spotlight?" (Yes / No, optional)

### Confirmation email

The Apps Script trigger on form submit sends a confirmation email with:

- The meeting link
- The calendar `.ics` attachment
- The Code of Conduct link
- A reminder the call is recorded

## Recording

- **Live recording:** native to the meeting platform (Meet Workspace recording, Zoom cloud recording).
- **Live captions:** on by default. Verify at the T-30 check.
- **Backup recording:** the recording lead also runs a local OBS recording as a fallback in case the cloud recording fails. The OBS recording is deleted once the cloud recording is confirmed good.

## Publishing

**Choice: YouTube — public, unlisted by default during review.**

Per-call workflow:

1. Recording lead downloads the raw recording from Meet / Zoom
2. Trim pre-call and post-call chatter (no content edits)
3. Upload to the project's YouTube channel as **Unlisted**
4. Replace the auto-transcript with the human-reviewed one
5. Add chapter markers matching the agenda segments
6. Flip to **Public** once a second maintainer signs off
7. Paste the YouTube + transcript links into the per-call document and into `docs.bzonfhir.com/community-calls`

Why YouTube, not Vimeo / self-hosted:

- Free, high reliability
- Auto-generated captions + transcripts that we can edit
- Built-in chapter markers
- Best discoverability for an open-source community
- No bandwidth costs we'd have to monitor

## Slides / shared docs

- Maintainers use a shared Google Slides deck (one deck per call, copied from a template)
- Community spotlight presenters can bring their own slides (any format that the meeting platform can screen-share)
- All slide decks get attached to the per-call document under "Links" after the call

## Comms automation

The post-call recap is sent through the channels listed in [`communication-channels.md`](./communication-channels.md). The recording lead is responsible for triggering it once the YouTube upload is public.
