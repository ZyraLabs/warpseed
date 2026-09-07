warpseed 1.1.5 — nothing is deleted without asking

1.1.4 made cancel throw away a transfer's part-downloaded data, which is what
stops a cancelled 50 GB upload sitting on your seedbox quota. It did not add a
warning, so a single click could discard hours of transfer with no way back.
That was wrong, and this fixes it.

## Fixed

- **Cancel now asks first**, and says exactly what it is about to delete —
  "the 12.4 GB already transferred" — and that Pause would keep it. The
  **Skip** buttons in Deck and Timeline go through the same warning: they
  cancel a failed transfer, and "skip" reads far too much like "leave it for
  later".
- **Clear done asks** when the list contains cancelled transfers, whose
  part-downloaded data goes with the row. It says plainly that completed
  transfers are only being removed from a list and the files you downloaded
  are untouched.
- **Deleting a saved site asks.** The X on a site in Quick Connect removed it,
  its saved password, its bookmarks and its pinned host key, on one click and
  with no confirmation at all. That has been true since 1.0.

## Changed

- Every destructive confirmation now offers **"Don't ask again until warpseed
  restarts"**. Silencing one kind of warning silences only that kind — turning
  off the file-delete prompt does not turn off the cancel prompt — and a
  relaunch always asks again.
- A transfer that has moved no bytes cancels immediately, with no dialog.
  There is nothing to lose, and a warning you always dismiss is a warning you
  stop reading.

## Notes

- If you are on 1.1.3 or earlier, cancel did not delete anything and there was
  nothing to warn about. Coming from 1.1.4, this is the guard rail that should
  have shipped with it.
