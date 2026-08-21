import { useEffect, useState } from "react";
import { on } from "../ipc";
import { Check, Disc, Warning } from "./Icon";

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
    const offInfo = on<string>("app:info", (msg) => push("success", msg));
    const local = (ev: Event) => {
      const { kind, text } = (ev as CustomEvent<{ kind: Toast["kind"]; text: string }>).detail;
      push(kind, text);
    };
    window.addEventListener("ws:toast", local);
    return () => {
      offErr();
      offInfo();
      window.removeEventListener("ws:toast", local);
    };
  }, []);

  if (toasts.length === 0) return null;
  return (
    <div className="toasts">
      {toasts.map((t) => (
        <div key={t.id} className={`toast toast--${t.kind}`} role="status">
          <span className="toast__icon">
            {t.kind === "error" ? (
              <Warning size={15} />
            ) : t.kind === "success" ? (
              <Check size={15} />
            ) : (
              <Disc size={15} />
            )}
          </span>
          <span className="toast__text">{t.text}</span>
        </div>
      ))}
    </div>
  );
}
