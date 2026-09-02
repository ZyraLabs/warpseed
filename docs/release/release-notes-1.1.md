warpseed 1.1.0 — sortable columns, faster uploads, and a log file

**If you are on 1.0.0, this is the update to take.** The column headers in
1.0.0 were not clickable at all — a CSS rule further down the same file put
the header strip out of flow, underneath the first row of files. Sorting by
size or date was unreachable, and the status bar was quietly eating most of
the pane. Both are fixed.

## Fixed

- **Column headers work.** Click Name, Size or Modified to sort; click again
  to reverse. Both panes now share one sort — previously each pane kept its
  own and they only agreed again after a restart. Sorting by size in a
  folder-only listing reverses properly instead of appearing to do nothing.
- **The file list gets the space.** The pane used a four-row grid for a
  layout whose filter strip is optional, so with the filter closed the free
  space went to the status bar instead of the listing.
- **Typing jumps instead of hiding.** Pressing a letter now moves the cursor
  to the first matching file, the way File Explorer does, and repeats step
  through matches. Previously it opened a filter and made rows disappear as
  you typed, with no way back from the keyboard. `Ctrl+F` still opens the
  filter deliberately, and `Esc` leaves it.
- **The address bar stops stealing focus.** After one `Ctrl+L`, every folder
  you opened re-entered the path editor, leaving the arrow keys dead until
  you clicked back into the list.
- **Times are local.** Timestamps were shown in UTC, so they disagreed with
  Explorer and made a correct date sort look wrong.
- **The folder tree opens collapsed** instead of listing every drive up front.
- **Queued transfers no longer wait behind a multi-lane one.** A transfer now
  starts on whatever connections are free rather than holding out for its
  full request, which could leave downloads pending indefinitely behind an
  upload.
- **Closing no longer risks re-sending a finished file.** Quitting in the
  moment a transfer completed could lose its "completed" record, and since a
  finished transfer has already published its file, the retry on next launch
  started again from zero. Shutdown now stops transfers and lets their state
  land first.

## New

- **Hyperlane uploads.** Large uploads now split across several connections,
  the way downloads have since 1.0. Off below 128 MB, three lanes by default,
  both adjustable in Settings → Hyperlane · Uploads. Interrupted uploads
  resume per chunk, and every resumed range is verified against the source
  before it is trusted.
- **Activity reports real speeds.** A summary band shows current rate, data
  moved today, average and best, and every finished transfer carries its
  duration and rate. Averages are over time spent transferring, not
  wall-clock, so idle gaps do not drag them down. Transfers from before this
  release have no timing recorded and show no speed.
- **A log file.** `warpseed.log`, next to the database, with **Settings →
  About → Open log folder** to find it. Attach it to a bug report; it makes
  a slow or failed transfer diagnosable instead of guesswork.
- **Sort shortcuts and column affordances.** `Ctrl+F3` / `Ctrl+F5` / `Ctrl+F6`
  sort by name, date and size, and the headers show a sort direction.

## Notes

Hyperlane uploads are new in this release and have been tested against one
server. If an upload behaves oddly, set **Lanes per file** to 1 in Settings
to fall back to the single-connection path, and send the log.

Upload throughput depends heavily on your connection: where a single stream
is held back by packet loss, more lanes can help far more than expected, so
it is worth trying higher lane counts and watching the Activity average.

Windows 10/11 x64. Portable, no installer. The binary is unsigned, so
SmartScreen may prompt on first run — "More info" then "Run anyway", and
verify the published SHA-256 if you would rather be certain.
