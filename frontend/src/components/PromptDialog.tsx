import { useEffect, useRef, useState } from "react";

export interface PromptSpec {
  title: string;
  body?: string;
  /** Present for text prompts; absent for a plain confirm. */
  initialValue?: string;
  confirmLabel: string;
  danger?: boolean;
  /** Set on destructive confirms to offer "don't ask again this session".
      The key groups the actions that are silenced together, so agreeing to
      skip the warning for deleting files says nothing about cancelling a
      transfer. Handled by the store's askConfirm; see store.ts. */
  suppressKey?: string;
  onConfirm: (value: string) => void;
  /** Called with the checkbox state when a suppressible confirm is
      accepted. */
  onSuppress?: (suppress: boolean) => void;
}

/** One dialog for confirm ("Delete 3 items?") and text entry ("New name"),
    keyboard-first: Enter confirms, Esc cancels, input starts selected. */
export default function PromptDialog({
  spec,
  onClose,
}: {
  spec: PromptSpec | null;
  onClose: () => void;
}) {
  const [value, setValue] = useState("");
  const [suppress, setSuppress] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!spec) return;
    setValue(spec.initialValue ?? "");
    setSuppress(false);
    requestAnimationFrame(() => {
      inputRef.current?.focus();
      inputRef.current?.select();
    });
  }, [spec]);

  if (!spec) return null;

  const confirm = () => {
    if (spec.initialValue !== undefined && value.trim() === "") return;
    spec.onSuppress?.(suppress);
    spec.onConfirm(value.trim());
    onClose();
  };

  return (
    <div className="scrim scrim--center" onMouseDown={onClose}>
      <div
        className="dialog dialog--prompt"
        onMouseDown={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.stopPropagation();
            onClose();
          } else if (e.key === "Enter") {
            e.preventDefault();
            confirm();
          }
        }}
        role="dialog"
        aria-label={spec.title}
      >
        <h2>{spec.title}</h2>
        {spec.body && <p>{spec.body}</p>}
        {spec.initialValue !== undefined && (
          <input
            ref={inputRef}
            className="prompt__input"
            value={value}
            spellCheck={false}
            onChange={(e) => setValue(e.target.value)}
            aria-label={spec.title}
          />
        )}
        {spec.suppressKey && (
          <label className="prompt__suppress">
            <input
              type="checkbox"
              checked={suppress}
              onChange={(e) => setSuppress(e.target.checked)}
            />
            Don&rsquo;t ask again until warpseed restarts
          </label>
        )}
        <div className="dialog__actions">
          <button className="btn" autoFocus={spec.danger} onClick={onClose}>
            Cancel
          </button>
          <button
            className={`btn ${spec.danger ? "btn--danger" : "btn--primary"}`}
            // Destructive confirms start unfocused: focus goes to Cancel, so
            // a stray Enter or Space cannot delete anything.
            autoFocus={spec.initialValue === undefined && !spec.danger}
            onClick={confirm}
          >
            {spec.confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
