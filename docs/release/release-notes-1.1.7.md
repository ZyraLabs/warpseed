warpseed 1.1.7 — the queue reads right

## Fixed

- **The queue said uploads came FROM the server.** Every row showed
  "site → destination", which is the truth for a download and backwards for
  an upload. Uploads now read "This PC → site:/path". The tooltip was
  already right; the column was not.

- **Widening a column pushed the rest of the queue off the screen.** The
  window sized itself to the queue's widest column instead of the other way
  round, so anything past the window edge was cut off with no scrollbar — at
  the 900px minimum window the right-hand columns could not be reached at
  all, and dragging the File column wider to read a long filename only made
  it worse. The queue now scrolls sideways within the window, so a column
  can be dragged as wide as you like and everything stays reachable.

- **Clicking "Progress" in the queue did nothing.** Every other heading in
  the dock sorts, so one that looked identical but ignored clicks read as
  sorting being broken rather than as that column not being a control. It
  now sorts by how far along each transfer is, click again to reverse, and
  once more to go back to queue order.

  The **%** column beside it was always sortable and shows the same number,
  so the two light up together — clicking either orders the rows exactly as
  the bars look.

## Changed

- **Uploads are drawn in their own colour** — in the queue, on the Flight
  screen, in Deck, Activity and the mini pill — so a mixed queue says which
  way the bytes are going at a glance. Each theme picks a colour clear of
  its own accent. A paused, failed or finished upload still shows its state
  colour: direction only tints a transfer that is actually moving.
