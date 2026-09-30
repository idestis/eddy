import { createContext, type ReactNode, useCallback, useContext, useRef, useState } from "react";
import { Icon } from "./Icon";

export type ToastKind = "ok" | "bad" | "info";

interface Toast {
  id: number;
  kind: ToastKind;
  text: string;
}

type ShowToast = (text: string, kind?: ToastKind) => void;

const ToastContext = createContext<ShowToast>(() => {});

export const useToast = (): ShowToast => useContext(ToastContext);

const TTL_MS = 3400;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(0);

  const show = useCallback<ShowToast>((text, kind = "info") => {
    const id = next.current++;
    setToasts((t) => [...t.slice(-3), { id, kind, text }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), TTL_MS);
  }, []);

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
            className="flex max-w-[420px] animate-toast items-start gap-[9px] rounded-[13px] bg-ink px-3.5 py-[11px] text-13 text-surface shadow-toast motion-reduce:animate-none"
          >
            <Icon
              name={t.kind === "ok" ? "check" : t.kind === "bad" ? "alert" : "info"}
              className={`mt-px size-4 shrink-0 ${t.kind === "ok" ? "text-code-ok" : t.kind === "bad" ? "text-code-error" : ""}`}
            />
            <span>{t.text}</span>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
