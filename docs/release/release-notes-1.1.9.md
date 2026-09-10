warpseed 1.1.9 — a dropped connection is no longer fatal

**If an upload or download failed with "connection lost" and stayed failed,
this is the fix.** Two separate faults, both reported from a real seedbox.

## Fixed

- **A lost connection failed the transfer outright instead of retrying it.**
  warpseed decides whether a failure is worth retrying by reading the error,
  and the phrase the SFTP library uses for a dead session — "connection lost" —
  matched none of the patterns it knew. So the single most transient failure
  there is, a connection dropping mid-transfer, was treated as permanent: the
  row went straight to failed and sat there. It now retries with backoff like
  any other network blip. Nothing was ever lost — the part-file and its
  checkpoints survive, so a manual retry always resumed correctly — but you had
  to notice and do it yourself.

- **An upload and a download could not run at the same time.** The shipped lane
  counts are 4 for downloads and 3 for uploads, which needs 7 connections, and
  the per-site budget was 6. Whichever direction claimed the connections first
  held them until it finished while the other waited its turn. The budget is now
  8, so both run — and if you had set the value yourself, warpseed leaves your
  number alone.

- **A transfer waiting for connections blocked the other direction entirely.**
  When a transfer could not fit, warpseed stopped considering every other
  transfer for that server, uploads and downloads alike. Queue order is a
  promise about what runs next in *one* direction; it was never meant to let a
  waiting upload freeze your downloads.

## Notes

- Raising the budget means warpseed may open more connections to a server than
  before. It still backs off on its own: when a server refuses the extras, that
  limit is remembered and the queue sizes itself to what the server actually
  gives.
- If your provider is strict about connection counts, Settings → Transfers →
  Connections per site is the number to lower.
