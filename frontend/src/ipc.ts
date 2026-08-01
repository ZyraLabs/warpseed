/* The only module that touches Wails-generated bindings and runtime.
   Everything else imports from here (ux-spec/plan facade rule). */
import {
  CancelTransfer,
  ClearDoneTransfers,
  ConnectSite,
  DeleteSite,
  DisconnectSite,
  EnqueueDownloads,
  ListLocal,
  ListRemote,
  LocalHome,
  LocalRoots,
  PauseTransfer,
  RemoteHome,
  ResolvePrompt,
  ResumeTransfer,
  SaveSite,
  SchemaVersion,
  Sites,
  TransfersList,
} from "../wailsjs/go/main/App";
import { EventsOn } from "../wailsjs/runtime/runtime";

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
}

export interface DownloadItem {
  src: string;
  size: number;
}

export interface TransferProgress {
  id: number;
  bytes: number;
  size: number;
}

export interface TransferState {
  id: number;
  state: string;
  error?: string;
}

/** Connect a site's browse session and resolve its SFTP home directory. */
export async function connectAndHome(id: number): Promise<string> {
  await ConnectSite(id);
  return (RemoteHome(id) as Promise<string>).catch(() => "/");
}

export const enqueueDownloads = (
  siteId: number,
  items: DownloadItem[],
  localDir: string,
): Promise<number[]> => EnqueueDownloads(siteId, items as never, localDir) as Promise<number[]>;

export const transfersList = (): Promise<Transfer[]> =>
  TransfersList() as unknown as Promise<Transfer[]>;
export const pauseTransfer = (id: number): Promise<void> => PauseTransfer(id);
export const resumeTransfer = (id: number): Promise<void> => ResumeTransfer(id);
export const cancelTransfer = (id: number): Promise<void> => CancelTransfer(id);
export const clearDoneTransfers = (): Promise<void> => ClearDoneTransfers();
