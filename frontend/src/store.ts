/* UI state only. Directory contents are server state and live in the pane
   hook (web patterns rule: never mirror server state into the client store). */
import { create } from "zustand";

export type PaneSide = 0 | 1;

interface PanePaths {
  path: string;
}

interface UiState {
  panes: [PanePaths, PanePaths];
  activePane: PaneSide;
  dbSchemaVersion: number;
  setPath: (side: PaneSide, path: string) => void;
  setActivePane: (side: PaneSide) => void;
  setDbSchemaVersion: (v: number) => void;
}

export const useUiStore = create<UiState>((set) => ({
  panes: [{ path: "" }, { path: "" }],
  activePane: 0,
  dbSchemaVersion: 0,
  setPath: (side, path) =>
    set((s) => {
      const panes: [PanePaths, PanePaths] = [...s.panes] as [PanePaths, PanePaths];
      panes[side] = { path };
      return { panes };
    }),
  setActivePane: (side) => set({ activePane: side }),
  setDbSchemaVersion: (v) => set({ dbSchemaVersion: v }),
}));
