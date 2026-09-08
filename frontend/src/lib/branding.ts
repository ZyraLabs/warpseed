/** Release identity. The version is NOT here: it lives in wails.json, is read
    from there by the Go side, and reaches the frontend through ipc.appVersion.
    A copy in this file drifted six releases behind and went out in the subject
    line of every emailed bug report. */
export const COMPANY = "Zyra Labs";
export const WEBSITE_URL = "https://zyralabs.tech";
export const DONATE_URL = "https://buymeacoffee.com/zyralabs";
export const REPO_URL = "https://github.com/ZyraLabs/warpseed";
export const SUPPORT_EMAIL = "warpseed@zyralabs.tech";

/**
 * A mailto: link to the support address with the version and platform
 * pre-filled, so a bug report arrives with the facts we always ask for.
 */
export function bugReportUrl(version: string): string {
  const subject = `warpseed ${version} bug report`;
  const body = [
    "What happened:",
    "",
    "What I expected:",
    "",
    "Steps to reproduce:",
    "1.",
    "",
    "---",
    `warpseed ${version}`,
    typeof navigator !== "undefined" ? navigator.userAgent : "",
  ].join("\n");
  return `mailto:${SUPPORT_EMAIL}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
}
