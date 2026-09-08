warpseed 1.1.8 — the folder tree remembers

## Fixed

- **The folder tree stopped re-listing folders you had already opened.** Every
  collapse threw the folder's contents away, so re-opening it went back to the
  server. Worse, collapsing a folder discarded everything nested inside it, so
  a branch you had opened four levels deep had to be fetched again one folder
  at a time — and closing the sidebar discarded the whole tree. On a seedbox
  each of those is a round trip on the one browse connection. Re-opening a
  folder now costs nothing.

- **Switching a pane between two sites showed the previous server's folders.**
  Every remote site's tree starts at `/`, and the tree identified a folder by
  its path alone — so the second site inherited the first one's listing until
  something forced a refresh. Found while fixing the caching above.

- **A folder that failed to list no longer looks empty.** A momentary error
  was remembered as "this folder has no subfolders" for as long as the sidebar
  stayed open. Failures are now recorded as failures, and re-opening the folder
  tries again.

- **The version in the log was wrong.** Every build from 1.1.1 to 1.1.7 logged
  `warpseed 1.1.1 starting`, because the version was typed in twice and only one
  copy was ever updated. A log is the main evidence for which build a problem
  came from, so this quietly pointed every bug report at the wrong release. It
  now comes from the same place the release build reads it.

## Notes

- The tree updates itself when files land. Completing a transfer, renaming,
  deleting or moving refreshes the affected part of the tree, and connecting or
  disconnecting a site clears that site's tree entirely — a reconnect can be to
  a server that changed while you were away.
- Nothing about the tree is saved between runs. It is a picture of something
  that changes underneath you, and a stale one restored from disk would be
  worse than starting fresh.
