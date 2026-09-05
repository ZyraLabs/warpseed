/** Release identity — the one place a URL or version number changes. */
export const APP_VERSION = "1.1.1";
export const COMPANY = "Zyra Labs";
export const WEBSITE_URL = "https://zyralabs.tech";
export const DONATE_URL = "https://buymeacoffee.com/zyralabs";
export const REPO_URL = "https://github.com/ZyraLabs/warpseed";
export const SUPPORT_EMAIL = "warpseed@zyralabs.tech";

/**
 * A mailto: link to the support address with the version and platform
 * pre-filled, so a bug report arrives with the facts we always ask for.
 */
export function bugReportUrl(): string {
  const subject = `warpseed ${APP_VERSION} bug report`;
  const body = [
    "What happened:",
    "",
    "What I expected:",
    "",
    "Steps to reproduce:",
    "1.",
    "",
    "---",
    `warpseed ${APP_VERSION}`,
    typeof navigator !== "undefined" ? navigator.userAgent : "",
  ].join("\n");
  return `mailto:${SUPPORT_EMAIL}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
}
