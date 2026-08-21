<p align="center">
  <img src="docs/design/brand/zyra-avatar-1024.png" width="80" alt="Zyra Labs">
</p>

# warpseed

**A fast, free transfer client for Windows, built for seedbox workloads.**
Parallel connections, byte-level resume that survives errors and restarts,
and a queue you can trust with an overnight 50 GB run.

Made by [Zyra Labs](https://zyralabs.tech). Free forever — if it saves you
time, [a coffee keeps the updates coming](https://buymeacoffee.com/zyralabs).

## Download

Grab the latest `warpseed.exe` from the
[Releases page](../../releases/latest). Portable — no installer, no accounts,
no telemetry. Requires Windows 10/11 x64 with WebView2 (preinstalled on
Windows 11).

> The binary is currently unsigned, so SmartScreen may prompt on first run
> ("More info" → "Run anyway"). Verify the SHA-256 checksum published with
> each release. Or build it yourself — see below.

## Highlights

- **Hyperlane** — one large file split across up to 16 connections at once,
  so a per-connection speed cap no longer caps the file
- **Byte-level resume** — errored or paused transfers pick up at the exact
  byte, verified against per-chunk checkpoints
- **Four views** — Deck (the night's work at a glance), Browse (dual-pane
  commander with WinSCP muscle memory), Activity (the session as a story),
  Flight (a live picture of the pipeline)
- **Mini mode** — collapse to a tiny always-on-top pill while the run
  continues; Escape brings the window back
- **Three themes** — Clay (default), Cobalt, and Iris (dark)
- **Safety** — SFTP host-key pinning on first use, credentials stored in
  Windows Credential Manager, downloads confined to their destination

## Bug reports

Email **bugreports@zyralabs.tech** with what happened and what you expected.
Reports are handled on an urgency basis. This repository does not take
issues or pull requests — warpseed is released and supported by Zyra Labs
directly, and the source is published so you can read exactly what runs on
your machine.

## Building from source

Cross-compilable, CGO-free Go + a Vite frontend:

```
# requires Go 1.25+, Node 20+, and the Wails v2 CLI
wails build
# → build/bin/warpseed.exe
```

## License

MIT © 2026 Zyra Labs — see [LICENSE](LICENSE).
