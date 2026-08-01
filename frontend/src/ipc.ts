/* The only module that touches Wails-generated bindings and runtime.
   Everything else imports from here (ux-spec/plan facade rule). */
import {
  ConnectSite,
  DeleteSite,
  DisconnectSite,
  ListLocal,
  ListRemote,
  LocalHome,
  LocalRoots,
  ResolvePrompt,
  SaveSite,
  SchemaVersion,
  Sites,
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
