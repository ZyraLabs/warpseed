warpseed 1.1.3 — lanes you actually get, and one button for a failed batch

**If Hyperlane said 4 lanes but a queue of large files crawled at one
connection's speed each, this is the fix.** Thank you to the tester who
worked out the connection arithmetic before we did, and who lost a batch to
a hard drive unplugged mid-run.

## Fixed

- **A queue of large files ran every file on one lane.** Transfers took
  whatever connections happened to be spare rather than waiting for the lane
  count you set, so the first file claimed the budget and everything behind
  it started narrow. Files now wait for their full lane count: a queue of
  large files runs them one at a time at full width instead of all of them
  at one connection's speed. Queue order is respected per site, so the file
  at the front no longer waits behind smaller ones that fit.
- **A part-transferred large file is no longer restarted when the server is
  short of connections.** A Hyperlane download that had to fall back to a
  single connection deleted its multi-lane progress before starting over —
  on a 50 GB file that is tens of gigabytes re-downloaded, and a server
  granting one connection where several were asked for is ordinary. It now
  waits and retries with its progress intact instead.
- **A server that refuses connections no longer stalls the queue.** When a
  server grants fewer connections than asked for, warpseed now remembers
  what it actually got and sizes the following transfers to match, instead
  of holding the rest of the queue back waiting for a width that server was
  never going to give. The observed limit is forgotten once the site goes
  idle or goes stale, so a momentary refusal cannot hold a site at one
  connection for the rest of an overnight run.
- **The Transfers settings did not say they were connection budgets.**
  "Concurrent transfers" and "Per-site default" count connections, not
  files, and Hyperlane draws its lanes from them — so setting them to 1 to
  "give one file all the lanes" silently turned Hyperlane off, which is the
  exact opposite of what it reads like. They are now **Connections, all
  sites** and **Connections per site**, the section says what they do, and
  the lane field tells you when your budget is limiting it and what to raise
  it to.

## New

- **Retry failed** and **Clear failed** in the queue toolbar, shown whenever
  anything has failed. One unplugged drive or one hour of a server refusing
  logins fails a whole batch, and clearing it a row at a time was the
  reported pain. Retry requeues everything and each file resumes from the
  byte it reached; Clear removes the rows and the part-downloaded data with
  them, so nothing is left behind on disk.

## Notes

- Nothing about your saved settings changes — the two Transfers numbers mean
  what they always meant, they are just named and explained correctly now.
  If you had lowered them to force more lanes, raise them back: the lane
  count can never exceed them, and Settings now says so under the field.
- **Clear failed deletes part-downloaded data.** Those files start from the
  beginning if you queue them again. Finished files are never touched, and
  the confirmation says so before anything is removed. It clears exactly the
  rows it counted, so anything that fails while the confirmation is open is
  left for you to look at rather than swept away with the rest.
- **Clear failed keeps a row whose data it cannot reach** rather than
  deleting the record and stranding the file. That means failed uploads
  while the site is disconnected — connect it and clear again. It also
  leaves the part-file alone when another queued copy of the same
  destination is still using it, so re-queuing a folder and then tidying the
  red rows no longer resets the new copy to zero.
- If the log says something like `site 1 granted 4/6 connections`, that is
  your server refusing the extras, not warpseed giving up — it is the
  quickest way to find the connection ceiling your provider actually
  enforces, and a good number to set *Connections per site* to.
