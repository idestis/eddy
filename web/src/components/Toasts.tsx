import { createContext, type ReactNode, useCallback, useContext, useRef, useState } from "react";
import { motionMs } from "../lib/motion";
import { Icon } from "./Icon";

export type ToastKind = "ok" | "bad" | "info";

export interface ToastAction {
  label: string;
  run: () => void;
}

export interface ToastOptions {
  /** One follow-up, such as Undo after a suspend. */
  action?: ToastAction;
}

interface Toast {
  id: number;
  kind: ToastKind;
  text: string;
  action?: ToastAction;
  leaving?: boolean;
}

type ShowToast = (text: string, kind?: ToastKind, options?: ToastOptions) => void;

const ToastContext = createContext<ShowToast>(() => {});

export const useToast = (): ShowToast => useContext(ToastContext);

const TTL_MS = 3400;
/** Toasts with an action stay longer, so there is time to use it. */
const ACTION_TTL_MS = 6000;
const MAX_TOASTS = 4;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(0);

  // Leaving toasts play their exit, then go.
  const dismiss = useCallback((id: number) => {
    const wait = motionMs("base");
    if (wait === 0) {
      setToasts((t) => t.filter((x) => x.id !== id));
      return;
    }
    setToasts((t) => t.map((x) => (x.id === id ? { ...x, leaving: true } : x)));
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), wait);
  }, []);

  const show = useCallback<ShowToast>(
    (text, kind = "info", options) => {
      const id = next.current++;
      setToasts((t) => [...t.slice(-(MAX_TOASTS - 1)), { id, kind, text, action: options?.action }]);
      setTimeout(() => dismiss(id), options?.action ? ACTION_TTL_MS : TTL_MS);
    },
    [dismiss],
  );

  return (
    <ToastContext.Provider value={show}>
      {children}
      <div
        className="pointer-events-none fixed right-6 bottom-7 z-[70] flex flex-col items-end gap-2"
        role="status"
        aria-live="polite"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            className={`flex max-w-[440px] items-start gap-[9px] rounded-[13px] bg-ink px-3.5 py-[11px] text-13 text-surface shadow-toast ${t.leaving ? "anim-toast-out" : "anim-toast-in"}`}
          >
            <Icon
              name={t.kind === "ok" ? "check" : t.kind === "bad" ? "alert" : "info"}
              className={`mt-px size-4 shrink-0 ${t.kind === "ok" ? "text-code-ok" : t.kind === "bad" ? "text-code-error" : ""}`}
            />
            <span className="min-w-0">{t.text}</span>
            {t.action && !t.leaving && (
              <button
                type="button"
                className="pointer-events-auto -my-1 -mr-1.5 ml-1 shrink-0 rounded-lg px-2 py-1 text-12-5 font-semibold text-surface underline decoration-surface/40 underline-offset-2 hover:bg-surface/12"
                onClick={() => {
                  dismiss(t.id);
                  t.action?.run();
                }}
              >
                {t.action.label}
              </button>
            )}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
