/* UI state only — directory contents stay in the pane hook (server state).
   Transfers are a live event-driven mirror of the queue (refetched on
   queue:changed; progress overlaid from transfer:progress events). */
import { create } from "zustand";
import type { PaneSource, Site, Transfer } from "./ipc";

interface ProgressSample {
  bytes: number;
  at: number; // ms timestamp of last sample
  rate: number; // EMA bytes/sec
  chunks?: number[]; // per-connection completion fractions
}

export type PaneSide = 0 | 1;

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
  settingsOpen: boolean;

  setPane: (side: PaneSide, source: PaneSource, path: string) => void;
  setPath: (side: PaneSide, path: string) => void;
  setActivePane: (side: PaneSide) => void;
  setDbSchemaVersion: (v: number) => void;
  setSites: (s: Site[]) => void;
  setConnState: (siteId: number, state: string) => void;
  setPaletteOpen: (open: boolean) => void;
  setQuickConnect: (open: boolean, side?: PaneSide) => void;
  setTransfers: (t: Transfer[]) => void;
  applyProgress: (id: number, bytes: number, size: number, chunks?: number[]) => void;
  patchTransferState: (id: number, state: string, error?: string) => void;
  setQueueOpen: (open: boolean) => void;
  setSettingsOpen: (open: boolean) => void;
}

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
  settingsOpen: false,

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
  setTransfers: (transfers) => set({ transfers }),
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
  patchTransferState: (id, state, error) =>
    set((s) => ({
      transfers: s.transfers.map((t) =>
        t.id === id ? { ...t, state, error: error ?? t.error } : t,
      ),
    })),
  setQueueOpen: (queueOpen) => set({ queueOpen }),
  setSettingsOpen: (settingsOpen) => set({ settingsOpen }),
}));
