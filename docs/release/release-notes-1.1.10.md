warpseed 1.1.10 — stop the queue, cancel a whole folder

**If you have ever queued a folder by mistake and had to cancel it one row
at a time, or wished warpseed would not start last night's queue the moment
it opened, this is the release.** Both came from the same tester, and both
were fair: a queue you can trust with an overnight run has to be a queue you
can also stop.

## New

- **Pause queue.** One button at the left of the queue toolbar (or "Queue:
  pause" in the command palette) stops everything: nothing new starts, and
  running transfers stop and go back to the queue with their progress kept,
  so **Resume queue** picks them up from the same byte. The pause is
  remembered — close warpseed while it is paused and it opens paused.

- **Cancel all queued.** Cancels everything waiting in the queue — queued
  rows, paused rows, rows a queue pause put back, and ones waiting for a
  decision about an existing file. Running transfers keep going; pause the
  queue first if you want those stopped too. It acts on the whole queue, not
  just the rows on screen, so a folder of five thousand files is one click
  and one confirmation, and the confirmation says how many of those rows
  hold part-transferred data before it is deleted.

- **Select rows and cancel them.** Click a row to select it, Ctrl+click to
  add or remove one, Shift+click for a range; **Cancel selected** appears in
  the toolbar and **Delete** does the same. Escape clears the selection.
  As with a single cancel, nothing is asked when nothing has transferred
  yet; when part-transferred data is at stake the dialog says so, and the
  data is deleted with the row.

- **Start paused** (Settings → Queue on launch). Every launch begins with
  the queue stopped, so you decide when it resumes rather than the app
  deciding for you. Pressing Resume queue starts that session; the next
  launch begins paused again until you turn the setting off.

## Fixed

- **Closing warpseed mid-transfer no longer counts against that transfer.**
  A transfer cut off by shutdown was classified like any other broken
  connection and could come back on the retry ladder with an attempt spent.
  It now returns to the queue clean, the same as one interrupted by the new
  pause.

- **A cancel can no longer be undone by the transfer it interrupted.** The
  engine's "failed" and "retry" writes now apply only while the row is still
  running, so a row you cancel while it is unwinding stays cancelled instead
  of reappearing as queued. The one exception is deliberate: a cancel that
  lands after the finished file has already been moved into place is too
  late, and the row says "completed" because that is what happened.

## Notes

- Cancelled rows stay in the list until you **Clear done**, so an
  accidental "cancel everything" is visible, not silent. Their
  part-transferred data is deleted at cancel time, as it always has been.
- A row you paused yourself stays paused when the queue is paused and
  resumed; the queue-wide pause only moves rows it stopped.
