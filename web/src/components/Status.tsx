import type { ReactNode } from "react";
import type { ClusterInfo, Status } from "../api/types";
import { STATUS_LABEL } from "../lib/format";
import { Icon } from "./Icon";

/** Filled status glyphs from the prototype; the colour comes from .st-<status>. */
export function StatusIcon({ status, label }: { status: Status; label?: boolean }) {
  let glyph: ReactNode;
  switch (status) {
    case "ready":
      glyph = (
        <svg className="i" viewBox="0 0 16 16" aria-hidden="true">
          <circle cx="8" cy="8" r="7" fill="currentColor" />
          <path
            d="M4.9 8.2l2.1 2.1 4.2-4.5"
            fill="none"
            stroke="var(--win)"
            strokeWidth="1.8"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      );
      break;
    case "failed":
      glyph = (
        <svg className="i" viewBox="0 0 16 16" aria-hidden="true">
          <circle cx="8" cy="8" r="7" fill="currentColor" />
          <path
            d="M5.6 5.6l4.8 4.8M10.4 5.6l-4.8 4.8"
            fill="none"
            stroke="var(--win)"
            strokeWidth="1.8"
            strokeLinecap="round"
          />
        </svg>
      );
      break;
    case "reconciling":
      glyph = (
        <svg className="i spin" viewBox="0 0 16 16" aria-hidden="true">
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
    case "suspended":
      glyph = (
        <svg
          className="i"
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
          className="i"
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
  return label ? (
    <span className={`st st-${status}`} role="img" aria-label={STATUS_LABEL[status]}>
      {glyph}
    </span>
  ) : (
    <span className={`st st-${status}`}>{glyph}</span>
  );
}

export function StatusPill({ status }: { status: Status }) {
  return (
    <span className={`stp st-${status}`}>
      <StatusIcon status={status} />
      <span className="lbl">{STATUS_LABEL[status]}</span>
    </span>
  );
}

/** Failing / reconciling / suspended counts of a cluster, or a single tick when all is well. */
export function Health({ cluster }: { cluster: ClusterInfo }) {
  if (!cluster.connected) {
    return (
      <span className="hb failed" title="Disconnected">
        <Icon name="alert" />
      </span>
    );
  }
  const counts = cluster.counts ?? {};
  const shown = (["failed", "reconciling", "suspended"] as const).filter((s) => (counts[s] ?? 0) > 0);
  if (shown.length === 0) {
    return (
      <span className="hb ready" title="All ready">
        <StatusIcon status="ready" />
      </span>
    );
  }
  return (
    <span className="health">
      {shown.map((s) => (
        <span key={s} className={`hb ${s}`} title={`${counts[s]} ${STATUS_LABEL[s].toLowerCase()}`}>
          <StatusIcon status={s} />
          {counts[s]}
        </span>
      ))}
    </span>
  );
}

export function Keys({ keys }: { keys: string[] }) {
  return (
    <span className="kbds">
      {keys.map((k, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: a sequence like "g g" repeats keys
        <kbd key={i}>{k}</kbd>
      ))}
    </span>
  );
}
