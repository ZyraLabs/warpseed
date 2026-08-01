import { useEffect, useState } from "react";
import { on } from "../ipc";

interface Toast {
  id: number;
  kind: "info" | "error" | "success";
  text: string;
}

let nextId = 1;

/** Bottom-right toasts (ux-spec §7.8): app errors + local notifications via
    the ws:toast CustomEvent. Max 3 shown, auto-dismiss 5s. */
export default function Toasts() {
  const [toasts, setToasts] = useState<Toast[]>([]);

  useEffect(() => {
    const push = (kind: Toast["kind"], text: string) => {
      const t = { id: nextId++, kind, text };
      setToasts((ts) => [...ts.slice(-2), t]);
      setTimeout(() => setToasts((ts) => ts.filter((x) => x.id !== t.id)), 5000);
    };
    const offErr = on<string>("app:error", (msg) => push("error", msg));
    const local = (ev: Event) => {
      const { kind, text } = (ev as CustomEvent<{ kind: Toast["kind"]; text: string }>).detail;
      push(kind, text);
    };
    window.addEventListener("ws:toast", local);
    return () => {
      offErr();
      window.removeEventListener("ws:toast", local);
    };
  }, []);

  if (toasts.length === 0) return null;
  return (
    <div className="toasts">
      {toasts.map((t) => (
        <div key={t.id} className={`toast toast--${t.kind}`} role="status">
          {t.text}
        </div>
      ))}
    </div>
  );
}
