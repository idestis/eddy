import {
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { pushOverlay } from "../lib/keys";

const FOCUSABLE =
  "a[href], button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex='-1'])";

interface ModalProps {
  label: string;
  onClose: () => void;
  children: ReactNode;
  className?: string;
  /**
   * "top" pins the dialog near the top (palette, help), "center" is for small confirms,
   * "anchor" opens a popover below `anchor` (the cluster switcher).
   */
  placement?: "top" | "center" | "anchor";
  /** The element an "anchor" popover opens from. */
  anchor?: HTMLElement | null;
}

const SCRIM: Record<NonNullable<ModalProps["placement"]>, string> = {
  top: "items-start justify-center bg-scrim px-4 pb-4 pt-[max(10vh,16px)]",
  center: "items-start justify-center bg-scrim px-4 pb-4 pt-[max(18vh,16px)]",
  anchor: "bg-scrim/40",
};

const GAP = 8;

/** Places an anchored popover below its trigger, flipped or clamped to stay on screen. */
function useAnchorPosition(anchor: HTMLElement | null | undefined, dialog: HTMLElement | null) {
  // Transparent (not hidden) until placed, so the dialog can take focus straight away.
  const [style, setStyle] = useState<CSSProperties>({ opacity: 0 });
  useLayoutEffect(() => {
    if (!anchor || !dialog) return;
    const place = () => {
      const a = anchor.getBoundingClientRect();
      const d = dialog.getBoundingClientRect();
      const vw = window.innerWidth;
      const vh = window.innerHeight;
      const left = Math.max(GAP, Math.min(a.left, vw - d.width - GAP));
      const below = a.bottom + GAP;
      const top = below + d.height > vh - GAP ? Math.max(GAP, a.top - d.height - GAP) : below;
      setStyle({ position: "absolute", left, top });
    };
    place();
    window.addEventListener("resize", place);
    return () => window.removeEventListener("resize", place);
  }, [anchor, dialog]);
  return style;
}

/**
 * A modal dialog, rendered through a portal into document.body so its scrim covers the
 * whole viewport whatever the layout around the caller. Focus is trapped, Escape and
 * outside clicks close it, and focus returns to where it was. While open, list and
 * detail shortcuts pause.
 */
export function Modal({ label, onClose, children, className = "", placement = "top", anchor }: ModalProps) {
  const ref = useRef<HTMLDivElement>(null);
  const [dialog, setDialog] = useState<HTMLDivElement | null>(null);
  const anchored = useAnchorPosition(placement === "anchor" ? anchor : null, dialog);
  const setRef = useCallback((el: HTMLDivElement | null) => {
    ref.current = el;
    setDialog(el);
  }, []);

  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const release = pushOverlay();
    const el = ref.current;
    const focusInside = () => {
      if (el && !el.contains(document.activeElement)) {
        (el.querySelector<HTMLElement>("[autofocus], input, textarea") ?? el).focus();
      }
    };
    focusInside();
    // A second try after layout, in case the first landed before the dialog was focusable.
    const frame = requestAnimationFrame(focusInside);
    return () => {
      cancelAnimationFrame(frame);
      release();
      if (previous?.isConnected) previous.focus({ preventScroll: true });
    };
  }, []);

  const onKeyDown = (e: ReactKeyboardEvent) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key !== "Tab" || !ref.current) return;
    const nodes = [...ref.current.querySelectorAll<HTMLElement>(FOCUSABLE)];
    const first = nodes[0];
    const last = nodes[nodes.length - 1];
    if (!first || !last) return;
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  return createPortal(
    // biome-ignore lint/a11y/noStaticElementInteractions: the scrim only closes on outside click; keyboard users press Escape
    <div
      data-scrim={placement}
      className={`fixed inset-0 z-40 flex ${SCRIM[placement]}`}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      onKeyDown={onKeyDown}
    >
      <div
        ref={setRef}
        className={`flex max-h-[74vh] w-[min(640px,100%)] flex-col overflow-hidden rounded-dialog border border-line bg-surface shadow-dialog outline-none ${className}`}
        style={placement === "anchor" ? anchored : undefined}
        role="dialog"
        aria-modal="true"
        aria-label={label}
        tabIndex={-1}
      >
        {children}
      </div>
    </div>,
    document.body,
  );
}
