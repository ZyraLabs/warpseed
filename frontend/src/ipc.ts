/* The only module that touches Wails-generated bindings. Everything else
   imports from here — swapping runtimes (v3, or another shell) is contained
   to this file plus the Go events facade. */
import { ListLocal, LocalHome, LocalRoots, SchemaVersion } from "../wailsjs/go/main/App";

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

export async function listLocal(path: string): Promise<Listing> {
  const l = await ListLocal(path);
  return { path: l.path, parent: l.parent, entries: (l.entries ?? []) as FsEntry[] };
}

export async function localHome(): Promise<string> {
  return LocalHome();
}

export async function localRoots(): Promise<FsRoot[]> {
  return (await LocalRoots()) as FsRoot[];
}

export async function schemaVersion(): Promise<number> {
  return SchemaVersion();
}
