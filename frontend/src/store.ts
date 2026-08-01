/* UI state only — directory contents stay in the pane hook (server state). */
import { create } from "zustand";
import type { PaneSource, Site } from "./ipc";

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

  setPane: (side: PaneSide, source: PaneSource, path: string) => void;
  setPath: (side: PaneSide, path: string) => void;
  setActivePane: (side: PaneSide) => void;
  setDbSchemaVersion: (v: number) => void;
  setSites: (s: Site[]) => void;
  setConnState: (siteId: number, state: string) => void;
  setPaletteOpen: (open: boolean) => void;
  setQuickConnect: (open: boolean, side?: PaneSide) => void;
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
}));
