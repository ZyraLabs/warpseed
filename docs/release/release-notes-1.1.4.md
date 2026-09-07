warpseed 1.1.4 — the data-safety pass

**No new features. This release closes every remaining way the queue could
lose, waste or misplace your bytes**, including one that could publish a file
of exactly the right size holding the wrong contents. If you run overnight
transfers, take this one.

## Fixed

- **A resumed Hyperlane download now proves its progress is real.** The check
  was the part file's size — which proves nothing, because the file is
  created at its full size before the first byte arrives. Anything that
  replaced its contents without changing its length passed: a folder restored
  from a backup or Windows' Previous Versions, another tool writing the same
  path, or leftovers from an attempt at a different file of the same size.
  The ranges already marked done were never fetched again, and the finished
  file had the right length and the wrong bytes. Each completed range is now
  read back from the server and compared before anything is resumed — the
  check uploads have had all along.
- **Cancel no longer leaves the file behind.** Cancelling removed the queue
  row's work but not its part file, so a cancelled 50 GB upload left a
  full-size placeholder on the seedbox. It is sparse on disk, but seedbox
  quotas are usually billed on apparent size, so you were paying for a
  transfer you cancelled. Cancel now discards the placeholder, the lane plan
  and the byte count. Pause is unchanged and still keeps everything for
  resume.
- **Clear done no longer strands a cancelled transfer's data.** It deleted
  the row, which was the only record that the part file existed. It now
  sweeps those files first, the same way Clear failed does.
- **The same file queued twice no longer runs twice.** Two rows aimed at one
  destination wrote into the same part file at the same time. For downloads
  that wasted the transfer; for uploads, where the two sources can differ, it
  could splice two files together. Only one row per destination runs at a
  time now, and re-queuing something already waiting returns the row you
  already have instead of adding a second. Two *different* files aimed at one
  destination are still accepted — that is a conflict, and warpseed will not
  pretend it queued something it did not.
- **Cancel is now always honoured.** A transfer cancelled in the moment
  before the queue picked it up could be started anyway, because the queue
  worked from a list read a moment earlier and never rechecked. It now claims
  a transfer only while that transfer is still waiting, so a cancel can never
  be overtaken.
- **Closing warpseed no longer spends a transfer's retries.** A transfer
  interrupted on its second attempt came back with one retry left and the old
  error still attached, so it gave up at the first hiccup of the new run.
  Quitting is not a transfer failure: recovery now restores a clean slate and
  keeps the byte progress, so it resumes rather than restarts.

## Notes

- Nothing here changes your settings, your queue or your saved sites.
- The download resume check costs two 256 KiB reads per lane before a resumed
  transfer starts — a fraction of a second against a transfer measured in
  gigabytes, and the only way to know the bytes on disk are the bytes you
  asked for.
