import { useEffect, useMemo, useRef, useState } from "react";
import { logsUrl } from "../api/endpoints";
import { useMe } from "../api/queries";
import type { Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { useKeys } from "../lib/keys";
import { buildAttachment } from "../lib/logAttachments";
import { type LogFormat, setViewPrefs, useViewPrefs } from "../lib/viewPrefs";
import { filterLines, type LogLevel, type LogLine, levelCounts, toLine } from "../lib/workloadLogs";
import { Icon } from "./Icon";
import { type AskMode, FormatToggle, LevelChips, LogLines } from "./LogLines";
import { Select } from "./Select";
import { KeyHint } from "./Status";
import { useToast } from "./Toasts";
import { HEAD_BTN } from "./WorkloadLogsView";

const MAX_LINES = 5_000;
const TAIL = 500;

type StreamStatus = "connecting" | "streaming" | "ended" | "error";

/** Streams GET …/pods/{ns}/{name}/logs over SSE. Follow keeps the view pinned to the newest line. */
export function LogsView({ cluster, pod }: { cluster: string; pod: Resource }) {
  const saved = useViewPrefs();
  const { data: me } = useMe();
  const toast = useToast();
  const [lines, setLines] = useState<LogLine[]>([]);
  const [follow, setFollow] = useState(saved.logFollow ?? true);
  const [unseen, setUnseen] = useState(0);
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const [attempt, setAttempt] = useState(0);
  const [levels, setLevels] = useState<ReadonlySet<LogLevel> | undefined>();
  const { askWithLogs } = useAppState();
  const containers = pod.containers ?? [];
  const [container, setContainer] = useState<string | undefined>(containers[0]);
  // Keep the choice valid when the pod summary changes (for example after a restart).
  const current = container && containers.includes(container) ? container : containers[0];
  const format: LogFormat = saved.logFormat ?? "structured";
  const followRef = useRef(follow);
  followRef.current = follow;

  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` restarts the stream on retry
  useEffect(() => {
    let next = 0;
    setLines([]);
    setUnseen(0);
    setStatus("connecting");
    const q = new URLSearchParams({ tail: String(TAIL), follow: "true" });
    if (current) q.set("container", current);
    const url = `${logsUrl(cluster, pod.namespace, pod.name)}?${q}`;
    const source = new EventSource(url);
    const append = (added: LogLine[]) => {
      setLines((prev) => {
        const all = prev.concat(added);
        return all.length > MAX_LINES ? all.slice(all.length - MAX_LINES) : all;
      });
      if (!followRef.current) setUnseen((n) => n + added.length);
    };
    source.addEventListener("open", () => setStatus("streaming"));
    source.addEventListener("log", (ev) => {
      try {
        const data = JSON.parse((ev as MessageEvent<string>).data) as { lines?: string[] };
        if (data.lines?.length)
          append(data.lines.map((line) => toLine({ pod: pod.name, container: current, line }, next++)));
      } catch {
        // Ignore a malformed frame; the next one will do.
      }
    });
    source.addEventListener("end", () => {
      source.close();
      setStatus("ended");
      append([toLine({ pod: "", line: "End of stream", marker: "ended" }, next++)]);
    });
    source.addEventListener("error", () => {
      source.close();
      setStatus((s) => (s === "ended" ? s : "error"));
    });
    return () => source.close();
  }, [cluster, pod.namespace, pod.name, current, attempt]);

  const shown = useMemo(() => filterLines(lines, { levels }), [lines, levels]);
  const counts = useMemo(() => levelCounts(lines), [lines]);
  const aiLogs = Boolean(me?.features.ai && me.features.aiLogs);

  const resume = () => {
    setUnseen(0);
    setFollow(true);
  };
  const toggleFollow = () => {
    const on = !follow;
    if (on) resume();
    else setFollow(false);
    setViewPrefs({ logFollow: on });
  };

  const ask = (picked: readonly LogLine[], mode: AskMode | "all") => {
    const { attachment, total } = buildAttachment(picked, `${pod.namespace}/${pod.name}/${current ?? ""}`);
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
      link: { cluster, ref: pod },
    });
  };

  useKeys({ follow: toggleFollow });

  const statusText = {
    connecting: "connecting…",
    streaming: "streaming",
    ended: "stream ended",
    error: "disconnected",
  }[status];

  return (
    <section
      className="relative flex h-[max(360px,calc(100vh-400px))] flex-1 flex-col overflow-hidden rounded-[14px] bg-code-bg text-code-ink"
      aria-label={`Logs of ${pod.name}`}
    >
      <div className="flex flex-wrap items-center gap-2.5 border-b border-code-line px-3.5 py-2.5">
        <div className="flex min-w-0 flex-1 flex-col">
          <b className="truncate font-mono text-13-5 font-semibold">{pod.name}</b>
          <span className="text-12 text-code-dim">
            {current ? `${current} in ` : ""}
            {pod.namespace} on {cluster}, {statusText}
          </span>
        </div>
        {containers.length > 1 && (
          <div className="flex items-center gap-2 text-12 text-code-dim">
            Container
            <Select
              tone="code"
              label="Container"
              value={current ?? ""}
              onChange={setContainer}
              options={containers.map((c, i) => ({ value: c, label: c, meta: i === 0 ? "main" : undefined }))}
            />
          </div>
        )}
        {status === "error" && (
          <button type="button" className={HEAD_BTN} onClick={() => setAttempt((a) => a + 1)}>
            <Icon name="sync" />
            Retry
          </button>
        )}
        <FormatToggle value={format} onChange={(f) => setViewPrefs({ logFormat: f })} />
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
        <LevelChips counts={counts} picked={levels} onChange={setLevels} />
      </div>
      <LogLines
        lines={shown}
        format={format}
        query=""
        showContainer={false}
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
