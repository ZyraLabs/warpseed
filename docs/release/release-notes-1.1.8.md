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

- **The folder tree was useless on most seedboxes.** It always started at
  `/`, which a seedbox account usually cannot list, so the sidebar opened onto
  a folder that would never expand. It now starts where the pane starts — the
  folder configured for the site, then the account's home.

- **The Name column can be resized.** There was no handle on the divider
  between Name and Size, so the only way to widen it was to shrink the other
  two columns. Drag the divider and a long filename opens out; the row scrolls
  if you drag it past the pane.

- **The folder tree sidebar can be resized**, and remembers its width. It was
  fixed at 190px, which a deep remote path does not fit.

- **Column grips no longer feel dead on the first drag.** They measured from
  the stored width rather than the rendered one, so a column showing wider than
  its stored value ignored the beginning of a drag.

## New

- **warpseed tells you when there is a new version.** A calm strip across the
  top: what is available, what you have, a button to the release page, and a
  Dismiss that stays dismissed until the release after it. It checks GitHub
  once per run and never at a moment that would slow the app down.

  It sends no identifiers and no usage data — it asks a public page what the
  latest version is, and nothing about you goes with the question. There is
  still no telemetry. Turn it off in **Settings → About**, where there is also
  a **Check now** button.

  **warpseed never downloads or replaces itself.** It is a portable exe with no
  installer; an app that rewrites the file it is running from is one antivirus
  away from leaving you with neither. The button opens the release page and you
  choose.

- **The version shown in Settings, and in the bug-report email, was wrong.** It
  said 1.1.1 on every build since — the same drift fixed in the log, still live
  in the interface. Every bug report emailed since 1.1.2 arrived labelled with
  the wrong version.

- **Closing warpseed while transfers are running now asks first.** It used to
  be a hard kill with no warning. Nothing was ever actually lost — every
  connection checkpoints, and unfinished transfers restart on the next launch —
  but you were never told that, and a 50 GB overnight run simply vanishing is
  not something to find out by guessing.

  The confirmation says how many transfers are running, how much would be
  re-sent (about 8 MB per connection), what the leftover `.wspart` and
  `.wschunk` files are for, and how many queued transfers are untouched. Three
  answers: keep warpseed open, close and resume later, or minimize to the pill
  and leave everything running.

  **An idle warpseed still closes instantly**, exactly as before — no dialog, no
  delay. Settings → Closing chooses what the X button does while transfers are
  running, and the confirmation itself offers "don't ask again".

## Notes

- The tree updates itself when files land. Completing a transfer, renaming,
  deleting or moving refreshes the affected part of the tree, and connecting or
  disconnecting a site clears that site's tree entirely — a reconnect can be to
  a server that changed while you were away.
- Nothing about the tree is saved between runs. It is a picture of something
  that changes underneath you, and a stale one restored from disk would be
  worse than starting fresh.
