// The Logs tab of a Deployment, StatefulSet, DaemonSet or Job: every pod's logs in one
// stream (GET …/workloads/{kind}/{ns}/{name}/logs, docs/api.md "Workload logs"), with a
// coloured prefix per pod, pod, container and level filters, search, pause and follow.
// The buffer is capped (lib/workloadLogs.ts) and rendered by <LogLines>.

import { type CSSProperties, useEffect, useMemo, useRef, useState } from "react";
import { workloadLogsUrl } from "../api/endpoints";
import { useMe } from "../api/queries";
import type { Resource, WorkloadLogEntry, WorkloadPod, WorkloadPodsEvent } from "../api/types";
import { useAppState } from "../lib/appState";
import { useKeys } from "../lib/keys";
import { buildAttachment } from "../lib/logAttachments";
import { type LogFormat, setViewPrefs, useViewPrefs } from "../lib/viewPrefs";
import {
  appendEntries,
  containersOf,
  filterLines,
  LOG_CAP,
  type LogLevel,
  type LogLine,
  levelCounts,
  podHue,
  podLabel,
  toText,
  truncationNotice,
} from "../lib/workloadLogs";
import { Icon } from "./Icon";
import { type AskMode, FormatToggle, LevelChips, LogLines } from "./LogLines";
import { OverflowChips, type OverflowItem } from "./OverflowChips";
import { Select } from "./Select";
import { KeyHint } from "./Status";
import { useToast } from "./Toasts";

const FLUSH_MS = 100;
const TAILS = [100, 500, 1000] as const;
const SINCE: ReadonlyArray<[string, string]> = [
  ["", "Any time"],
  ["300", "Last 5 min"],
  ["3600", "Last hour"],
  ["21600", "Last 6 h"],
  ["86400", "Last 24 h"],
];

type StreamStatus = "connecting" | "streaming" | "ended" | "error";

const ALL_PODS = "\u0000all";

export const HEAD_BTN =
  "flex h-[32px] items-center gap-[7px] rounded-[9px] border border-white/14 px-[10px] text-12-5 transition-colors duration-(--duration-fast) hover:bg-white/6 disabled:opacity-40 aria-pressed:bg-white/10 [&_kbd]:border-white/20 [&_kbd]:bg-transparent [&_kbd]:text-code-dim";

export function WorkloadLogsView({ cluster, workload }: { cluster: string; workload: Resource }) {
  const { askWithLogs } = useAppState();
  const { data: me } = useMe();
  const toast = useToast();
  const saved = useViewPrefs();
  const [lines, setLines] = useState<LogLine[]>([]);
  const [pods, setPods] = useState<WorkloadPod[]>([]);
  const [podTotals, setPodTotals] = useState({ total: 0, limit: 0 });
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const [attempt, setAttempt] = useState(0);
  const [tail, setTail] = useState<number>(saved.logTail ?? 100);
  const [since, setSince] = useState(saved.logSince ?? "");
  const [picked, setPicked] = useState<ReadonlySet<string> | undefined>();
  const [container, setContainer] = useState("");
  const [levels, setLevels] = useState<ReadonlySet<LogLevel> | undefined>();
  const [query, setQuery] = useState("");
  const [follow, setFollowState] = useState(saved.logFollow ?? true);
  const [paused, setPaused] = useState(false);
  const [held, setHeld] = useState(0);
  const [unseen, setUnseen] = useState(0);
  const [trimmed, setTrimmed] = useState(0);
  const format: LogFormat = saved.logFormat ?? "structured";
  const queue = useRef<WorkloadLogEntry[]>([]);
  const nextId = useRef(0);
  const pausedRef = useRef(paused);
  pausedRef.current = paused;
  const followRef = useRef(follow);
  followRef.current = follow;

  // One stream per workload and tail/since choice. Pod, container and level filters apply
  // to the buffer, so switching them is instant and keeps what was already received.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` restarts the stream on retry
  useEffect(() => {
    setLines([]);
    setPods([]);
    setTrimmed(0);
    setHeld(0);
    setUnseen(0);
    setStatus("connecting");
    queue.current = [];
    const q = new URLSearchParams({ tail: String(tail), follow: "true" });
    if (since) q.set("since", since);
    const source = new EventSource(`${workloadLogsUrl(cluster, workload)}?${q}`);
    const parse = <T,>(ev: Event): T | undefined => {
      try {
        return JSON.parse((ev as MessageEvent<string>).data) as T;
      } catch {
        return undefined;
      }
    };
    source.addEventListener("open", () => setStatus("streaming"));
    source.addEventListener("pods", (ev) => {
      const data = parse<WorkloadPodsEvent>(ev);
      if (!data) return;
      setPods(data.pods);
      setPodTotals({ total: data.total, limit: data.limit });
    });
    source.addEventListener("log", (ev) => {
      const data = parse<{ entries?: WorkloadLogEntry[] }>(ev);
      if (data?.entries?.length) queue.current.push(...data.entries);
    });
    source.addEventListener("end", (ev) => {
      const data = parse<{ error?: { message: string } }>(ev);
      source.close();
      queue.current.push({
        pod: "",
        line: data?.error ? `Stream ended: ${data.error.message}` : "End of stream",
        marker: data?.error ? "error" : "ended",
      });
      setStatus("ended");
    });
    source.addEventListener("error", () => {
      source.close();
      setStatus((s) => (s === "ended" ? s : "error"));
    });
    // Batch renders: log bursts arrive many times a second. Paused lines wait, capped.
    const timer = setInterval(() => {
      if (pausedRef.current) {
        if (queue.current.length > LOG_CAP) queue.current = queue.current.slice(-LOG_CAP);
        setHeld(queue.current.length);
        return;
      }
      const batch = queue.current;
      if (batch.length === 0) return;
      queue.current = [];
      setHeld(0);
      setLines((prev) => {
        const res = appendEntries(prev, batch, nextId.current);
        nextId.current = res.nextId;
        if (res.trimmed) setTrimmed((t) => t + res.trimmed);
        return res.lines;
      });
      if (!followRef.current) setUnseen((n) => n + batch.length);
    }, FLUSH_MS);
    return () => {
      clearInterval(timer);
      source.close();
    };
  }, [cluster, workload.kind, workload.namespace, workload.name, tail, since, attempt]);

  const shown = useMemo(
    () => filterLines(lines, { pods: picked, container, query, levels }),
    [lines, picked, container, query, levels],
  );
  const counts = useMemo(() => levelCounts(lines), [lines]);
  const containers = useMemo(() => containersOf(pods), [pods]);
  const notice = truncationNotice(podTotals.total, podTotals.limit);
  const aiLogs = Boolean(me?.features.ai && me.features.aiLogs);
  const source = `${workload.namespace}/${workload.name} · ${pods.length} pod${pods.length === 1 ? "" : "s"}`;

  const setFollow = (on: boolean) => setFollowState(on);
  const resume = () => {
    setUnseen(0);
    setFollowState(true);
  };
  // An explicit toggle (button or f) is remembered as the default; scrolling is not.
  const toggleFollow = () => {
    const next = !follow;
    if (next) resume();
    else setFollowState(false);
    setViewPrefs({ logFollow: next });
  };

  const togglePod = (name: string) =>
    setPicked((cur) => {
      const next = new Set(cur ?? pods.map((p) => p.name));
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next.size === pods.length ? undefined : next;
    });

  const podItems: OverflowItem[] = [
    {
      key: ALL_PODS,
      text: "All pods",
      active: picked === undefined,
      option: "All pods",
      chip: (
        <button
          type="button"
          className={`${HEAD_BTN} h-7!`}
          aria-pressed={picked === undefined}
          onClick={() => setPicked(undefined)}
        >
          All pods
        </button>
      ),
    },
    ...pods.map((p) => {
      const label = podLabel(p.name, workload.name);
      return {
        key: p.name,
        text: label,
        active: picked?.has(p.name) ?? false,
        option: (
          <span
            className="inline-flex items-center gap-1.5 font-mono"
            style={{ "--pod-h": podHue(p.name) } as CSSProperties}
          >
            <span className="lg-pod-dot size-2 rounded-full" />
            {label}
          </span>
        ),
        chip: (
          <button
            type="button"
            className={`${HEAD_BTN} h-7! font-mono text-11-5! ${picked && !picked.has(p.name) ? "opacity-45" : ""}`}
            aria-pressed={picked ? picked.has(p.name) : true}
            title={`${p.name} · ${p.status}`}
            onClick={() => togglePod(p.name)}
            style={{ "--pod-h": podHue(p.name) } as CSSProperties}
          >
            <span className="lg-pod-dot size-2 rounded-full" />
            {label}
          </button>
        ),
      };
    }),
  ];

  const copy = () =>
    navigator.clipboard.writeText(toText(shown)).then(
      () => toast(`Copied ${shown.length} lines`, "ok"),
      () => toast("Couldn't copy. Select the text and copy it by hand.", "bad"),
    );

  const ask = (picked: readonly LogLine[], mode: AskMode | "all") => {
    const { attachment, total } = buildAttachment(picked, source);
    if (!attachment.lines.length) {
      toast("There are no log lines to attach yet.");
      return;
    }
    askWithLogs({
      attachment,
      total,
      question:
        mode === "all"
          ? "Summarize these logs. Any errors or warnings I should care about?"
          : "What's wrong in these log lines?",
      link: { cluster, ref: workload },
    });
  };

  useKeys({ follow: toggleFollow });

  const statusText = {
    connecting: "connecting…",
    streaming: paused ? "paused" : "streaming",
    ended: "stream ended",
    error: "disconnected",
  }[status];

  return (
    <section
      className="relative flex h-[max(420px,calc(100vh-400px))] flex-1 flex-col overflow-hidden rounded-[14px] bg-code-bg text-code-ink"
      aria-label={`Logs of ${workload.kind} ${workload.name}`}
    >
      <div className="flex flex-wrap items-center gap-2.5 border-b border-code-line px-3.5 py-2.5">
        <div className="flex min-w-0 flex-1 flex-col">
          <b className="truncate font-mono text-13-5 font-semibold">{workload.name}</b>
          <span className="text-12 text-code-dim">
            {pods.length} pod{pods.length === 1 ? "" : "s"} in {workload.namespace} on {cluster}, {statusText}
            {held > 0 && ` · ${held} new lines held`}
          </span>
        </div>
        {status === "error" && (
          <button type="button" className={HEAD_BTN} onClick={() => setAttempt((a) => a + 1)}>
            <Icon name="sync" />
            Retry
          </button>
        )}
        <FormatToggle value={format} onChange={(f) => setViewPrefs({ logFormat: f })} />
        <button type="button" className={HEAD_BTN} aria-pressed={paused} onClick={() => setPaused((p) => !p)}>
          <Icon name={paused ? "play" : "pause"} />
          {paused ? "Resume" : "Pause"}
        </button>
        <button type="button" className={HEAD_BTN} onClick={copy} disabled={shown.length === 0}>
          <Icon name="copy" />
          Copy
        </button>
        {aiLogs && (
          <button
            type="button"
            className={HEAD_BTN}
            disabled={shown.length === 0}
            title="Ask AI about the lines shown (the newest 500)"
            onClick={() => ask(shown, "all")}
          >
            <Icon name="spark" />
            Ask AI
          </button>
        )}
        <button type="button" className={HEAD_BTN} aria-pressed={follow} onClick={toggleFollow}>
          <span
            className={`size-2 rounded-full transition-colors duration-(--duration-base) ${follow ? "bg-code-ok shadow-[0_0_0_3px_rgb(74_222_128/0.2)]" : "bg-code-dim"}`}
          />
          Follow <KeyHint id="follow" />
        </button>
      </div>
      <div className="flex flex-wrap items-center gap-2 border-b border-code-line px-3.5 py-2">
        <OverflowChips
          label="Pods"
          tone="code"
          className="flex-[1_1_0]"
          chipClass={`${HEAD_BTN} h-7! aria-pressed:bg-white/10`}
          gap={6}
          items={podItems}
          onToggle={(k) => (k === ALL_PODS ? setPicked(undefined) : togglePod(k))}
          summary={() => (picked ? `${picked.size} of ${pods.length} pods` : "All pods")}
        />
        <LevelChips
          counts={counts}
          picked={levels}
          onChange={setLevels}
          className="max-w-[18rem] min-w-0 flex-[1_1_0] justify-end"
        />
      </div>
      <div className="flex flex-wrap items-center gap-2 border-b border-code-line px-3.5 py-2">
        {containers.length > 1 && (
          <Select
            tone="code"
            label="Container"
            value={container}
            onChange={setContainer}
            options={[
              { value: "", label: "All containers" },
              ...containers.map((c) => ({
                value: c,
                label: c,
                meta: `${pods.filter((p) => p.containers.includes(c)).length} pods`,
              })),
            ]}
          />
        )}
        <Select
          tone="code"
          label="Lines per pod"
          value={tail}
          onChange={(t) => {
            setTail(t);
            setViewPrefs({ logTail: t });
          }}
          options={TAILS.map((t) => ({ value: t, label: `Last ${t} lines` }))}
        />
        <Select
          tone="code"
          label="Since"
          value={since}
          onChange={(v) => {
            setSince(v);
            setViewPrefs({ logSince: v });
          }}
          options={SINCE.map(([value, label]) => ({ value, label }))}
        />
        <label className="ml-auto flex h-[32px] min-w-40 flex-[0_1_260px] items-center gap-1.5 rounded-[9px] border border-white/14 px-2 text-code-dim focus-within:border-c">
          <Icon name="search" className="size-3.5" />
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.stopPropagation();
                setQuery("");
                e.currentTarget.blur();
              }
            }}
            placeholder="Find in logs"
            aria-label="Find in logs"
            spellCheck={false}
            className="min-w-0 flex-1 border-0 bg-transparent font-mono text-12 text-code-ink outline-none placeholder:text-code-dim"
          />
          {query && <span className="text-11 tabular-nums">{shown.length}</span>}
        </label>
      </div>
      {(notice || trimmed > 0) && (
        <div
          className="flex flex-col gap-0.5 border-b border-code-line px-3.5 py-1.5 text-12 text-code-warn"
          role="status"
        >
          {notice && <span>{notice}</span>}
          {trimmed > 0 && (
            <span>
              Keeping the newest {LOG_CAP.toLocaleString()} lines; {trimmed.toLocaleString()} older lines were
              dropped.
            </span>
          )}
        </div>
      )}
      <LogLines
        lines={shown}
        format={format}
        query={query}
        workload={workload.name}
        showContainer={containers.length > 1}
        follow={follow}
        setFollow={setFollow}
        unseen={unseen}
        onResume={resume}
        onAsk={aiLogs ? (sel, mode) => ask(sel, mode) : undefined}
        emptyText={
          status === "error"
            ? "The log stream is not available."
            : lines.length
              ? "No lines match the filters."
              : "Waiting for log lines…"
        }
      />
    </section>
  );
}
