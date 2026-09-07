/** Fire a toast. Toasts.tsx listens for this event, so any component can
    raise one without threading a callback down the tree. */
export function toast(kind: "info" | "error" | "success", text: string) {
  window.dispatchEvent(new CustomEvent("ws:toast", { detail: { kind, text } }));
}
