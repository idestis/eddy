import type { ReactNode } from "react";
import type { ClusterInfo, Status } from "../api/types";
import { STATUS_LABEL } from "../lib/format";
import { hint, isSequence, type KeyId } from "../lib/keys";
import type { Requested } from "../lib/liveMotion";
import { Icon } from "./Icon";

/**
 * The status colour tokens (design/tokens.css). Rows, chips, cluster cards, the palette
 * and trees all use these, so a status looks the same everywhere.
 */
export const STATUS_TEXT: Record<Status | "attention", string> = {
  ready: "text-ok",
  failed: "text-bad",
  reconciling: "text-run",
  suspended: "text-off",
  unknown: "text-off",
  // Healthy but finished: the ready colour, muted, so live objects stand out.
  completed: "text-ok/70",
  attention: "text-attn",
};

const SVG = "size-4 shrink-0";

/** Filled status glyphs from the prototype, coloured with the status tokens. */
export function StatusIcon({
  status,
  label,
  className = "",
}: {
  status: Status;
  label?: boolean;
  className?: string;
}) {
  let glyph: ReactNode;
  switch (status) {
    case "ready":
      glyph = (
        <svg className={SVG} viewBox="0 0 16 16" aria-hidden="true">
          <circle cx="8" cy="8" r="7" fill="currentColor" />
          <path
            d="M4.9 8.2l2.1 2.1 4.2-4.5"
            fill="none"
            stroke="var(--color-surface)"
            strokeWidth="1.8"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      );
      break;
    case "failed":
      glyph = (
        <svg className={SVG} viewBox="0 0 16 16" aria-hidden="true">
          <circle cx="8" cy="8" r="7" fill="currentColor" />
          <path
            d="M5.6 5.6l4.8 4.8M10.4 5.6l-4.8 4.8"
            fill="none"
            stroke="var(--color-surface)"
            strokeWidth="1.8"
            strokeLinecap="round"
          />
        </svg>
      );
      break;
    case "reconciling":
      glyph = (
        <svg
          className={`${SVG} animate-spin motion-reduce:animate-[spin_3s_linear_infinite]`}
          viewBox="0 0 16 16"
          aria-hidden="true"
        >
          <circle
            cx="8"
            cy="8"
            r="6"
            fill="none"
            stroke="currentColor"
            strokeOpacity=".25"
            strokeWidth="2.2"
          />
          <path
            d="M8 2a6 6 0 0 1 6 6"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.2"
            strokeLinecap="round"
          />
        </svg>
      );
      break;
    case "completed":
      glyph = (
        <svg
          className={SVG}
          viewBox="0 0 16 16"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.7"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
        >
          <circle cx="8" cy="8" r="6.3" />
          <path d="M5.3 8.2l1.9 1.9 3.6-3.9" />
        </svg>
      );
      break;
    case "suspended":
      glyph = (
        <svg
          className={SVG}
          viewBox="0 0 16 16"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.7"
          strokeLinecap="round"
          aria-hidden="true"
        >
          <circle cx="8" cy="8" r="6.3" />
          <path d="M6.4 5.7v4.6M9.6 5.7v4.6" />
        </svg>
      );
      break;
    default:
      glyph = (
        <svg
          className={SVG}
          viewBox="0 0 16 16"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.7"
          strokeLinecap="round"
          aria-hidden="true"
        >
          <circle cx="8" cy="8" r="6.3" strokeDasharray="2.5 2.5" />
        </svg>
      );
  }
  const cls = `inline-flex ${STATUS_TEXT[status]} ${className}`;
  return label ? (
    <span className={cls} role="img" aria-label={STATUS_LABEL[status]}>
      {glyph}
    </span>
  ) : (
    <span className={cls}>{glyph}</span>
  );
}

const REQUESTED_LABEL: Record<Requested["action"], string> = {
  reconcile: "Reconcile requested",
  suspend: "Suspend requested",
  resume: "Resume requested",
};

/** A small "requested" marker for a row or header waiting for an action's result. */
export function RequestedBadge({ req, className = "" }: { req: Requested; className?: string }) {
  return (
    <span
      className={`anim-fade-in inline-flex shrink-0 items-center gap-1.5 rounded-md border border-c/30 bg-c-soft px-1.5 py-px text-11 font-semibold whitespace-nowrap text-c ${className}`}
      title="Sent. Waiting for the cluster to report a new status."
    >
      <span className="requested-dot size-1.5 rounded-full bg-c" />
      {REQUESTED_LABEL[req.action]}
    </span>
  );
}

/** A small spinner for buttons whose request is in flight. */
export function Spinner({ className = "" }: { className?: string }) {
  return (
    <svg
      className={`size-4 shrink-0 animate-spin motion-reduce:animate-[spin_3s_linear_infinite] ${className}`}
      viewBox="0 0 16 16"
      aria-hidden="true"
    >
      <circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" strokeOpacity=".3" strokeWidth="2" />
      <path d="M8 2a6 6 0 0 1 6 6" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

/** The "Not ready" glyph: an amber ring, the union of failed and reconciling. */
export function AttentionIcon({ className = "" }: { className?: string }) {
  return (
    <span className={`inline-flex ${STATUS_TEXT.attention} ${className}`}>
      <svg className={SVG} viewBox="0 0 16 16" aria-hidden="true">
        <circle cx="8" cy="8" r="7" fill="currentColor" />
        <path
          d="M8 4.6v4.2M8 11.2v.1"
          stroke="var(--color-surface)"
          strokeWidth="1.9"
          strokeLinecap="round"
        />
      </svg>
    </span>
  );
}

export function StatusPill({ status }: { status: Status }) {
  return (
    <span
      className={`inline-flex items-center gap-1.5 whitespace-nowrap text-12-5 font-medium ${STATUS_TEXT[status]}`}
    >
      <StatusIcon status={status} />
      <span>{STATUS_LABEL[status]}</span>
    </span>
  );
}

const HB = "inline-flex items-center gap-[3px] text-12 font-semibold tabular-nums [&_svg]:size-[13px]";

/** Failing / reconciling / suspended counts of a cluster, or a single tick when all is well. */
export function Health({ cluster }: { cluster: ClusterInfo }) {
  if (!cluster.connected) {
    return (
      <span className={`${HB} text-bad`} title="Disconnected">
        <Icon name="alert" />
      </span>
    );
  }
  const counts = cluster.counts ?? {};
  const shown = (["failed", "reconciling", "suspended"] as const).filter((s) => (counts[s] ?? 0) > 0);
  if (shown.length === 0) {
    return (
      <span className={`${HB} text-ok`} title="All ready">
        <StatusIcon status="ready" />
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-2">
      {shown.map((s) => (
        <span
          key={s}
          className={`${HB} ${STATUS_TEXT[s]}`}
          title={`${counts[s]} ${STATUS_LABEL[s].toLowerCase()}`}
        >
          <StatusIcon status={s} />
          {counts[s]}
        </span>
      ))}
    </span>
  );
}

/**
 * Keycaps for a shortcut. A sequence such as "g f" renders as one keycap, read out as
 * "g then f", instead of two loose boxes.
 */
export function Keys({
  keys,
  sequence,
  className = "",
  keyId,
}: {
  keys: string[];
  sequence?: boolean;
  className?: string;
  /** The binding shown, so a screen can be checked for one key hinted for two actions. */
  keyId?: KeyId;
}) {
  if (sequence && keys.length > 1) {
    return (
      <kbd
        className={className}
        aria-label={keys.join(" then ")}
        title={`Press ${keys.join(", then ")}`}
        data-key-id={keyId}
      >
        {keys.join(" ")}
      </kbd>
    );
  }
  return (
    <span className={`inline-flex gap-[3px] ${className}`} data-key-id={keyId}>
      {keys.map((k, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: keycaps are positional
        <kbd key={i}>{k}</kbd>
      ))}
    </span>
  );
}

/** The hint for a registered binding (lib/keys.ts), next to buttons and commands. */
export function KeyHint({ id, className }: { id: KeyId; className?: string }) {
  return <Keys keys={hint(id)} sequence={isSequence(id)} className={className} keyId={id} />;
}
