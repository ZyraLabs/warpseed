warpseed 1.1.1 — Hyperlane downloads no longer stall the disk

**If Hyperlane downloads sat at one connection's speed, cancel took ages to
let go, or warpseed lingered in Task Manager doing disk work after you closed
it, this is the fix.** Thank you to the tester who reported all three in one
email and pointed at the same file in WinSCP for comparison.

## Fixed

- **Large downloads no longer zero-fill the destination.** Hyperlane reserves
  the full file size up front so each lane can write at its own offset. On
  Windows that reservation was not free: the first write from each lane made
  NTFS write zeros over everything before it, inside a call that nothing could
  interrupt. On a 50 GB file with four lanes that was roughly 37 GB of zeros
  before lane four's first byte landed. Three of four lanes sat idle for
  minutes, cancel could not take effect until the zeros finished, and the
  process outlived the window — End Task included — until the disk was done.
  The part file is now flagged sparse before it is sized, so a write at any
  offset costs only that write, and the flag is cleared again once every
  byte is in place so the finished file is an ordinary one. Volumes that
  refuse the flag (FAT32, some network shares) still work and a line in the
  log says so.

## Added

- **Verbose log.** Settings → About → *Verbose log*. Adds per-lane byte
  ranges, how long each lane's first write took, and when a cancel was
  requested versus when the lanes actually let go. Off by default so the log
  stays a readable timeline. Turn it on when reporting a stall, reproduce it,
  then send `warpseed.log` from *Open log folder*.

## Notes

- This is about downloads to a local NTFS volume. The single-connection
  path was never affected; only files above the Hyperlane threshold
  (256 MB by default) hit this.
- Known gap: a Hyperlane *upload* to a Windows-hosted SFTP server can hit
  the same zero-fill on the server's side, where warpseed cannot set the
  flag. Seedboxes run Linux, so this is unlikely to affect anyone; if it
  does, set upload lanes to 1 for that site.
- A `.wschunk` left by 1.1.0 is flagged sparse on its next attempt and
  resumes normally.
