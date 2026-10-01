import { useState } from "react";
import { EnvLegend } from "./EnvLegend";
import { LogoMark } from "./icons";
import { Kbd } from "./Kbd";

/*
 * An HTML/CSS/SVG illustration of the UI (styles in styles/mock.css), laid out like the app: sidebar,
 * list, detail panel, and a separate graph view. Decorative, so hidden from assistive tech.
 */

const ICONS: Record<string, string> = {
  list: "M3 4h10M3 8h10M3 12h10",
  warn: "M8 2.5 14 13H2L8 2.5ZM8 6.5v3M8 11.5v.01",
  flux: "M8 2 2 5l6 3 6-3-6-3ZM2 8l6 3 6-3M2 11l6 3 6-3",
  grid: "M2.5 2.5h4v4h-4zM9.5 2.5h4v4h-4zM2.5 9.5h4v4h-4zM9.5 9.5h4v4h-4z",
  share: "M4 8.5a1 1 0 1 0 0-1M12 4.5a1 1 0 1 0 0-1M12 12.5a1 1 0 1 0 0-1M5 7.4l6-3M5 8.6l6 3",
  db: "M3 4c0-1 2.2-1.8 5-1.8S13 3 13 4s-2.2 1.8-5 1.8S3 5 3 4ZM3 4v8c0 1 2.2 1.8 5 1.8s5-.8 5-1.8V4M3 8c0 1 2.2 1.8 5 1.8S13 9 13 8",
  globe: "M8 2a6 6 0 1 0 0 12A6 6 0 0 0 8 2ZM2 8h12M8 2c2 2 2 10 0 12M8 2c-2 2-2 10 0 12",
  server: "M2.5 3h11v4h-11zM2.5 9h11v4h-11zM5 5h.01M5 11h.01",
  key: "M10.5 5.5a2.5 2.5 0 1 0-1.8 2.4L3 13.5V15h2v-1.5h1.5V12L8 10.5",
  box: "M3 5l5-2.5L13 5v6l-5 2.5L3 11V5ZM3 5l5 2.5L13 5M8 7.5v6",
  git: "M5 3v7M5 12.5a1.5 1.5 0 1 0 0-.01M5 3.5a1.5 1.5 0 1 0 0-.01M11 5.5a1.5 1.5 0 1 0 0-.01M11 7c0 3-6 1.5-6 3.5",
  down: "M4 6l4 4 4-4",
  right: "M6 4l4 4-4 4",
  search: "M7 12a5 5 0 1 0 0-10 5 5 0 0 0 0 10ZM11 11l3 3",
  spark: "M8 2l1.2 3.3L12.5 6.5 9.2 7.7 8 11 6.8 7.7 3.5 6.5l3.3-1.2L8 2Z",
  arrow: "M3 8h9M8.5 4.5 12 8l-3.5 3.5",
  plus: "M8 3v10M3 8h10",
  minus: "M3 8h10",
  fit: "M3 6V3h3M10 3h3v3M13 10v3h-3M6 13H3v-3",
  chat: "M3 3.5h10v7H8l-3 2.5v-2.5H3v-7Z",
  lock: "M4.5 7.5h7v5.5h-7zM6 7.5V5.5a2 2 0 0 1 4 0v2",
};

function Ic({ n, className = "" }: { n: string; className?: string }) {
  return (
    <svg className={`ic ${className}`} viewBox="0 0 16 16" aria-hidden="true">
      <path d={ICONS[n]} />
    </svg>
  );
}

type Tone = "ok" | "bad" | "run" | "off" | "attn";
/** The app's status glyphs: a filled check, cross or exclamation, a spinner arc, and a pause. */
function Status({ tone }: { tone: Tone }) {
  return (
    <svg className={`st ${tone}`} viewBox="0 0 16 16" aria-hidden="true">
      {tone === "run" && (
        <>
          <circle
            cx="8"
            cy="8"
            r="6"
            fill="none"
            stroke="currentColor"
            strokeOpacity=".25"
            strokeWidth="1.6"
          />
          <path
            d="M8 2a6 6 0 0 1 6 6"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
          />
        </>
      )}
      {tone === "off" && (
        <>
          <circle cx="8" cy="8" r="6.2" fill="none" stroke="currentColor" strokeWidth="1.4" />
          <path d="M6.5 5.5v5M9.5 5.5v5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
        </>
      )}
      {(tone === "ok" || tone === "bad" || tone === "attn") && (
        <>
          <circle cx="8" cy="8" r="7" fill="currentColor" />
          <path
            d={
              tone === "ok"
                ? "M5 8.2l2 2 4-4.2"
                : tone === "bad"
                  ? "M5.7 5.7l4.6 4.6M10.3 5.7l-4.6 4.6"
                  : "M8 4.5v4M8 11v.01"
            }
            fill="none"
            stroke="var(--color-surface)"
            strokeWidth="1.7"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </>
      )}
    </svg>
  );
}

type NavNode = { label: string; n: string; icon: string; open?: boolean; kids?: NavNode[] };
const nav: NavNode[] = [
  {
    label: "Flux",
    n: "81",
    icon: "flux",
    open: true,
    kids: [
      { label: "Kustomizations", n: "27", icon: "box" },
      { label: "HelmReleases", n: "17", icon: "box" },
      {
        label: "Sources",
        n: "37",
        icon: "git",
        open: true,
        kids: [
          { label: "Git repositories", n: "1", icon: "git" },
          { label: "Helm repositories", n: "19", icon: "box" },
          { label: "Helm charts", n: "17", icon: "box" },
        ],
      },
    ],
  },
  { label: "Workloads", n: "224", icon: "grid" },
  { label: "Networking", n: "89", icon: "share" },
  { label: "Storage", n: "23", icon: "db" },
  { label: "Cluster", n: "158", icon: "globe" },
  {
    label: "Karpenter",
    n: "35",
    icon: "server",
    open: true,
    kids: [
      { label: "NodePools", n: "13", icon: "box" },
      { label: "NodeClaims", n: "13", icon: "box" },
    ],
  },
  { label: "External Secrets", n: "13", icon: "key" },
];

function NavItems({ items, top }: { items: NavNode[]; top?: boolean }) {
  return (
    <>
      {items.map((it) => (
        <div key={it.label} className={top ? undefined : "kid"}>
          <div className={`it${top ? " lead" : ""}`}>
            <Ic n={it.icon} />
            <span className="t">{it.label}</span>
            <span className="n">{it.n}</span>
            {top || it.kids ? <Ic n={it.open ? "down" : "right"} className="chev" /> : null}
          </div>
          {it.open && it.kids && (
            <div className="kids">
              <NavItems items={it.kids} />
            </div>
          )}
        </div>
      ))}
    </>
  );
}

const REV = "main@sha1:3fa9c21d7e";
const rows: {
  name: string;
  tone: Tone;
  label: string;
  msg: string;
  bad?: boolean;
  ago: string;
  sel?: boolean;
}[] = [
  { name: "apps", tone: "ok", label: "Ready", msg: `Applied revision: ${REV}`, ago: "2d", sel: true },
  { name: "cert-manager", tone: "ok", label: "Ready", msg: `Applied revision: ${REV}`, ago: "41d" },
  { name: "external-dns", tone: "ok", label: "Ready", msg: `Applied revision: ${REV}`, ago: "41d" },
  { name: "ingress-config", tone: "ok", label: "Ready", msg: `Applied revision: ${REV}`, ago: "12d" },
  {
    name: "payments-api",
    tone: "bad",
    label: "Failed",
    msg: "health check failed: Deployment/payments-api",
    bad: true,
    ago: "9m",
  },
  { name: "podinfo", tone: "run", label: "Reconciling", msg: "Reconciliation in progress", ago: "now" },
  { name: "batch-jobs", tone: "off", label: "Suspended", msg: "Suspended", ago: "3d" },
  { name: "observability", tone: "ok", label: "Ready", msg: `Applied revision: ${REV}`, ago: "2d" },
  { name: "sources", tone: "ok", label: "Ready", msg: `Applied revision: ${REV}`, ago: "41d" },
];

function Chip({ tone, label, n, cls }: { tone: Tone; label: string; n: number; cls?: string }) {
  return (
    <span className={`fc${cls ? ` ${cls}` : ""}`}>
      <Status tone={tone} />
      {label} <b>{n}</b>
    </span>
  );
}

function ListView() {
  return (
    <div className="shell" aria-hidden="true">
      <div className="side">
        <div className="logo">
          <LogoMark />
          eddy
        </div>
        <div className="sw-row">
          <div className="sw">ST</div>
          <div className="nm">
            <b>staging</b>
            <small>Staging · eu-west-1</small>
          </div>
          <Ic n="down" />
        </div>
        <div className="lbl">Browse</div>
        <div className="snav">
          <div className="it cur">
            <Ic n="list" />
            <span className="t">All resources</span>
            <span className="n">623</span>
          </div>
          <div className="it">
            <Ic n="warn" />
            <span className="t">Needs attention</span>
            <span className="n attn">2</span>
          </div>
          <NavItems items={nav} top />
        </div>
      </div>

      <div className="main">
        <div className="top">
          <div className="crumb">
            <span>staging ›</span> All resources
          </div>
          <span className="envtag">Staging</span>
          <div className="search">
            <Ic n="search" />
            Search staging <Kbd>⌘K</Kbd>
          </div>
        </div>
        <div className="cards">
          <div className="tile">
            <h5>Needs attention</h5>
            <p>2 objects need attention on staging.</p>
          </div>
          <div className="tile">
            <h5>
              <Ic n="git" />
              Git source
            </h5>
            <dl className="kv m-0">
              <dt>Repository</dt>
              <dd>acme/fleet-infra</dd>
              <dt>Revision</dt>
              <dd>main@3fa9c21</dd>
              <dt>Fetched</dt>
              <dd>5d ago, every 1m0s</dd>
              <dt>Applied by</dt>
              <dd>27 Kustomizations</dd>
            </dl>
          </div>
          <div className="tile ai">
            <h5>
              <Ic n="spark" />
              Ask AI about staging
            </h5>
            <p>Quick answers from what Eddy can see: statuses, events and versions.</p>
            <div className="chips">
              <span>What is failing on staging?</span>
            </div>
          </div>
        </div>
        <div className="filters">
          <div className="filter-in">
            <Ic n="list" />
            Filter this list <Kbd>/</Kbd>
          </div>
          <Chip tone="attn" label="Not ready" n={2} />
          <Chip tone="bad" label="Failed" n={1} />
          <Chip tone="run" label="Reconciling" n={1} cls="x" />
          <Chip tone="off" label="Suspended" n={1} cls="x" />
          <span className="count">643 resources</span>
          <span className="seg">
            <span className="on">Grouped</span>
            <span>Flat</span>
          </span>
        </div>
        <div className="table">
          <div className="row head">
            <span>Name</span>
            <span>Status</span>
            <span className="rd">Ready</span>
            <span className="ms">Message</span>
            <span className="ver">Version</span>
            <span className="ago">Age</span>
          </div>
          <div className="row grp">
            <Ic n="box" />
            Kustomizations <span>27</span>
          </div>
          {rows.map((r) => (
            <div key={r.name} className={`row${r.sel ? " sel" : ""}`}>
              <span className="nm">
                <i>flux-system / </i>
                <b>{r.name}</b>
              </span>
              <span className="stt">
                <Status tone={r.tone} />
                {r.label}
              </span>
              <span className="rd" />
              <span className={`ms${r.bad ? " bad" : ""}`}>{r.msg}</span>
              <span className="ver">main@3fa9c21</span>
              <span className="ago">{r.ago}</span>
            </div>
          ))}
        </div>
      </div>

      <div className="detail">
        <div className="seg">
          <span className="on">
            Details <Kbd>d</Kbd>
          </span>
          <span>
            Ask AI <Kbd>a</Kbd>
          </span>
        </div>
        <div className="kindline">
          <span className="kbadge">KS</span>
          Kustomization in flux-system
        </div>
        <p className="big">apps</p>
        <div className="okbox">
          <Status tone="ok" />
          <div className="min-w-0">
            <b className="font-semibold">Ready</b>
            <small>Applied revision:</small>
            <small>{REV}</small>
          </div>
        </div>
        <div className="acts">
          <span>
            <Ic n="arrow" />
            Open <Kbd>1</Kbd>
          </span>
          <span className="ask">
            <Ic n="spark" />
            Ask <Kbd>a</Kbd>
          </span>
        </div>
        <dl className="dkv">
          <dt>Source</dt>
          <dd className="lk">GitRepository/fleet-infra</dd>
          <dt>Revision</dt>
          <dd>main@3fa9c21</dd>
          <dt>Interval</dt>
          <dd>10m</dd>
          <dt>Inventory</dt>
          <dd>9 objects</dd>
          <dt>API version</dt>
          <dd>kustomize.toolkit.fluxcd.io/v1</dd>
          <dt>Last change</dt>
          <dd>2m ago</dd>
        </dl>
        <h6>Recent events</h6>
        <div className="ev">
          <b>
            ReconciliationSucceeded <span>2m</span>
          </b>
          <p>Reconciliation finished in 285ms, next run in 10m0s</p>
        </div>
        <div className="ev">
          <b>
            ReconciliationSucceeded <span>12m</span>
          </b>
          <p>Reconciliation finished in 266ms, next run in 10m0s</p>
        </div>
      </div>

      <div className="scrim" />
      <div className="pal">
        <div className="pal-in">
          <Ic n="search" />
          <span className="ph">
            <span className="ph-long">Search staging, or type : for commands</span>
            <span className="ph-short">Search…</span>
          </span>
          <span className="scope">
            <span className="on">
              <i /> staging
            </span>
            <span>
              <Ic n="globe" />
              <span className="lg">All clusters</span>
              <span className="sm">All</span>
            </span>
          </span>
        </div>
        <div className="pal-h">Needs attention, staging</div>
        <div className="pal-it sel">
          <span className="tile-i">
            <Status tone="bad" />
          </span>
          <span className="pal-t">
            <span className="mono">
              <em>JOB</em>nightly-report-29657955
            </span>
            <small>Job in reports · BackoffLimitExceeded: Job has reached the backoff limit</small>
          </span>
          <span className="ctag">
            <i /> staging
          </span>
        </div>
        <div className="pal-h">Clusters</div>
        <div className="pal-it">
          <span className="av pe">PE</span>
          <span className="pal-t">
            <span>Switch to prod-eu</span>
            <small>Production · eu-west-1</small>
          </span>
          <Kbd>2</Kbd>
        </div>
        <div className="pal-it">
          <span className="av dv">DV</span>
          <span className="pal-t">
            <span>Switch to dev</span>
            <small>Development · eu-west-1</small>
          </span>
          <Kbd>3</Kbd>
        </div>
        <div className="pal-it cur">
          <span className="av">ST</span>
          <span className="pal-t">
            <span>staging</span>
            <small>Staging · eu-west-1</small>
          </span>
          <span className="badge">current</span>
        </div>
        <div className="pal-cmd">
          <div className="pal-h">Commands</div>
          <div className="pal-it">
            <span className="tile-i">
              <Ic n="spark" />
            </span>
            <span className="pal-t">
              <span>Ask AI about nightly-report-29657955</span>
            </span>
            <Kbd>a</Kbd>
          </div>
          <div className="pal-it">
            <span className="tile-i">
              <Ic n="git" />
            </span>
            <span className="pal-t">
              <span>Jump to the owner of nightly-report-29657955</span>
              <small>CronJob/nightly-report</small>
            </span>
            <Kbd>u</Kbd>
          </div>
          <div className="pal-it">
            <span className="tile-i">
              <Ic n="chat" />
            </span>
            <span className="pal-t">
              <span>New thread about nightly-report-29657955</span>
            </span>
            <Kbd>c</Kbd>
          </div>
          <div className="pal-it">
            <span className="tile-i">
              <Ic n="warn" />
            </span>
            <span className="pal-t">
              <span>Show what needs attention</span>
              <small>staging</small>
            </span>
          </div>
        </div>
        <div className="pal-foot">
          <span>
            <Kbd>↑</Kbd> <Kbd>↓</Kbd> move
          </span>
          <span>
            <Kbd>↵</Kbd> open
          </span>
          <span>
            <Kbd>1-9</Kbd> switch
          </span>
          <span>
            <Kbd>Tab</Kbd> all clusters
          </span>
          <span>
            <Kbd>:</Kbd> commands only
          </span>
          <span>
            <Kbd>esc</Kbd> close
          </span>
        </div>
      </div>
    </div>
  );
}

type GNode = {
  id: string;
  x: number;
  y: number;
  kind: string;
  name: string;
  sub: string;
  age: string;
  tone?: "ok" | "run" | "wt";
  sel?: boolean;
};
const NW = 176;
const NH = 46;
const gnodes: GNode[] = [
  { id: "gr", x: 10, y: 127, kind: "GR", name: "flux-system", sub: "flux-system · main@3fa9c21", age: "1m" },
  { id: "cv", x: 218, y: 52, kind: "KS", name: "cluster-vars", sub: "flux-system · main@3fa9c21", age: "1m" },
  { id: "so", x: 218, y: 202, kind: "KS", name: "sources", sub: "flux-system · main@3fa9c21", age: "3m" },
  { id: "in", x: 426, y: 24, kind: "KS", name: "ingress", sub: "flux-system · main@3fa9c21", age: "1m" },
  {
    id: "ss",
    x: 426,
    y: 112,
    kind: "KS",
    name: "secrets-store",
    sub: "flux-system · main@3fa9c21",
    age: "5m",
  },
  {
    id: "cm",
    x: 426,
    y: 214,
    kind: "KS",
    name: "cert-manager",
    sub: "flux-system · main@3fa9c21",
    age: "7m",
    tone: "wt",
  },
  {
    id: "ap",
    x: 634,
    y: 127,
    kind: "KS",
    name: "apps",
    sub: "flux-system · main@3fa9c21",
    age: "22s",
    sel: true,
  },
  { id: "hr", x: 842, y: 52, kind: "HR", name: "podinfo", sub: "demo · 6.7.0", age: "66d", tone: "run" },
  { id: "ns", x: 842, y: 127, kind: "NS", name: "demo", sub: "", age: "68d" },
  { id: "es", x: 842, y: 202, kind: "ES", name: "podinfo-auth", sub: "demo", age: "59m" },
];
const gedges: { a: string; b: string; cls: "" | "src" | "app" | "wait" }[] = [
  { a: "gr", b: "cv", cls: "src" },
  { a: "gr", b: "so", cls: "src" },
  { a: "cv", b: "in", cls: "" },
  { a: "cv", b: "ss", cls: "" },
  { a: "so", b: "ss", cls: "" },
  { a: "so", b: "cm", cls: "" },
  { a: "in", b: "ap", cls: "" },
  { a: "ss", b: "ap", cls: "" },
  { a: "cm", b: "ap", cls: "wait" },
  { a: "ap", b: "hr", cls: "app" },
  { a: "ap", b: "ns", cls: "app" },
  { a: "ap", b: "es", cls: "app" },
];

/** Top-to-bottom layout for narrow figures: one row per dependency layer, centred. */
const VW = 104;
const VH = 40;
const VROW: Record<string, { x: number; row: number }> & Record<"gr", { x: number; row: number }> = {
  gr: { x: 123, row: 0 },
  cv: { x: 65, row: 1 },
  so: { x: 181, row: 1 },
  in: { x: 7, row: 2 },
  ss: { x: 123, row: 2 },
  cm: { x: 239, row: 2 },
  ap: { x: 123, row: 3 },
  hr: { x: 7, row: 4 },
  ns: { x: 123, row: 4 },
  es: { x: 239, row: 4 },
};
const vpos = (id: string) => {
  const p = VROW[id] ?? VROW.gr;
  return { x: p.x, y: 6 + p.row * 66 };
};

const edgeClass = (cls: string) => `gedge${cls ? ` ${cls}` : ""}`;

function GraphView() {
  const byId = new Map(gnodes.map((n) => [n.id, n]));
  return (
    <div aria-hidden="true">
      <div className="ghead">
        <b>Manages</b>
        <small>Upstream ← this object → downstream</small>
        <span className="seg">
          <span>Tree</span>
          <span className="on">Graph</span>
          <span>Outline</span>
        </span>
        <span className="cnt">10 nodes · 12 edges</span>
      </div>
      <div className="gscroll">
        <div className="gcanvas">
          <svg className="gh" viewBox="0 0 1030 330" role="presentation">
            {gedges.map((e) => {
              const a = byId.get(e.a);
              const b = byId.get(e.b);
              if (!a || !b) return null;
              const x1 = a.x + NW;
              const y1 = a.y + NH / 2;
              const x2 = b.x;
              const y2 = b.y + NH / 2;
              const m = (x1 + x2) / 2;
              return (
                <path
                  key={`${e.a}-${e.b}`}
                  className={edgeClass(e.cls)}
                  d={`M${x1} ${y1}C${m} ${y1} ${m} ${y2} ${x2} ${y2}`}
                />
              );
            })}
            {gnodes.map((n) => (
              <g key={n.id}>
                <rect
                  className={`gnode${n.sel ? " sel" : ""}`}
                  x={n.x}
                  y={n.y}
                  width={NW}
                  height={NH}
                  rx={9}
                />
                <circle className={n.tone ?? "ok"} cx={n.x + 14} cy={n.y + 16} r={6} />
                <path
                  d={`M${n.x + 11} ${n.y + 16.2}l2 2 4-4.2`}
                  fill="none"
                  stroke="var(--color-surface)"
                  strokeWidth={1.6}
                  strokeLinecap="round"
                />
                <rect className="kb" x={n.x + 27} y={n.y + 9} width={22} height={13} rx={4} />
                <text className="kb" x={n.x + 38} y={n.y + 18.5} textAnchor="middle">
                  {n.kind}
                </text>
                <text x={n.x + 54} y={n.y + 19}>
                  {n.name}
                </text>
                <text className="sub" x={n.x + NW - 8} y={n.y + 19} textAnchor="end">
                  {n.age}
                </text>
                {n.sub && (
                  <text className="sub" x={n.x + 27} y={n.y + 36}>
                    {n.sub}
                  </text>
                )}
              </g>
            ))}
          </svg>
          <svg className="gv" viewBox="0 0 350 330" role="presentation">
            {gedges.map((e) => {
              const a = vpos(e.a);
              const b = vpos(e.b);
              const x1 = a.x + VW / 2;
              const y1 = a.y + VH;
              const x2 = b.x + VW / 2;
              const y2 = b.y;
              const m = (y1 + y2) / 2;
              return (
                <path
                  key={`${e.a}-${e.b}`}
                  className={edgeClass(e.cls)}
                  d={`M${x1} ${y1}C${x1} ${m} ${x2} ${m} ${x2} ${y2}`}
                />
              );
            })}
            {gnodes.map((n) => {
              const { x, y } = vpos(n.id);
              return (
                <g key={n.id}>
                  <rect className={`gnode${n.sel ? " sel" : ""}`} x={x} y={y} width={VW} height={VH} rx={9} />
                  <circle className={n.tone ?? "ok"} cx={x + 13} cy={y + 13} r={5.5} />
                  <path
                    d={`M${x + 10.2} ${y + 13.2}l1.9 1.9 3.7-3.9`}
                    fill="none"
                    stroke="var(--color-surface)"
                    strokeWidth={1.5}
                    strokeLinecap="round"
                  />
                  <rect className="kb" x={x + 24} y={y + 7} width={22} height={13} rx={4} />
                  <text className="kb" x={x + 35} y={y + 16.5} textAnchor="middle">
                    {n.kind}
                  </text>
                  <text className="sub" x={x + VW - 8} y={y + 17} textAnchor="end">
                    {n.age}
                  </text>
                  <text x={x + 9} y={y + 33}>
                    {n.name}
                  </text>
                </g>
              );
            })}
          </svg>
          <div className="glegend">
            <span>
              <i /> first → then
            </span>
            <span>
              <i className="src" /> source
            </span>
            <span>
              <i className="app" /> applies
            </span>
            <span>
              <i className="wait" /> waiting
            </span>
          </div>
          <div className="gzoom">
            <span>
              <Ic n="plus" />
            </span>
            <span>
              <Ic n="minus" />
            </span>
            <span>
              <Ic n="fit" />
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}

export function HeroMock() {
  const [view, setView] = useState<"list" | "graph">("list");
  return (
    <figure className="mx-auto mt-10 mb-0 max-w-[1080px] sm:mt-14">
      <fieldset className="mock-toggle mb-4 min-w-0" aria-label="Illustration view">
        <button type="button" aria-pressed={view === "list"} onClick={() => setView("list")}>
          List
        </button>
        <button type="button" aria-pressed={view === "graph"} onClick={() => setView("graph")}>
          Dependency graph
        </button>
      </fieldset>
      <div
        className="win"
        role="img"
        aria-label="Illustration of the Eddy UI: the staging cluster's resource list with a detail panel and the command palette open on the needs-attention list and the cluster switcher, plus a live dependency graph view."
      >
        <div className="ribbon" />
        {view === "list" ? <ListView /> : <GraphView />}
      </div>
      <figcaption className="fine mt-4 text-center">
        An illustration built in HTML and CSS from the app's own design tokens. The colours mark the
        environment, and protected clusters carry a solid badge.
      </figcaption>
      <EnvLegend className="mt-2" />
    </figure>
  );
}
