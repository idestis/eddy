import { type KeyboardEvent, useId, useState } from "react";
import type { InstallGuide as Guide, JoinToken } from "../api/types";
import { dateTime } from "../lib/format";
import { expiresIn } from "../lib/onboarding";
import { CopyButton } from "./CopyButton";
import { Icon } from "./Icon";

type GuideTab = "helm" | "values" | "manifests" | "clusterResource";

const TABS: ReadonlyArray<{ id: GuideTab; label: string; hint: string }> = [
  { id: "helm", label: "Helm", hint: "Run this with a kubeconfig for the new cluster." },
  {
    id: "values",
    label: "values.yaml",
    hint: "For a HelmRelease or Argo CD Application. Prefer a SOPS or External Secrets reference for the token.",
  },
  { id: "manifests", label: "Manifests", hint: "Plain YAML for clusters without Helm: kubectl apply -f -" },
  {
    id: "clusterResource",
    label: "Cluster resource",
    hint: "The Cluster CR in the management cluster, if you would rather keep it in Git.",
  },
];

/** The install guide as tabs of copyable code, plus warnings and the network tip. */
export function InstallGuide({ guide, joinToken }: { guide: Guide; joinToken?: JoinToken }) {
  const [tab, setTab] = useState<GuideTab>("helm");
  const id = useId();
  const current = TABS.find((t) => t.id === tab) ?? TABS[0];
  const text = guide[tab];

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    e.stopPropagation();
    const i = TABS.findIndex((t) => t.id === tab);
    const next = TABS[(i + step + TABS.length) % TABS.length];
    if (!next) return;
    setTab(next.id);
    document.getElementById(`${id}-${next.id}`)?.focus();
  };

  return (
    <div className="flex min-w-0 flex-col gap-3">
      {joinToken && (
        <p className="flex flex-wrap items-center gap-x-1.5 text-12-5 text-ink-2">
          <Icon name="clock" className="size-3.5 shrink-0 text-ink-3" />
          The join token works once and expires{" "}
          <b className="font-semibold text-ink">{expiresIn(joinToken.expiresAt)}</b>
          <span className="text-ink-3">({dateTime(joinToken.expiresAt)})</span>. Eddy cannot show it again.
        </p>
      )}
      {guide.warnings?.map((w) => (
        <div
          key={w}
          className="flex items-start gap-2 rounded-xl border border-warn/40 bg-warn/12 px-3 py-2 text-12-5 text-ink-2"
          role="status"
        >
          <Icon name="alert" className="mt-px size-4 shrink-0 text-warn" />
          {w}
        </div>
      ))}
      <div>
        <div
          role="tablist"
          aria-label="Install with"
          className="flex gap-1 overflow-x-auto border-b border-line no-scrollbar"
          onKeyDown={onKeyDown}
        >
          {TABS.map((t) => (
            <button
              key={t.id}
              id={`${id}-${t.id}`}
              type="button"
              role="tab"
              aria-selected={t.id === tab}
              aria-controls={`${id}-panel`}
              tabIndex={t.id === tab ? 0 : -1}
              className="-mb-px border-b-2 border-transparent px-3 py-2 text-13 font-medium whitespace-nowrap text-ink-3 hover:text-ink aria-selected:border-c aria-selected:text-ink"
              onClick={() => setTab(t.id)}
            >
              {t.label}
            </button>
          ))}
        </div>
        <div
          id={`${id}-panel`}
          role="tabpanel"
          aria-labelledby={`${id}-${tab}`}
          className="flex flex-col gap-2 pt-3"
        >
          <div className="flex items-start justify-between gap-3">
            <p className="text-12-5 text-ink-3">{current?.hint}</p>
            <CopyButton text={text} label={`Copy ${current?.label ?? ""}`} className="btn btn-sm shrink-0" />
          </div>
          <pre className="m-0 max-h-[280px] min-w-0 overflow-auto rounded-control bg-code-bg px-3 py-2.5 font-mono text-12 whitespace-pre text-code-ink">
            {text}
          </pre>
        </div>
      </div>
      <div className="flex items-start gap-2 rounded-xl border border-line bg-surface-sunken px-3 py-2.5 text-12-5 text-ink-2">
        <Icon name="globe" className="mt-px size-4 shrink-0 text-ink-3" />
        <span>
          <b className="font-semibold text-ink">Network requirements.</b> The agent only dials out: allow
          outbound HTTPS (443) from the cluster to <code className="text-12">{hostOf(guide.hubURL)}</code>. No
          inbound port is needed. For private networks, reach the endpoint over PrivateLink or VPC peering.{" "}
          <a
            className="text-c underline underline-offset-3"
            href={guide.networkDocs}
            target="_blank"
            rel="noreferrer"
          >
            Read the network guide
          </a>
        </span>
      </div>
    </div>
  );
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}
