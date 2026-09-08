warpseed 1.1.7 — the Progress heading sorts

## Fixed

- **Clicking "Progress" in the queue did nothing.** Every other heading in
  the dock sorts, so one that looked identical but ignored clicks read as
  sorting being broken rather than as that column not being a control. It
  now sorts by how far along each transfer is, click again to reverse, and
  once more to go back to queue order.

  The **%** column beside it was always sortable and shows the same number,
  so the two light up together — clicking either orders the rows exactly as
  the bars look.
