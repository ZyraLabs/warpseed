warpseed 1.1.2 — big queues no longer go dark

**If you queued a few hundred files and the queue dock, Flight and Activity
views all went quiet while the transfers plainly carried on, this is the
fix.** Thank you to the tester who sent the screenshot.

## Fixed

- **Queues over 200 rows showed nothing in flight.** The UI asked the
  database for the newest 200 rows, but transfers are picked up oldest
  first. Once more than 200 items were waiting, every row actually moving
  fell outside that window: the dock read "0 active", Flight had no lanes,
  Activity had no live cards, the mini pill was blank, and completions
  logged as "transfer #412" with no name — all while the bytes kept landing.
  The list now always includes everything in flight, the next 2,000 waiting
  rows in the order they will be picked up, the newest 500 failed rows and
  the newest 200 finished ones, so what is moving is always on screen no
  matter how much is queued behind it. Running rows also pin to the top of
  the dock, so a long backlog never buries them.
- **The dock stays smooth with thousands of rows.** Only the rows in view
  are drawn, so a 2000-file queue costs the same to update as a 20-file
  one. Bursts of completions are folded into one refresh instead of one
  per file.
- **Flight's queued rail** shows the next 60 in pick-up order and says how
  many more are waiting, rather than listing every file.
- **Reading the queue no longer slows the transfers.** The database read
  behind the dock sorted every finished row you had ever accumulated, on the
  same connection the transfers write their checkpoints through. It now
  walks an index and stops at the window, however large the history.
- A transfer picked up straight after queuing is named in the session log
  from the start instead of appearing as "transfer #412".

## Notes

- The windows above cap what is listed, not what is queued. Past 2,000
  waiting rows the strip's queued count stops at 2,000 and the Deck's
  disk-fit estimate only counts the rows it can see; everything is still
  transferred.
- The queue database gains one index on first launch (schema v9); the
  upgrade is instant and one-way, as with every previous version.
