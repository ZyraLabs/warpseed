warpseed 1.1.6 — it asks before it replaces a file

**Downloading a file you already had used to replace it silently — and only
after transferring the whole thing again.** A 50 GB file cost 50 GB to
discover it was unwanted, and the copy you had was gone. That is fixed, in
both directions.

## Fixed

- **The destination is checked before the transfer starts**, not at the
  moment it finishes. Nothing is re-transferred to find out it was not
  needed, and nothing is replaced without a decision having been made.

## New

- **Overwrite rules, in Settings → When the file already exists.** Each kind
  of clash gets its own answer:

  | | Default |
  |---|---|
  | Incoming is newer and larger | Overwrite |
  | Incoming is smaller | Ask |
  | Incoming is older | Ask |
  | Identical (same size and time) | Skip |
  | Anything else | Ask |

  Every one can be set to Overwrite, Skip, Keep both, or Ask. "Newer and
  larger" is the only case that replaces a file on its own — a bigger file
  with a later date is a better copy of the same thing far more often than it
  is a mistake. Anything that could be a downgrade asks.

- **"Ask" holds the file in the queue** instead of interrupting you. The dock
  shows *"3 files already exist at the destination. Nothing is transferred
  until you decide."* Each row explains itself — *"Incoming is smaller than
  the copy you have — 1.8 GB incoming vs 2.4 GB already there"* — with Skip,
  Keep both and Overwrite per row, or the same three for the whole set. A
  folder full of clashes is one decision, not one dialog per file.

- **Keep both** transfers to a free name beside the existing file:
  `ep01.mkv` becomes `ep01 (1).mkv`.

- Uploads get all of this too. Uploading over a file already on the seedbox
  was just as silent, and harder to notice.

## Notes

- Held transfers are queued but not running: they consume no connection and
  hold nothing up. The rest of the queue carries on around them.
- A server that reports no timestamp still gets a size comparison; warpseed
  will not call two files identical on size alone.
- Existing settings and queues are untouched. The rules above are the
  defaults for everyone, including existing installs.
