import type { CheckState, ConnectionAttempt, ConnectionCheck, JoinTokenInfo } from "../api/types";
import { ago, dateTime } from "../lib/format";
import { CHECK_STATE_LABEL, reasonLabel } from "../lib/onboarding";
import { Icon, type IconName } from "./Icon";

const STATE_ICON: Record<CheckState, { icon: IconName; className: string }> = {
  ok: { icon: "check", className: "text-ok" },
  warn: { icon: "alert", className: "text-warn" },
  fail: { icon: "x", className: "text-bad" },
  pending: { icon: "clock", className: "text-run animate-blink motion-reduce:animate-none" },
  info: { icon: "info", className: "text-ink-3" },
};

/** The live connection checklist (ADR-0005). */
export function ConnectionChecklist({ checks }: { checks: readonly ConnectionCheck[] }) {
  if (!checks.length) return <p className="text-12-5 text-ink-3">Waiting for the hub…</p>;
  return (
    <ul className="m-0 flex list-none flex-col p-0" aria-label="Connection checks">
      {checks.map((c) => {
        const s = STATE_ICON[c.state] ?? STATE_ICON.info;
        return (
          <li key={c.id} className="flex items-start gap-2.5 border-b border-line py-2 last:border-b-0">
            <span className={`mt-0.5 flex ${s.className}`}>
              <Icon name={s.icon} className="size-4 shrink-0" label={CHECK_STATE_LABEL[c.state]} />
            </span>
            <span className="flex min-w-0 flex-col gap-0.5">
              <span className="text-13 font-medium">{c.label}</span>
              {c.detail && <span className="text-12-5 break-words text-ink-3">{c.detail}</span>}
              {c.docs && (
                <a
                  className="self-start text-12-5 text-c underline underline-offset-3"
                  href={c.docs}
                  target="_blank"
                  rel="noreferrer"
                >
                  {c.state === "warn" || c.state === "fail" ? "How to fix this" : "Learn more"}
                </a>
              )}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/** Rejected agent connections for this cluster name, newest first. */
export function RejectedAttempts({ attempts }: { attempts: readonly ConnectionAttempt[] }) {
  if (!attempts.length) {
    return <p className="text-12-5 text-ink-3">No rejected connections.</p>;
  }
  const sorted = [...attempts].sort((a, b) => b.at.localeCompare(a.at));
  return (
    <ul className="m-0 flex list-none flex-col gap-1.5 p-0" aria-label="Rejected connection attempts">
      {sorted.map((a, i) => (
        <li
          // biome-ignore lint/suspicious/noArrayIndexKey: attempts have no id; time plus position is stable enough
          key={`${a.at}-${i}`}
          className="flex flex-col gap-0.5 rounded-control border border-bad/25 bg-bad/6 px-3 py-2 text-12-5"
        >
          <span className="flex flex-wrap items-center gap-x-2">
            <Icon name="alert" className="size-3.5 shrink-0 text-bad" />
            <b className="font-semibold">{reasonLabel(a.reason)}</b>
            <time className="text-ink-3" dateTime={a.at} title={dateTime(a.at)}>
              {ago(a.at)}
            </time>
          </span>
          {a.detail && <span className="break-words text-ink-2">{a.detail}</span>}
          {(a.peer || a.hubPod) && (
            <span className="font-mono text-12 text-ink-3">
              {a.peer && `from ${a.peer}`}
              {a.peer && a.hubPod && " · "}
              {a.hubPod && `hub ${a.hubPod}`}
            </span>
          )}
        </li>
      ))}
    </ul>
  );
}

const TOKEN_STATE: Record<JoinTokenInfo["state"], string> = {
  active: "Active",
  used: "Used",
  expired: "Expired",
  revoked: "Revoked",
};

/** One line about the cluster's latest join token. */
export function JoinTokenSummary({ token }: { token: JoinTokenInfo | undefined }) {
  if (!token) return <p className="text-12-5 text-ink-3">No join token has been issued.</p>;
  const when =
    token.state === "used" && token.usedAt
      ? `used ${ago(token.usedAt)}`
      : token.state === "active"
        ? `expires ${dateTime(token.expiresAt)}`
        : `issued ${ago(token.createdAt)}`;
  return (
    <p className="flex flex-wrap items-center gap-x-2 text-12-5 text-ink-2">
      <Icon name="key" className="size-3.5 shrink-0 text-ink-3" />
      <span className={`badge${token.state === "active" ? " badge-accent" : ""}`}>
        {TOKEN_STATE[token.state]}
      </span>
      <span className="font-mono text-12 text-ink-3">{token.id}</span>
      <span>
        by {token.createdBy}, {when}
      </span>
    </p>
  );
}
