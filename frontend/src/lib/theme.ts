/* Theme mechanism: data-theme on <html>, Clay as the no-attribute fallback.
   A localStorage mirror lets main.tsx stamp the theme before first render
   (no flash); the settings database remains the source of truth. Legacy ids
   (v3 themes, "dark"/"light", "system") coerce to the nearest v4 theme. */

export type ThemeId = "clay" | "cobalt" | "iris";
/** "system" survives in stored settings from older builds; it coerces to clay. */
export type ThemePref = ThemeId | "system";

export interface ThemeInfo {
  id: ThemePref;
  name: string;
  blurb: string;
  /** Swatch preview: ground, card, accent. */
  swatch: [string, string, string];
}

/** Hex mirrors of each palette, for the picker's preview chips only —
    the app itself always reads the tokens in tokens.css. */
export const THEMES: ThemeInfo[] = [
  {
    id: "clay",
    name: "Clay",
    blurb: "Warm paper, cream cards, terracotta in motion.",
    swatch: ["#f6f0e8", "#fffdf9", "#c65a33"],
  },
  {
    id: "cobalt",
    name: "Cobalt",
    blurb: "Cool off-white, sharp white cards, electric blue.",
    swatch: ["#f4f2ee", "#ffffff", "#2242ff"],
  },
  {
    id: "iris",
    name: "Iris",
    blurb: "The dark one — violet-black ground, periwinkle glow.",
    swatch: ["#0e0b1f", "#171332", "#8b7bff"],
  },
];

const MIRROR_KEY = "ws-theme";

function stamp(theme: ThemeId) {
  document.documentElement.dataset.theme = theme;
}

/** Older builds stored v3 theme ids, "dark"/"light", or "system";
    map every legacy value to its nearest v4 theme. */
export function coerceTheme(value: string | null): ThemeId {
  switch (value) {
    case "clay":
    case "cobalt":
    case "iris":
      return value;
    case "dark":
    case "flightdeck":
    case "nightshift":
      return "iris";
    case "press":
      return "cobalt";
    // "light", "drafting", "system" and anything unknown land on the default.
    default:
      return "clay";
  }
}

export function applyTheme(pref: ThemePref) {
  const resolved = coerceTheme(pref);
  localStorage.setItem(MIRROR_KEY, resolved);
  stamp(resolved);
}

/** Called synchronously in main.tsx, before first render. */
export function applyMirroredTheme() {
  applyTheme(coerceTheme(localStorage.getItem(MIRROR_KEY)));
}
