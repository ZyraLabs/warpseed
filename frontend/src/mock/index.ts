/* Browser-only mock of the Wails bridge, for documentation screenshots and
   design work without a Go backend. Installed by main.tsx only when the page
   is loaded with ?mock=1 or VITE_MOCK=1; production builds never import it.

   It fakes exactly two globals the generated bindings read:
     window.go.main.App.<Method>  — frontend/wailsjs/go/main/App.js
     window.runtime.*             — frontend/wailsjs/runtime/runtime.js
   and drives the same events the Go side emits (see internal/events and
   internal/dispatch): transfer:progress, transfer:state, queue:changed,
   site:connstate, fs:changed, app:info/app:error. */
import type { Bookmark, Listing, Site, Transfer } from "../ipc";
import { useUiStore } from "../store";
import {
  BOOKMARKS,
  CONNECTED_SITE_ID,
  DATA_INFO,
  DISK,
  LOCAL,
  LOCAL_HOME,
  LOCAL_ROOTS,
  REMOTE,
  SETTINGS,
  SIM,
  SITES,
  TRANSFERS,
} from "./data";

type Listener = (...data: unknown[]) => void;

// --- event bus -------------------------------------------------------------

const listeners = new Map<string, Set<Listener>>();

function emit(event: string, ...data: unknown[]) {
  for (const cb of listeners.get(event) ?? []) {
    try {
      cb(...data);
    } catch (err) {
      console.error(`[mock] listener for ${event} threw`, err);
    }
  }
}

function eventsOn(event: string, cb: Listener): () => void {
  let set = listeners.get(event);
  if (!set) listeners.set(event, (set = new Set()));
  set.add(cb);
  return () => set?.delete(cb);
}

// --- state -----------------------------------------------------------------

const state = {
  sites: SITES.map((s) => ({ ...s })),
  transfers: TRANSFERS.map((t) => ({ ...t })),
  bookmarks: BOOKMARKS.map((b) => ({ ...b })),
  // `?theme=cobalt|iris` picks the demo theme (settings are the source of
  // truth for the theme, so the localStorage mirror alone would be overridden).
  settings: { ...SETTINGS, "ui.theme": new URLSearchParams(window.location.search).get("theme") ?? SETTINGS["ui.theme"] } as Record<string, string>,
  connected: new Set<number>([CONNECTED_SITE_ID]),
  /** Live per-transfer simulation: bytes and per-lane fractions. */
  sim: new Map<number, { bytes: number; lanes: number[]; laneLen: number; laneRate: number }>(),
  nextId: 200,
};

const delay = (ms = 40) => new Promise<void>((r) => setTimeout(r, ms));

// --- path helpers ----------------------------------------------------------

function localKey(p: string): string {
  let k = p.replace(/\//g, "\\");
  if (/^[A-Za-z]:$/.test(k)) k += "\\";
  if (k.length > 3) k = k.replace(/\\+$/, "");
  return k;
}
function localParent(k: string): string {
  if (/^[A-Za-z]:\\$/.test(k)) return "";
  const i = k.lastIndexOf("\\");
  return i <= 2 ? k.slice(0, 3) : k.slice(0, i);
}
function remoteKey(p: string): string {
  const k = p.replace(/\\/g, "/").replace(/\/+$/, "");
  return k === "" ? "/" : k;
}
function remoteParent(k: string): string {
  if (k === "/") return "";
  const i = k.lastIndexOf("/");
  return i <= 0 ? "/" : k.slice(0, i);
}
function sortEntries(list: Listing["entries"]): Listing["entries"] {
  return [...list].sort((a, b) =>
    a.isDir !== b.isDir ? (a.isDir ? -1 : 1) : a.name.localeCompare(b.name, undefined, { sensitivity: "base" }),
  );
}

// --- transfer simulation ---------------------------------------------------

function startSim(t: Transfer) {
  const cfg = SIM[t.id] ?? { lanes: Number(state.settings["transfers.chunk_streams"] || 4), laneRate: 4 * 1024 * 1024 };
  const laneLen = t.size / cfg.lanes;
  const frac = t.size > 0 ? t.bytesDone / t.size : 0;
  // Spread progress unevenly across lanes so the hyperlane bar has texture.
  const lanes = Array.from({ length: cfg.lanes }, (_, i) =>
    Math.max(0, Math.min(1, frac + (((i * 7919) % 13) - 6) * 0.025)),
  );
  state.sim.set(t.id, { bytes: t.bytesDone, lanes, laneLen, laneRate: cfg.laneRate });
}

function tick(dtSec: number) {
  for (const t of state.transfers) {
    if (t.state !== "active") continue;
    const s = state.sim.get(t.id);
    if (!s) continue;
    let sum = 0;
    for (let i = 0; i < s.lanes.length; i++) {
      // ±12 % jitter per lane per tick — real links breathe.
      const jitter = 0.88 + Math.random() * 0.24;
      s.lanes[i] = Math.min(1, s.lanes[i] + (s.laneRate * jitter * dtSec) / s.laneLen);
      sum += s.lanes[i] * s.laneLen;
    }
    s.bytes = Math.min(t.size, Math.round(sum));
    t.bytesDone = s.bytes;
    emit("transfer:progress", { id: t.id, bytes: s.bytes, size: t.size, chunks: [...s.lanes] });
    if (s.bytes >= t.size) finish(t);
  }
}

function setState(t: Transfer, st: string, error?: string) {
  t.state = st;
  t.error = error ?? null;
  t.updatedAt = new Date().toISOString();
  const payload: Record<string, unknown> = { id: t.id, state: st };
  if (error) payload.error = error;
  emit("transfer:state", payload);
  emit("queue:changed", null);
}

function finish(t: Transfer) {
  state.sim.delete(t.id);
  emit("transfer:progress", { id: t.id, bytes: t.size, size: t.size });
  setState(t, "completed");
  emit("fs:changed", { source: "local", siteId: 0, dir: localParent(localKey(t.dst)) });
  dispatchNext();
}

function dispatchNext() {
  const next = state.transfers.find((t) => t.state === "pending");
  if (!next) return;
  startSim(next);
  setState(next, "active");
}

// --- window.go.main.App ----------------------------------------------------

const App = {
  async SchemaVersion() {
    await delay();
    return 7;
  },
  async Sites(): Promise<Site[]> {
    await delay();
    return state.sites.map((s) => ({ ...s }));
  },
  async SaveSite(site: Partial<Site>, _password: string): Promise<Site> {
    await delay(120);
    const now = new Date().toISOString();
    if (site.id && site.id > 0) {
      const i = state.sites.findIndex((s) => s.id === site.id);
      if (i >= 0) {
        state.sites[i] = { ...state.sites[i], ...site, updatedAt: now } as Site;
        return { ...state.sites[i] };
      }
    }
    const created: Site = {
      id: state.nextId++,
      name: site.name || site.host || "new site",
      protocol: site.protocol || "sftp",
      host: site.host || "",
      port: site.port || 22,
      username: site.username || "",
      credRef: "",
      optionsJson: "{}",
      remotePath: site.remotePath || "",
      maxTransfers: site.maxTransfers || 4,
      createdAt: now,
      updatedAt: now,
    };
    state.sites.push(created);
    return { ...created };
  },
  async DeleteSite(id: number) {
    await delay();
    state.sites = state.sites.filter((s) => s.id !== id);
  },
  async ConnectSite(id: number) {
    if (!state.sites.some((s) => s.id === id)) throw new Error(`site ${id} not found`);
    if (state.connected.has(id)) return;
    emit("site:connstate", { siteId: id, state: "connecting" });
    await delay(450);
    state.connected.add(id);
    emit("site:connstate", { siteId: id, state: "connected" });
  },
  async DisconnectSite(id: number) {
    await delay();
    state.connected.delete(id);
    emit("site:connstate", { siteId: id, state: "disconnected" });
  },
  async RemoteHome(id: number) {
    await delay();
    return id === 3 ? "/volume1/media" : "/home/seedling";
  },
  async SetSiteRemotePath(id: number, p: string) {
    const s = state.sites.find((x) => x.id === id);
    if (s) s.remotePath = p;
  },
  async ListRemote(id: number, p: string): Promise<Listing> {
    await delay(90);
    if (!state.connected.has(id)) throw new Error(`site ${id}: not connected`);
    const tree = REMOTE[id] ?? {};
    const key = remoteKey(p);
    const entries = tree[key];
    if (!entries) throw new Error(`sftp: stat ${key}: no such file or directory`);
    return { path: key, parent: remoteParent(key), entries: sortEntries(entries) };
  },
  async ListLocal(p: string): Promise<Listing> {
    await delay(30);
    const key = localKey(p);
    const entries = LOCAL[key];
    if (!entries) throw new Error(`open ${key}: The system cannot find the path specified.`);
    return { path: key, parent: localParent(key), entries: sortEntries(entries) };
  },
  async LocalHome() {
    return LOCAL_HOME;
  },
  async LocalRoots() {
    return LOCAL_ROOTS.map((r) => ({ ...r }));
  },
  async DiskSpace(_p: string) {
    return { ...DISK };
  },
  async BookmarksFor(siteId: number): Promise<Bookmark[]> {
    await delay();
    return state.bookmarks.filter((b) => b.siteId === siteId).map((b) => ({ ...b }));
  },
  async AddBookmark(siteId: number, p: string, label: string) {
    state.bookmarks.push({ id: state.nextId++, siteId, path: p, label, createdAt: new Date().toISOString() });
  },
  async DeleteBookmark(id: number) {
    state.bookmarks = state.bookmarks.filter((b) => b.id !== id);
  },
  async MkdirLocal(parent: string, name: string) {
    const key = localKey(parent);
    (LOCAL[key] ??= []).push({ name, isDir: true, size: -1, modTime: new Date().toISOString(), mode: "drwxr-xr-x" });
    LOCAL[`${key}${key.endsWith("\\") ? "" : "\\"}${name}`] = [];
    emit("fs:changed", { source: "local", siteId: 0, dir: key });
  },
  async MkdirRemote(id: number, parent: string, name: string) {
    const key = remoteKey(parent);
    const tree = (REMOTE[id] ??= {});
    (tree[key] ??= []).push({ name, isDir: true, size: -1, modTime: new Date().toISOString(), mode: "drwxr-xr-x" });
    tree[key === "/" ? `/${name}` : `${key}/${name}`] = [];
    emit("fs:changed", { source: "remote", siteId: id, dir: key });
  },
  async DeleteLocal(paths: string[], dirPath: string) {
    const key = localKey(dirPath);
    const names = new Set(paths.map((p) => p.split(/[\\/]/).pop()));
    LOCAL[key] = (LOCAL[key] ?? []).filter((e) => !names.has(e.name));
    emit("fs:changed", { source: "local", siteId: 0, dir: key });
    return paths.length;
  },
  async DeleteRemote(id: number, paths: string[], dirPath: string) {
    const key = remoteKey(dirPath);
    const names = new Set(paths.map((p) => p.split("/").pop()));
    const tree = (REMOTE[id] ??= {});
    tree[key] = (tree[key] ?? []).filter((e) => !names.has(e.name));
    emit("fs:changed", { source: "remote", siteId: id, dir: key });
    return paths.length;
  },
  async RenameLocal(p: string, newName: string, dirPath: string) {
    const key = localKey(dirPath);
    const old = p.split(/[\\/]/).pop();
    for (const e of LOCAL[key] ?? []) if (e.name === old) e.name = newName;
    emit("fs:changed", { source: "local", siteId: 0, dir: key });
  },
  async RenameRemote(id: number, p: string, newName: string, dirPath: string) {
    const key = remoteKey(dirPath);
    const old = p.split("/").pop();
    for (const e of REMOTE[id]?.[key] ?? []) if (e.name === old) e.name = newName;
    emit("fs:changed", { source: "remote", siteId: id, dir: key });
  },
  async MoveLocal(paths: string[], _dest: string, _dir: string) {
    return paths.length;
  },
  async EnqueueDownloads(siteId: number, items: { src: string; size: number; isDir: boolean }[], localDir: string) {
    await delay(80);
    const ids: number[] = [];
    const now = new Date().toISOString();
    for (const it of items) {
      const name = it.src.split("/").pop() ?? it.src;
      const t: Transfer = {
        id: state.nextId++,
        siteId,
        engine: "sftpfast",
        direction: "download",
        src: it.src,
        dst: `${localKey(localDir)}\\${name}`,
        size: it.isDir ? 0 : it.size,
        state: "pending",
        priority: 0,
        bytesDone: 0,
        attempt: 0,
        nextRetryAt: null,
        error: null,
        createdAt: now,
        updatedAt: now,
      };
      state.transfers.push(t);
      ids.push(t.id);
    }
    emit("app:info", `Queued ${items.length} download${items.length === 1 ? "" : "s"}`);
    emit("queue:changed", null);
    return ids;
  },
  async EnqueueUploads(siteId: number, items: { src: string; size: number; isDir: boolean }[], remoteDir: string) {
    await delay(80);
    const ids: number[] = [];
    const now = new Date().toISOString();
    for (const it of items) {
      const name = it.src.split(/[\\/]/).pop() ?? it.src;
      const t: Transfer = {
        id: state.nextId++,
        siteId,
        engine: "sftpfast",
        direction: "upload",
        src: it.src,
        dst: `${remoteKey(remoteDir)}/${name}`,
        size: it.isDir ? 0 : it.size,
        state: "pending",
        priority: 0,
        bytesDone: 0,
        attempt: 0,
        nextRetryAt: null,
        error: null,
        createdAt: now,
        updatedAt: now,
      };
      state.transfers.push(t);
      ids.push(t.id);
    }
    emit("app:info", `Queued ${items.length} upload${items.length === 1 ? "" : "s"}`);
    emit("queue:changed", null);
    return ids;
  },
  async TransfersList(): Promise<Transfer[]> {
    await delay();
    return state.transfers.map((t) => ({ ...t }));
  },
  async PauseTransfer(id: number) {
    const t = state.transfers.find((x) => x.id === id);
    if (t && t.state === "active") {
      state.sim.delete(id);
      setState(t, "paused");
      dispatchNext();
    }
  },
  async ResumeTransfer(id: number) {
    const t = state.transfers.find((x) => x.id === id);
    if (t && (t.state === "paused" || t.state === "failed")) {
      t.attempt += 1;
      startSim(t);
      setState(t, "active");
    }
  },
  async CancelTransfer(id: number) {
    const t = state.transfers.find((x) => x.id === id);
    if (t) {
      state.sim.delete(id);
      setState(t, "cancelled");
    }
  },
  async ClearDoneTransfers() {
    state.transfers = state.transfers.filter((t) => t.state !== "completed" && t.state !== "cancelled");
    emit("queue:changed", null);
  },
  async GetSettings() {
    await delay();
    return { ...state.settings };
  },
  async SetSetting(key: string, value: string) {
    state.settings[key] = value;
    emit("settings:changed", { key, value });
  },
  async DataLocation() {
    return { ...DATA_INFO, backups: [...DATA_INFO.backups] };
  },
  async BackupData() {
    await delay(200);
    const p = `${DATA_INFO.folder}\\backups\\warpseed-${new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19)}.db`;
    DATA_INFO.backups.unshift(p);
    return p;
  },
  async OpenDataFolder() {},
  async ResolvePrompt(_id: string, _answer: boolean) {},
  async SetMiniMode(_on: boolean) {},
};

// --- window.runtime --------------------------------------------------------

const noop = () => undefined;
const runtime = {
  EventsOn: eventsOn,
  EventsOnce: (event: string, cb: Listener) => {
    const off = eventsOn(event, (...d) => {
      off();
      cb(...d);
    });
    return off;
  },
  // Wails' runtime.js implements EventsOn as EventsOnMultiple(name, cb, -1):
  // a non-positive max means "forever".
  EventsOnMultiple: (event: string, cb: Listener, max: number) => {
    if (max <= 0) return eventsOn(event, cb);
    let n = 0;
    const off = eventsOn(event, (...d) => {
      if (++n >= max) off();
      cb(...d);
    });
    return off;
  },
  EventsOff: (event: string, ...more: string[]) => {
    for (const e of [event, ...more]) listeners.delete(e);
  },
  EventsOffAll: () => listeners.clear(),
  EventsEmit: emit,
  LogPrint: noop, LogTrace: noop, LogDebug: noop, LogInfo: noop, LogWarning: noop, LogError: noop, LogFatal: noop,
  WindowReload: noop, WindowReloadApp: noop, WindowSetAlwaysOnTop: noop,
  WindowSetSystemDefaultTheme: noop, WindowSetLightTheme: noop, WindowSetDarkTheme: noop,
  WindowCenter: noop, WindowSetTitle: (t: string) => { document.title = t; },
  WindowFullscreen: noop, WindowUnfullscreen: noop, WindowIsFullscreen: async () => false,
  WindowSetSize: noop, WindowGetSize: async () => ({ w: window.innerWidth, h: window.innerHeight }),
  WindowSetMaxSize: noop, WindowSetMinSize: noop, WindowSetPosition: noop,
  WindowGetPosition: async () => ({ x: 0, y: 0 }),
  WindowHide: noop, WindowShow: noop, WindowMaximise: noop, WindowToggleMaximise: noop,
  WindowUnmaximise: noop, WindowIsMaximised: async () => false, WindowMinimise: noop,
  WindowUnminimise: noop, WindowIsMinimised: async () => false, WindowIsNormal: async () => true,
  WindowSetBackgroundColour: noop, ScreenGetAll: async () => [],
  BrowserOpenURL: (url: string) => console.info("[mock] BrowserOpenURL", url),
  Environment: async () => ({ buildType: "dev", platform: "windows", arch: "amd64" }),
  Quit: noop, Hide: noop, Show: noop,
  ClipboardGetText: async () => "", ClipboardSetText: async () => true,
  OnFileDrop: noop, OnFileDropOff: noop,
  CanResolveFilePaths: () => false, ResolveFilePaths: noop,
};

// --- install ---------------------------------------------------------------

export function installMock() {
  const w = window as unknown as Record<string, unknown>;
  w.go = { main: { App } };
  w.runtime = runtime;
  // Wails' generated runtime.js also reads window.wails in some builds.
  w.wails ??= {};

  for (const t of state.transfers) if (t.state === "active") startSim(t);

  // Seed the session log once the app has subscribed: connection state,
  // then "in flight" lines for the running transfers.
  window.setTimeout(() => {
    for (const id of state.connected) emit("site:connstate", { siteId: id, state: "connected" });
    emit("app:info", "hyperion connected — Hyperlane ×8 available");
    for (const t of state.transfers) {
      if (t.state === "active") emit("transfer:state", { id: t.id, state: "active" });
    }
  }, 400);

  let last = performance.now();
  const iv = window.setInterval(() => {
    const now = performance.now();
    tick((now - last) / 1000);
    last = now;
  }, 500);

  // Debug helpers for driving the UI from the console / DevTools.
  w.__wsMock = {
    state,
    emit,
    store: useUiStore,
    stop: () => window.clearInterval(iv),
    /** Attach a pane to a site (connects it first) at the given remote path. */
    async openRemote(side: 0 | 1, siteId = CONNECTED_SITE_ID, path?: string) {
      await App.ConnectSite(siteId);
      const site = state.sites.find((s) => s.id === siteId);
      useUiStore.getState().setPane(side, siteId, path ?? site?.remotePath ?? "/");
    },
  };
  console.info("[mock] warpseed mock backend installed — window.__wsMock");
}
