/* Demo dataset for the browser-only mock backend (docs screenshots).
   Field names mirror the Go JSON tags in internal/queue, internal/localfs
   and app.go — those are authoritative; a mismatch renders blank UI. */
import type { Bookmark, FsEntry, FsRoot, Site, Transfer } from "../ipc";

const GiB = 1024 ** 3;
const MiB = 1024 ** 2;
const KiB = 1024;

const NOW = Date.now();
const iso = (minsAgo: number) => new Date(NOW - minsAgo * 60_000).toISOString();

export const SITES: Site[] = [
  {
    id: 1,
    name: "hyperion",
    protocol: "sftp",
    host: "hyperion.example.com",
    port: 22,
    username: "seedling",
    credRef: "keyring:site-1",
    optionsJson: "{}",
    remotePath: "/home/seedling/downloads",
    maxTransfers: 8,
    createdAt: iso(60 * 24 * 40),
    updatedAt: iso(12),
  },
  {
    id: 2,
    name: "nebula",
    protocol: "sftp",
    host: "nebula.example.net",
    port: 22,
    username: "seedling",
    credRef: "keyring:site-2",
    optionsJson: "{}",
    remotePath: "/home/seedling/files",
    maxTransfers: 6,
    createdAt: iso(60 * 24 * 21),
    updatedAt: iso(60 * 24 * 3),
  },
  {
    id: 3,
    name: "lab-nas",
    protocol: "sftp",
    host: "10.0.4.20",
    port: 2222,
    username: "nas",
    credRef: "keyring:site-3",
    optionsJson: "{}",
    remotePath: "/volume1/media",
    maxTransfers: 4,
    createdAt: iso(60 * 24 * 90),
    updatedAt: iso(60 * 24 * 9),
  },
];

/** Site connected at boot (the browse pane can attach to it immediately). */
export const CONNECTED_SITE_ID = 1;

const dir = (name: string, minsAgo: number): FsEntry => ({
  name,
  isDir: true,
  size: -1,
  modTime: iso(minsAgo),
  mode: "drwxr-xr-x",
});
const file = (name: string, size: number, minsAgo: number, mode = "-rw-r--r--"): FsEntry => ({
  name,
  isDir: false,
  size: Math.round(size),
  modTime: iso(minsAgo),
  mode,
});

const REMOTE_DOWNLOADS: FsEntry[] = [
  dir("Andor.S02.2160p.DSNP.WEB-DL.DDP5.1.DV.HDR.H.265-FLUX", 60 * 30),
  dir("Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.Atmos.H.264-FLUX", 60 * 55),
  dir("The.Bear.S03.1080p.WEB-DL.DDP5.1.H.264-NTb", 60 * 24 * 2),
  dir("linux-isos", 60 * 24 * 6),
  dir("music", 60 * 24 * 14),
  file("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.DV.TrueHD.7.1-FraMeSToR.mkv", 24.6 * GiB, 60 * 3),
  file("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.DV.TrueHD.7.1-FraMeSToR.nfo", 6.2 * KiB, 60 * 3),
  file("Oppenheimer.2023.1080p.BluRay.x264-SPARKS.mkv", 14.1 * GiB, 60 * 26),
  file("Interstellar.2014.IMAX.2160p.UHD.BluRay.x265-TERMiNAL.mkv", 31.4 * GiB, 60 * 24 * 3),
  file("Past.Lives.2023.1080p.WEB.H264-CUPCAKES.mkv", 5.9 * GiB, 60 * 24 * 4),
  file("ubuntu-24.04.3-live-server-amd64.iso", 3.1 * GiB, 60 * 7),
  file("Fedora-Workstation-Live-x86_64-42-1.1.iso", 2.3 * GiB, 60 * 11),
  file("debian-13.0.0-amd64-DVD-1.iso", 3.9 * GiB, 60 * 24),
  file("archlinux-2026.08.01-x86_64.iso", 1.2 * GiB, 60 * 24 * 5),
  file("Big.Buck.Bunny.2008.1080p.BluRay.x264-SAMPLE.mkv", 691 * MiB, 60 * 24 * 12),
  file("SHA256SUMS", 1.1 * KiB, 60 * 24),
  file("SHA256SUMS.gpg", 833, 60 * 24),
  file(".rtorrent.session.lock", 0, 2, "-rw-------"),
];

const REMOTE_LINUX: FsEntry[] = [
  file("ubuntu-24.04.3-live-server-amd64.iso", 3.1 * GiB, 60 * 7),
  file("Fedora-Workstation-Live-x86_64-42-1.1.iso", 2.3 * GiB, 60 * 11),
  file("debian-13.0.0-amd64-DVD-1.iso", 3.9 * GiB, 60 * 24),
  file("archlinux-2026.08.01-x86_64.iso", 1.2 * GiB, 60 * 24 * 5),
  file("nixos-minimal-25.05-x86_64-linux.iso", 1.1 * GiB, 60 * 24 * 8),
];

const REMOTE_HOME: FsEntry[] = [
  dir("downloads", 60 * 3),
  dir("files", 60 * 24),
  dir("watch", 60 * 24 * 3),
  dir(".config", 60 * 24 * 40),
  file(".bashrc", 3.7 * KiB, 60 * 24 * 40),
  file(".profile", 807, 60 * 24 * 40),
];

const REMOTE_SEASON = (n: number, minsAgo: number): FsEntry[] =>
  Array.from({ length: n }, (_, i) =>
    file(`Episode.S0${1 + Math.floor(i / 12)}E${String((i % 12) + 1).padStart(2, "0")}.mkv`, (2.4 + (i % 3) * 0.35) * GiB, minsAgo + i * 30),
  );

/** Remote tree by site id then POSIX path. */
export const REMOTE: Record<number, Record<string, FsEntry[]>> = {
  1: {
    "/": [dir("home", 60 * 24 * 90), dir("etc", 60 * 24 * 90), dir("var", 60 * 24 * 90), dir("tmp", 5)],
    "/home": [dir("seedling", 60 * 3)],
    "/home/seedling": REMOTE_HOME,
    "/home/seedling/downloads": REMOTE_DOWNLOADS,
    "/home/seedling/downloads/linux-isos": REMOTE_LINUX,
    "/home/seedling/downloads/Andor.S02.2160p.DSNP.WEB-DL.DDP5.1.DV.HDR.H.265-FLUX": [
      ...Array.from({ length: 12 }, (_, i) =>
        file(`Andor.S02E${String(i + 1).padStart(2, "0")}.2160p.DSNP.WEB-DL.DDP5.1.DV.HDR.H.265-FLUX.mkv`, (4.1 + (i % 4) * 0.3) * GiB, 60 * 30 + i * 10),
      ),
      file("Andor.S02.nfo", 4.4 * KiB, 60 * 30),
    ],
    "/home/seedling/downloads/Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.Atmos.H.264-FLUX": REMOTE_SEASON(10, 60 * 55),
    "/home/seedling/downloads/The.Bear.S03.1080p.WEB-DL.DDP5.1.H.264-NTb": REMOTE_SEASON(10, 60 * 24 * 2),
    "/home/seedling/downloads/music": [
      dir("Khruangbin - A LA SALA (2024) [FLAC]", 60 * 24 * 14),
      dir("Boards of Canada - Music Has the Right to Children (1998) [FLAC]", 60 * 24 * 20),
    ],
    "/home/seedling/files": [dir("backups", 60 * 24)],
    "/home/seedling/watch": [],
  },
  2: {
    "/": [dir("home", 60 * 24 * 90)],
    "/home": [dir("seedling", 60 * 24)],
    "/home/seedling": [dir("files", 60 * 24), dir("watch", 60 * 24 * 3)],
    "/home/seedling/files": [
      dir("completed", 60 * 24),
      file("openSUSE-Leap-16.0-DVD-x86_64.iso", 4.4 * GiB, 60 * 24),
      file("Rocky-10.0-x86_64-dvd.iso", 9.2 * GiB, 60 * 24 * 2),
    ],
    "/home/seedling/files/completed": [],
    "/home/seedling/watch": [],
  },
  3: {
    "/": [dir("volume1", 60 * 24 * 90)],
    "/volume1": [dir("media", 60 * 24 * 9)],
    "/volume1/media": [dir("Movies", 60 * 24 * 9), dir("TV", 60 * 24 * 9)],
    "/volume1/media/Movies": [],
    "/volume1/media/TV": [],
  },
};

export const LOCAL_HOME = "D:\\Media\\Incoming";

/** Local tree by Windows path (backslash form, no trailing slash except roots). */
export const LOCAL: Record<string, FsEntry[]> = {
  "C:\\": [dir("Program Files", 60 * 24 * 60), dir("Users", 60 * 24 * 60), dir("Windows", 60 * 24 * 60)],
  "C:\\Users": [dir("seedling", 60 * 24 * 60), dir("Public", 60 * 24 * 60)],
  "C:\\Users\\seedling": [dir("Desktop", 60 * 24), dir("Documents", 60 * 24 * 3), dir("Downloads", 60 * 2)],
  "C:\\Users\\seedling\\Downloads": [file("warpseed-setup-0.9.0.exe", 24.7 * MiB, 60 * 2)],
  "D:\\": [dir("Media", 60 * 5), dir("Backups", 60 * 24 * 7), dir("Games", 60 * 24 * 30)],
  "D:\\Media": [dir("Incoming", 60 * 5), dir("Movies", 60 * 24 * 2), dir("TV", 60 * 24 * 2), dir("Music", 60 * 24 * 14)],
  "D:\\Media\\Incoming": [
    dir("Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.Atmos.H.264-FLUX", 60 * 40),
    dir("linux-isos", 60 * 24),
    file("Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.DV.TrueHD.7.1-FraMeSToR.mkv.part", 14.3 * GiB, 0),
    file("debian-13.0.0-amd64-DVD-1.iso", 3.9 * GiB, 38),
    file("Past.Lives.2023.1080p.WEB.H264-CUPCAKES.mkv", 5.9 * GiB, 60 * 24 * 3),
    file("Big.Buck.Bunny.2008.1080p.BluRay.x264-SAMPLE.mkv", 691 * MiB, 60 * 24 * 11),
    file("SHA256SUMS", 1.1 * KiB, 38),
  ],
  "D:\\Media\\Incoming\\linux-isos": [
    file("archlinux-2026.08.01-x86_64.iso", 1.2 * GiB, 60 * 24 * 4),
    file("nixos-minimal-25.05-x86_64-linux.iso", 1.1 * GiB, 60 * 24 * 8),
  ],
  "D:\\Media\\Incoming\\Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.Atmos.H.264-FLUX": REMOTE_SEASON(10, 60 * 40),
  "D:\\Media\\Movies": [dir("Oppenheimer (2023)", 60 * 24 * 2), dir("Past Lives (2023)", 60 * 24 * 3)],
  "D:\\Media\\TV": [dir("Andor", 60 * 24 * 2), dir("Severance", 60 * 24 * 2)],
  "D:\\Media\\Music": [],
  "D:\\Backups": [file("warpseed-2026-08-17.db", 2.1 * MiB, 60 * 24 * 7)],
  "D:\\Games": [],
};

export const LOCAL_ROOTS: FsRoot[] = [
  { path: "C:\\", label: "Windows (C:)" },
  { path: "D:\\", label: "Media (D:)" },
];

export const BOOKMARKS: Bookmark[] = [
  { id: 1, siteId: 0, path: "D:\\Media\\Incoming", label: "Incoming", createdAt: iso(60 * 24 * 30) },
  { id: 2, siteId: 0, path: "D:\\Media\\Movies", label: "Movies", createdAt: iso(60 * 24 * 30) },
  { id: 3, siteId: 1, path: "/home/seedling/downloads", label: "downloads", createdAt: iso(60 * 24 * 20) },
  { id: 4, siteId: 1, path: "/home/seedling/downloads/linux-isos", label: "linux-isos", createdAt: iso(60 * 24 * 6) },
  { id: 5, siteId: 2, path: "/home/seedling/files", label: "files", createdAt: iso(60 * 24 * 10) },
];

export const DISK = { free: Math.round(812 * GiB), total: Math.round(3.64 * 1024 * GiB) };

export const DATA_INFO = {
  path: "C:\\Users\\seedling\\AppData\\Roaming\\warpseed\\warpseed.db",
  folder: "C:\\Users\\seedling\\AppData\\Roaming\\warpseed",
  backups: [
    "C:\\Users\\seedling\\AppData\\Roaming\\warpseed\\backups\\warpseed-2026-08-17T21-04-11.db",
    "C:\\Users\\seedling\\AppData\\Roaming\\warpseed\\backups\\warpseed-2026-08-03T09-12-40.db",
  ],
};

export const SETTINGS: Record<string, string> = {
  "ui.theme": "clay",
  "ui.local_default": LOCAL_HOME,
  "ui.donate_nudged": "1",
  "transfers.global_max": "8",
  "transfers.site_max": "8",
  "transfers.chunk_streams": "8",
  "transfers.chunk_min_mb": "64",
  "bw.mode": "off",
  "bw.observed_max": String(Math.round(43.8 * MiB)),
  "bw.percent": "80",
};

/** Per-lane speeds (bytes/s) so the aggregate lands near 40 MiB/s. */
export interface Sim {
  lanes: number;
  laneRate: number; // bytes/s per lane
}

export const TRANSFERS: Transfer[] = [
  {
    id: 101,
    siteId: 1,
    engine: "sftpfast",
    direction: "download",
    src: "/home/seedling/downloads/Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.DV.TrueHD.7.1-FraMeSToR.mkv",
    dst: "D:\\Media\\Incoming\\Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.DV.TrueHD.7.1-FraMeSToR.mkv",
    size: Math.round(24.6 * GiB),
    state: "active",
    priority: 0,
    bytesDone: Math.round(14.3 * GiB),
    attempt: 1,
    nextRetryAt: null,
    error: null,
    createdAt: iso(9),
    updatedAt: iso(0),
  },
  {
    id: 102,
    siteId: 1,
    engine: "sftpfast",
    direction: "download",
    src: "/home/seedling/downloads/ubuntu-24.04.3-live-server-amd64.iso",
    dst: "D:\\Media\\Incoming\\linux-isos\\ubuntu-24.04.3-live-server-amd64.iso",
    size: Math.round(3.1 * GiB),
    state: "active",
    priority: 0,
    bytesDone: Math.round(0.68 * GiB),
    attempt: 1,
    nextRetryAt: null,
    error: null,
    createdAt: iso(4),
    updatedAt: iso(0),
  },
  {
    id: 103,
    siteId: 1,
    engine: "sftpfast",
    direction: "download",
    src: "/home/seedling/downloads/The.Bear.S03.1080p.WEB-DL.DDP5.1.H.264-NTb/Episode.S01E04.mkv",
    dst: "D:\\Media\\Incoming\\The.Bear.S03.1080p.WEB-DL.DDP5.1.H.264-NTb\\Episode.S01E04.mkv",
    size: Math.round(2.9 * GiB),
    state: "paused",
    priority: 0,
    bytesDone: Math.round(1.19 * GiB),
    attempt: 1,
    nextRetryAt: null,
    error: null,
    createdAt: iso(22),
    updatedAt: iso(6),
  },
  {
    id: 104,
    siteId: 1,
    engine: "sftpfast",
    direction: "download",
    src: "/home/seedling/downloads/Fedora-Workstation-Live-x86_64-42-1.1.iso",
    dst: "D:\\Media\\Incoming\\linux-isos\\Fedora-Workstation-Live-x86_64-42-1.1.iso",
    size: Math.round(2.3 * GiB),
    state: "pending",
    priority: 0,
    bytesDone: 0,
    attempt: 0,
    nextRetryAt: null,
    error: null,
    createdAt: iso(3),
    updatedAt: iso(3),
  },
  {
    id: 105,
    siteId: 2,
    engine: "sftpfast",
    direction: "upload",
    src: "D:\\Backups\\warpseed-2026-08-17.db",
    dst: "/home/seedling/files/backups/warpseed-2026-08-17.db",
    size: Math.round(2.1 * MiB),
    state: "pending",
    priority: 0,
    bytesDone: 0,
    attempt: 0,
    nextRetryAt: null,
    error: null,
    createdAt: iso(2),
    updatedAt: iso(2),
  },
  {
    id: 100,
    siteId: 1,
    engine: "sftpfast",
    direction: "download",
    src: "/home/seedling/downloads/debian-13.0.0-amd64-DVD-1.iso",
    dst: "D:\\Media\\Incoming\\debian-13.0.0-amd64-DVD-1.iso",
    size: Math.round(3.9 * GiB),
    state: "completed",
    priority: 0,
    bytesDone: Math.round(3.9 * GiB),
    attempt: 1,
    nextRetryAt: null,
    error: null,
    createdAt: iso(41),
    updatedAt: iso(38),
  },
  {
    id: 99,
    siteId: 1,
    engine: "sftpfast",
    direction: "download",
    src: "/home/seedling/downloads/Interstellar.2014.IMAX.2160p.UHD.BluRay.x265-TERMiNAL.mkv",
    dst: "D:\\Media\\Incoming\\Interstellar.2014.IMAX.2160p.UHD.BluRay.x265-TERMiNAL.mkv",
    size: Math.round(31.4 * GiB),
    state: "failed",
    priority: 0,
    bytesDone: Math.round(6.02 * GiB),
    attempt: 3,
    nextRetryAt: null,
    error: "read tcp 192.168.1.20:51844->203.0.113.5:22: connection reset by peer",
    createdAt: iso(75),
    updatedAt: iso(47),
  },
];

export const SIM: Record<number, Sim> = {
  101: { lanes: 8, laneRate: Math.round(4.1 * MiB) }, // ≈ 33 MiB/s
  102: { lanes: 4, laneRate: Math.round(2.3 * MiB) }, // ≈ 9 MiB/s
};
