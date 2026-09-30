import { createContext, type ReactNode, useCallback, useContext, useId, useRef, useState } from "react";
import type { AskConfirm, ConfirmRequest } from "../lib/confirm";
import { Modal } from "./Modal";

interface ConfirmDialogProps {
  request: ConfirmRequest;
  onDone: (typed: string | null) => void;
}

export function ConfirmDialog({ request, onDone }: ConfirmDialogProps) {
  const [value, setValue] = useState("");
  const inputId = useId();
  const ok = value.trim() === request.expected;

  return (
    <Modal label={request.title} onClose={() => onDone(null)} className="modal" placement="center">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (ok) onDone(value.trim());
        }}
      >
        <h3>{request.title}</h3>
        <p>{request.body}</p>
        <label htmlFor={inputId}>
          Type <b className="mono">{request.expected}</b> to confirm
        </label>
        <input
          id={inputId}
          autoComplete="off"
          spellCheck={false}
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
        <div className="mact">
          <button type="button" className="btn" onClick={() => onDone(null)}>
            Cancel
          </button>
          <button type="submit" className="btn danger" disabled={!ok}>
            {request.action}
          </button>
        </div>
      </form>
    </Modal>
  );
}

const ConfirmContext = createContext<AskConfirm | null>(null);

export function useConfirm(): AskConfirm {
  const ask = useContext(ConfirmContext);
  if (!ask) throw new Error("useConfirm must be used inside <ConfirmProvider>");
  return ask;
}

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [request, setRequest] = useState<ConfirmRequest | null>(null);
  const resolver = useRef<((v: string | null) => void) | null>(null);

  const ask = useCallback<AskConfirm>(
    (req) =>
      new Promise((resolve) => {
        resolver.current?.(null);
        resolver.current = resolve;
        setRequest(req);
      }),
    [],
  );

  const done = (typed: string | null) => {
    resolver.current?.(typed);
    resolver.current = null;
    setRequest(null);
  };

  return (
    <ConfirmContext.Provider value={ask}>
      {children}
      {request && <ConfirmDialog request={request} onDone={done} />}
    </ConfirmContext.Provider>
  );
}
