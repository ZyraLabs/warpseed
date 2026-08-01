/* Theme mechanism (design-agent spec): data-theme attribute on <html>,
   dark = no-attribute fallback, System follows prefers-color-scheme live.
   A localStorage mirror lets main.tsx stamp the theme before first render
   (no flash); the settings DB remains the source of truth. */

export type ThemePref = "dark" | "light" | "system";

const MIRROR_KEY = "ws-theme";
let mediaQuery: MediaQueryList | null = null;
let mediaListener: ((e: MediaQueryListEvent) => void) | null = null;

function stamp(theme: "dark" | "light") {
  document.documentElement.dataset.theme = theme;
}

export function applyTheme(pref: ThemePref) {
  localStorage.setItem(MIRROR_KEY, pref);
  if (mediaQuery && mediaListener) {
    mediaQuery.removeEventListener("change", mediaListener);
    mediaQuery = null;
    mediaListener = null;
  }
  if (pref === "system") {
    mediaQuery = window.matchMedia("(prefers-color-scheme: light)");
    mediaListener = (e) => stamp(e.matches ? "light" : "dark");
    mediaQuery.addEventListener("change", mediaListener);
    stamp(mediaQuery.matches ? "light" : "dark");
  } else {
    stamp(pref);
  }
}

/** Called synchronously in main.tsx before render. */
export function applyMirroredTheme() {
  const pref = (localStorage.getItem(MIRROR_KEY) as ThemePref) || "dark";
  applyTheme(pref === "dark" || pref === "light" || pref === "system" ? pref : "dark");
}
