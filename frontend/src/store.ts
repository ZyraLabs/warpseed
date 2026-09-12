/* UI state only — directory contents stay in the pane hook (server state).
   Transfers are a live event-driven mirror of the queue (refetched on
   queue:changed; progress overlaid from transfer:progress events). */
import { create } from "zustand";
import { transfersList, type PaneSource, type Site, type Transfer } from "./ipc";
import type { PromptSpec } from "./components/PromptDialog";

interface ProgressSample {
  bytes: number;
  at: number; // ms timestamp of last sample
  rate: number; // EMA bytes/sec
  chunks?: number[]; // per-connection completion fractions
}

export type PaneSide = 0 | 1;

/** One line of the flight-view session log (ring buffer, newest first). */
export interface SessionEvent {
  at: number; // epoch ms
  kind: "info" | "ok" | "err";
  text: string;
}

const SESSION_LOG_CAP = 100;

/** Every refresh read gets a ticket; only the newest ticket may land, so
    two reads resolving out of order cannot roll the list back. A read that
    started before a transfer:state patch would also roll that row back, so
    patches are remembered with the ticket current at the time and overlaid
    on any list whose read started earlier. Dropping such reads instead
    would, under sustained churn, mean no list ever lands. */
let refreshTicket = 0;
interface Patch {
  state: string;
  error?: string;
  ticket: number;
}
const patches = new Map<number, Patch>();

interface PaneState {
  source: PaneSource;
  path: string;
}

interface UiState {
  panes: [PaneState, PaneState];
  activePane: PaneSide;
  dbSchemaVersion: number;
  sites: Site[];
  connStates: Record<number, string>;
  paletteOpen: boolean;
  quickConnect: { open: boolean; side: PaneSide };
  transfers: Transfer[];
  progress: Record<number, ProgressSample>;
  queueOpen: boolean;
  /** Queue-wide pause, mirrored from the backend (queue:paused). */
  queuePaused: boolean;
  settingsOpen: boolean;
  closeGuardOpen: boolean;
  viewMode: "browse" | "flight" | "deck" | "timeline";
  confirm: PromptSpec | null;
  miniMode: boolean;
  sessionLog: SessionEvent[];

  setPane: (side: PaneSide, source: PaneSource, path: string) => void;
  setPath: (side: PaneSide, path: string) => void;
  setActivePane: (side: PaneSide) => void;
  setDbSchemaVersion: (v: number) => void;
  setSites: (s: Site[]) => void;
  setConnState: (siteId: number, state: string) => void;
  setPaletteOpen: (open: boolean) => void;
  setQuickConnect: (open: boolean, side?: PaneSide) => void;
  /** Refetch the queue list — the only write path for it; stale or
      superseded responses are dropped. */
  refreshTransfers: () => Promise<void>;
  applyProgress: (id: number, bytes: number, size: number, chunks?: number[]) => void;
  patchTransferState: (id: number, state: string, error?: string) => void;
  setQueueOpen: (open: boolean) => void;
  setQueuePaused: (on: boolean) => void;
  setSettingsOpen: (open: boolean) => void;
  setCloseGuardOpen: (open: boolean) => void;
  /** Raise a confirmation. Destructive actions go through this rather than
      calling their IPC directly; see askConfirm's note. */
  askConfirm: (spec: PromptSpec) => void;
  closeConfirm: () => void;
  setViewMode: (mode: "browse" | "flight" | "deck" | "timeline") => void;
  setMiniMode: (on: boolean) => void;
  pushSessionEvent: (kind: SessionEvent["kind"], text: string) => void;
}

// Warnings the user has silenced for this run. Module scope, not persisted:
// see askConfirm.
const suppressed = new Set<string>();

export const useUiStore = create<UiState>((set) => ({
  panes: [
    { source: "local", path: "" },
    { source: "local", path: "" },
  ],
  activePane: 0,
  dbSchemaVersion: 0,
  sites: [],
  connStates: {},
  paletteOpen: false,
  quickConnect: { open: false, side: 1 },
  transfers: [],
  progress: {},
  queueOpen: false,
  queuePaused: false,
  settingsOpen: false,
  closeGuardOpen: false,
  confirm: null,
  viewMode: "browse",
  miniMode: false,
  sessionLog: [],

  setPane: (side, source, path) =>
    set((s) => {
      const panes = [...s.panes] as UiState["panes"];
      panes[side] = { source, path };
      return { panes };
    }),
  setPath: (side, path) =>
    set((s) => {
      const panes = [...s.panes] as UiState["panes"];
      panes[side] = { ...panes[side], path };
      return { panes };
    }),
  setActivePane: (side) => set({ activePane: side }),
  setDbSchemaVersion: (v) => set({ dbSchemaVersion: v }),
  setSites: (sites) => set({ sites }),
  setConnState: (siteId, state) =>
    set((s) => ({ connStates: { ...s.connStates, [siteId]: state } })),
  setPaletteOpen: (open) => set({ paletteOpen: open }),
  setQuickConnect: (open, side) =>
    set((s) => ({ quickConnect: { open, side: side ?? s.quickConnect.side } })),
  refreshTransfers: () => {
    const mine = ++refreshTicket;
    return transfersList()
      .then((list) => {
        if (mine !== refreshTicket) return;
        // The dispatcher writes a state before it emits it, so a patch
        // received before this read started is already in the rows; only
        // later ones need overlaying, and the earlier ones can be forgotten.
        for (const [id, p] of patches) if (p.ticket <= mine) patches.delete(id);
        const transfers = patches.size
          ? list.map((t) => {
              const p = patches.get(t.id);
              return p ? { ...t, state: p.state, error: p.error ?? t.error } : t;
            })
          : list;
        set({ transfers });
      })
      .catch(() => undefined);
  },
  applyProgress: (id, bytes, _size, chunks) =>
    set((s) => {
      const now = performance.now();
      const prev = s.progress[id];
      let rate = prev?.rate ?? 0;
      if (prev && now > prev.at) {
        const inst = ((bytes - prev.bytes) * 1000) / (now - prev.at);
        rate = prev.rate === 0 ? inst : prev.rate * 0.7 + inst * 0.3; // EMA smoothing
      }
      return {
        progress: { ...s.progress, [id]: { bytes, at: now, rate, chunks: chunks ?? prev?.chunks } },
      };
    }),
  patchTransferState: (id, state, error) => {
    patches.set(id, { state, error, ticket: refreshTicket });
    set((s) => ({
      transfers: s.transfers.map((t) =>
        t.id === id ? { ...t, state, error: error ?? t.error } : t,
      ),
    }));
  },
  setQueueOpen: (queueOpen) => set({ queueOpen }),
  setQueuePaused: (queuePaused) => set({ queuePaused }),
  setSettingsOpen: (settingsOpen) => set({ settingsOpen }),
  setCloseGuardOpen: (closeGuardOpen) => set({ closeGuardOpen }),

  // Every destructive action asks first, from one place, so the wording and
  // the escape hatch stay consistent and no new delete button can quietly
  // ship without one. A suppressKey lets the user silence that ONE kind of
  // warning for the rest of the run — agreeing to skip the file-delete
  // prompt says nothing about cancelling a transfer. Suppression lives in
  // memory on purpose: a relaunch asks again, because a habit formed during
  // one session should not follow someone into the next.
  askConfirm: (spec) => {
    if (spec.suppressKey && suppressed.has(spec.suppressKey)) {
      spec.onConfirm("");
      return;
    }
    set({
      confirm: {
        ...spec,
        onSuppress: (on) => {
          if (on && spec.suppressKey) suppressed.add(spec.suppressKey);
        },
      },
    });
  },
  closeConfirm: () => set({ confirm: null }),
  setViewMode: (viewMode) => set({ viewMode }),
  setMiniMode: (miniMode) => set({ miniMode }),
  pushSessionEvent: (kind, text) =>
    set((s) => ({
      sessionLog: [{ at: Date.now(), kind, text }, ...s.sessionLog].slice(0, SESSION_LOG_CAP),
    })),
}));
