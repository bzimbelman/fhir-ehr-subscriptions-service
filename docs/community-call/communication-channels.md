# Communication Channels

Where the call gets announced, where attendees get reminded, and where the recap lands. The list is intentionally short: one channel per job.

## Announcement channels

| Channel | Purpose | Owner | Cadence |
|---|---|---|---|
| GitHub Discussions — `Announcements` category | Primary source of truth: each call has its own pinned announcement thread with date, registration link, and agenda preview | Facilitator | 4 weeks before, 2 weeks before, 1 week before, day-of |
| Project mailing list (TBD) | Email reminder for people who don't watch GitHub | Facilitator | 2 weeks before + day-of |
| `docs.bzonfhir.com` banner | Site-wide banner linking to the next call's registration | Docs maintainer | Up from 4 weeks before until the call starts |
| Project README ("Community" section) | Static link to this directory and to `docs.bzonfhir.com/community-calls` | Maintainers | Updated each quarter when the per-call doc lands |

Anything posted to one of these channels should be a one-click path from "I saw the announcement" to "I am registered." If a step requires explanation, the announcement is too thin — link to [`attendee-runbook.md`](./attendee-runbook.md) instead.

## During-call channels

- **In-meeting chat** — the only real-time channel during the call itself. Questions and reactions go here. Captured by the notetaker.
- **No parallel Slack / Discord during the call.** Splitting attention across chats is how Q&A gets dropped. If we ever stand up a project chat, the community-call rule is "use the meeting chat only during the call."

## Recap channels

After the call (within 5 business days, per the [Facilitator Runbook](./facilitator-runbook.md)):

| Channel | What gets posted | Owner |
|---|---|---|
| GitHub Discussions — recap thread under the original announcement | Bullet recap, action items, deferred questions, links to recording + transcript | Notetaker |
| `docs.bzonfhir.com/community-calls` | Permanent index page entry for the call: title, date, embedded YouTube, transcript link | Docs maintainer |
| YouTube channel | The recording itself (Public after a second-maintainer sign-off — see [`recording-setup.md`](./recording-setup.md)) | Recording lead |
| Mailing list | Short "the call happened, here's the recording" note | Facilitator |
| The per-call document in this directory (e.g., [`2026-Q3-call.md`](./2026-Q3-call.md)) | Links to recording, transcript, slides; action items table filled in | Notetaker |

## Spotlight recruitment

- **Channel:** GitHub Discussions — `Show and tell` category
- **Cadence:** A "Spotlight call for Q<N>" thread opens 4 weeks before each call. Volunteers reply with a 1-paragraph pitch + a link to what they've built. The facilitator picks one (rotating if more than one).

## Confidentiality / off-record requests

The call is recorded and public. If a topic should stay off-record:

- Ask the facilitator before the call — they'll either defer the topic or arrange a separate maintainer-only follow-up
- Do not post sensitive details into the meeting chat; chats are captured into the public recap

## Updating this list

Adding a channel (e.g., a Discord, a Mastodon account) requires:

1. A PR to this file naming the channel + an owner with publish rights
2. Discussion in a community call before the channel is treated as official
3. An update to the maintainer-side handoff doc so the owner role is named, not just "someone"
