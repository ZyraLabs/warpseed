import { useEffect, useRef, useState } from "react";

export interface PromptSpec {
  title: string;
  body?: string;
  /** Present for text prompts; absent for a plain confirm. */
  initialValue?: string;
  confirmLabel: string;
  danger?: boolean;
  onConfirm: (value: string) => void;
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
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!spec) return;
    setValue(spec.initialValue ?? "");
    requestAnimationFrame(() => {
      inputRef.current?.focus();
      inputRef.current?.select();
    });
  }, [spec]);

  if (!spec) return null;

  const confirm = () => {
    if (spec.initialValue !== undefined && value.trim() === "") return;
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
        <div className="dialog__actions">
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className={`btn ${spec.danger ? "btn--danger" : "btn--primary"}`}
            autoFocus={spec.initialValue === undefined}
            onClick={confirm}
          >
            {spec.confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
