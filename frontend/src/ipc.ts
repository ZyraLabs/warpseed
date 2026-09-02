/* The only module that touches Wails-generated bindings and runtime.
   Everything else imports from here (ux-spec/plan facade rule). */
import {
  CancelTransfer,
  ClearDoneTransfers,
  ConnectSite,
  DeleteLocal,
  DeleteRemote,
  DeleteSite,
  DisconnectSite,
  EnqueueDownloads,
  LogDir,
  EnqueueUploads,
  GetSettings,
  AddBookmark,
  BackupData,
  BookmarksFor,
  DataLocation,
  DeleteBookmark,
  DiskSpace as DiskSpaceCall,
  OpenDataFolder,
  ListLocal,
  ListRemote,
  LocalHome,
  LocalRoots,
  SetSiteRemotePath,
  MkdirLocal,
  MkdirRemote,
  PauseTransfer,
  RemoteHome,
  RenameLocal,
  RenameRemote,
  ResolvePrompt,
  ResumeTransfer,
  SaveSite,
  SchemaVersion,
  SetMiniMode,
  SetSetting,
  Sites,
  TransfersList,
} from "../wailsjs/go/main/App";
import { BrowserOpenURL, EventsOn } from "../wailsjs/runtime/runtime";

/** Open a URL in the user's default browser (donate/website links). */
export const openExternal = (url: string): void => BrowserOpenURL(url);

export interface FsEntry {
  name: string;
  isDir: boolean;
  size: number; // -1 for directories
  modTime: string; // RFC3339 UTC
  mode: string;
}

export interface Listing {
  path: string;
  parent: string;
  entries: FsEntry[];
}

export interface FsRoot {
  path: string;
  label: string;
}

export interface Site {
  id: number;
  name: string;
  protocol: string;
  host: string;
  port: number;
  username: string;
  credRef: string;
  optionsJson: string;
  remotePath: string;
  maxTransfers: number;
  createdAt: string;
  updatedAt: string;
}

/** A pane reads from the local disk or from a connected site. */
export type PaneSource = "local" | number;

export async function list(source: PaneSource, path: string): Promise<Listing> {
  const l = source === "local" ? await ListLocal(path) : await ListRemote(source, path);
  return { path: l.path, parent: l.parent, entries: (l.entries ?? []) as FsEntry[] };
}

export const localHome = (): Promise<string> => LocalHome();
export const localRoots = (): Promise<FsRoot[]> => LocalRoots() as Promise<FsRoot[]>;
export const schemaVersion = (): Promise<number> => SchemaVersion();
export const setMiniMode = (on: boolean): Promise<void> => SetMiniMode(on);

export const sites = (): Promise<Site[]> => Sites() as unknown as Promise<Site[]>;
export const saveSite = (site: Partial<Site>, password: string): Promise<Site> =>
  SaveSite(site as never, password) as unknown as Promise<Site>;
export const deleteSite = (id: number): Promise<void> => DeleteSite(id);
export const connectSite = (id: number): Promise<void> => ConnectSite(id);
export const disconnectSite = (id: number): Promise<void> => DisconnectSite(id);
export const resolvePrompt = (promptId: string, answer: boolean): Promise<void> =>
  ResolvePrompt(promptId, answer);

/** Subscribe to a backend event; returns an unsubscribe function. */
export function on<T = unknown>(event: string, cb: (payload: T) => void): () => void {
  return EventsOn(event, cb as (...data: unknown[]) => void);
}

export interface HostKeyPrompt {
  promptId: string;
  siteId: number;
  host: string;
  algo: string;
  fingerprint: string;
}

export interface ConnState {
  siteId: number;
  state: "connecting" | "connected" | "disconnected" | "error";
}

/** Emitted whenever a directory's contents change (transfer completed, file
    deleted/renamed/created) so panes showing it can reload themselves. */
export interface FsChanged {
  source: "local" | "remote";
  siteId: number;
  dir: string;
}

// --- file operations ---

export const deleteEntries = (
  source: PaneSource,
  paths: string[],
  dir: string,
): Promise<number> =>
  source === "local" ? DeleteLocal(paths, dir) : DeleteRemote(source, paths, dir);

export const renameEntry = (
  source: PaneSource,
  path: string,
  newName: string,
  dir: string,
): Promise<void> =>
  source === "local"
    ? RenameLocal(path, newName, dir)
    : RenameRemote(source, path, newName, dir);

/** Opens the folder holding warpseed.log and returns its path. */
export const logDir = (): Promise<string> => LogDir() as Promise<string>;

// --- bookmarks and pane defaults ---

export interface Bookmark {
  id: number;
  siteId: number;
  path: string;
  label: string;
  createdAt: string;
}

/** Storage uses siteId 0 for the local filesystem. */
export const sourceKey = (source: PaneSource): number =>
  source === "local" ? 0 : source;

/** Resolve where a local pane should open: the configured default folder
    when it still exists, else the home directory. A saved default pointing
    at a folder that has since gone must not wedge the pane on every launch. */
export async function localStart(configured?: string): Promise<string> {
  const want = configured?.trim();
  if (want) {
    try {
      await ListLocal(want);
      return want;
    } catch {
      // fall through to home
    }
  }
  return LocalHome().catch(() => "/");
}

export const bookmarksFor = (source: PaneSource): Promise<Bookmark[]> =>
  BookmarksFor(sourceKey(source)) as unknown as Promise<Bookmark[]>;
export const addBookmark = (source: PaneSource, path: string, label: string): Promise<void> =>
  AddBookmark(sourceKey(source), path, label);
export const deleteBookmark = (id: number): Promise<void> => DeleteBookmark(id);
export const setSiteRemotePath = (siteId: number, path: string): Promise<void> =>
  SetSiteRemotePath(siteId, path);

export const makeDir = (source: PaneSource, parent: string, name: string): Promise<void> =>
  source === "local" ? MkdirLocal(parent, name) : MkdirRemote(source, parent, name);

// --- transfers ---

export interface Transfer {
  id: number;
  siteId: number;
  engine: string;
  direction: string;
  src: string;
  dst: string;
  size: number;
  state: string;
  priority: number;
  bytesDone: number;
  attempt: number;
  nextRetryAt: string | null;
  error: string | null;
  createdAt: string;
  updatedAt: string;
  /** Start of the CURRENT run, re-stamped on every claim; null until a
      transfer has actually been picked up. */
  startedAt?: string | null;
  /** bytes_done when this run began — a resumed transfer moved
      size - startBytes, not size. */
  startBytes?: number;
}

export interface DownloadItem {
  src: string;
  size: number;
  isDir: boolean;
}

export interface UploadItem {
  src: string;
  size: number;
  isDir: boolean;
}

export interface TransferProgress {
  id: number;
  bytes: number;
  size: number;
  /** Per-chunk completion fractions for multi-connection transfers. */
  chunks?: number[];
}

export interface TransferState {
  id: number;
  state: string;
  error?: string;
}

/** Connect a site's browse session and resolve its opening directory: the
    site's configured initial remote path when it still exists, else the
    SFTP home (a stale configured path must not wedge the pane). */
export async function connectAndHome(id: number, remotePath?: string): Promise<string> {
  await ConnectSite(id);
  const configured = remotePath?.trim();
  if (configured) {
    try {
      await ListRemote(id, configured);
      return configured;
    } catch {
      // fall through to home
    }
  }
  return (RemoteHome(id) as Promise<string>).catch(() => "/");
}

export const enqueueDownloads = (
  siteId: number,
  items: DownloadItem[],
  localDir: string,
): Promise<number[]> => EnqueueDownloads(siteId, items as never, localDir) as Promise<number[]>;

export const enqueueUploads = (
  siteId: number,
  items: UploadItem[],
  remoteDir: string,
): Promise<number[]> => EnqueueUploads(siteId, items as never, remoteDir) as Promise<number[]>;

export interface DataInfo {
  path: string;
  folder: string;
  backups: string[];
}

export interface DiskSpace {
  free: number;
  total: number;
}

export const diskSpace = (path: string): Promise<DiskSpace> =>
  DiskSpaceCall(path) as Promise<DiskSpace>;

export const dataLocation = (): Promise<DataInfo> =>
  DataLocation() as unknown as Promise<DataInfo>;
export const backupData = (): Promise<string> => BackupData();
export const openDataFolder = (): Promise<void> => OpenDataFolder();

export const getSettings = (): Promise<Record<string, string>> =>
  GetSettings() as Promise<Record<string, string>>;
export const setSetting = (key: string, value: string): Promise<void> => SetSetting(key, value);

export const transfersList = (): Promise<Transfer[]> =>
  TransfersList() as unknown as Promise<Transfer[]>;
export const pauseTransfer = (id: number): Promise<void> => PauseTransfer(id);
export const resumeTransfer = (id: number): Promise<void> => ResumeTransfer(id);
export const cancelTransfer = (id: number): Promise<void> => CancelTransfer(id);
export const clearDoneTransfers = (): Promise<void> => ClearDoneTransfers();
