# warpseed 1.0 — release notes

Free forever, from Zyra Labs. Support development: buymeacoffee.com/zyralabs

## What warpseed is
A fast transfer client for Windows built for seedbox workloads: parallel
connections, byte-level resume that survives errors and restarts, and a
queue you can trust with an overnight 50 GB run.

## Highlights
- **Hyperlane** — one large file split across up to 16 connections at
  once, so a per-connection speed cap no longer caps the file.
- **Byte-level resume** — errored or paused transfers pick up at the
  exact byte, verified against per-chunk checkpoints.
- **Four views** — Deck (the night's work at a glance), Browse (dual-pane
  commander, WinSCP muscle memory intact), Activity (the session as a
  story), and Flight (a live picture of the pipeline while it runs).
- **Mini mode** — collapse warpseed to a tiny always-on-top pill and keep
  an eye on the run while you work; Escape brings it back.
- **Three themes** — Clay (default), Cobalt, and Iris (dark).
- **Honest failure handling** — plain-language errors with one-click
  retry; the app reconnects cleanly after idle timeouts.
- **Safety** — host-key pinning on first use, credentials in Windows
  Credential Manager, downloads confined to their destination folder.

## Bug reports
Email bugreports@zyralabs.tech — handled on an urgency basis.

## Requirements
Windows 10/11 x64 · WebView2 (preinstalled on Windows 11)

## Known limitations
- SFTP password auth only (SSH keys/agent planned)
- FTP/S3/WebDAV planned via a future engine adapter
- Unsigned binary for now — SmartScreen may prompt on first run
