/* Theme mechanism: data-theme on <html>, Flight Deck as the no-attribute
   fallback, System following prefers-color-scheme live. A localStorage
   mirror lets main.tsx stamp the theme before first render (no flash); the
   settings database remains the source of truth. */

export type ThemeId = "flightdeck" | "drafting" | "press" | "nightshift";
export type ThemePref = ThemeId | "system";

export interface ThemeInfo {
  id: ThemePref;
  name: string;
  blurb: string;
  /** Swatch preview: ground, panel, accent. */
  swatch: [string, string, string];
}

/** Hex mirrors of each palette, for the picker's preview chips only —
    the app itself always reads the oklch tokens in tokens.css. */
export const THEMES: ThemeInfo[] = [
  {
    id: "flightdeck",
    name: "Flight Deck",
    blurb: "Cockpit instrument — cold slate, one teal, tightest grid.",
    swatch: ["#0b1114", "#182329", "#2fd6bd"],
  },
  {
    id: "drafting",
    name: "Drafting Table",
    blurb: "Engineering drawing — cool paper, navy ink, vermilion in motion.",
    swatch: ["#dfe6ec", "#fbfcfd", "#cf4520"],
  },
  {
    id: "press",
    name: "Press",
    blurb: "Bone paper, black rules, no rounding, industrial orange.",
    swatch: ["#e6e6df", "#f6f6f1", "#f0500a"],
  },
  {
    id: "nightshift",
    name: "Nightshift",
    blurb: "Warm dark — amber on near-black, serif chrome.",
    swatch: ["#12100d", "#241f1a", "#e9a53f"],
  },
  {
    id: "system",
    name: "System",
    blurb: "Follows Windows: Flight Deck when dark, Drafting Table when light.",
    swatch: ["#12100d", "#dfe6ec", "#2fd6bd"],
  },
];

const MIRROR_KEY = "ws-theme";
const SYSTEM_DARK: ThemeId = "flightdeck";
const SYSTEM_LIGHT: ThemeId = "drafting";

let mediaQuery: MediaQueryList | null = null;
let mediaListener: ((e: MediaQueryListEvent) => void) | null = null;

function stamp(theme: ThemeId) {
  document.documentElement.dataset.theme = theme;
}

/** Older builds stored "dark"/"light"; keep those working. */
function coerce(value: string | null): ThemePref {
  switch (value) {
    case "dark":
      return "flightdeck";
    case "light":
      return "drafting";
    case "flightdeck":
    case "drafting":
    case "press":
    case "nightshift":
    case "system":
      return value;
    default:
      return "flightdeck";
  }
}

export function applyTheme(pref: ThemePref) {
  const resolved = coerce(pref);
  localStorage.setItem(MIRROR_KEY, resolved);

  if (mediaQuery && mediaListener) {
    mediaQuery.removeEventListener("change", mediaListener);
    mediaQuery = null;
    mediaListener = null;
  }

  if (resolved === "system") {
    mediaQuery = window.matchMedia("(prefers-color-scheme: light)");
    mediaListener = (e) => stamp(e.matches ? SYSTEM_LIGHT : SYSTEM_DARK);
    mediaQuery.addEventListener("change", mediaListener);
    stamp(mediaQuery.matches ? SYSTEM_LIGHT : SYSTEM_DARK);
  } else {
    stamp(resolved);
  }
}

/** Called synchronously in main.tsx, before first render. */
export function applyMirroredTheme() {
  applyTheme(coerce(localStorage.getItem(MIRROR_KEY)));
}
