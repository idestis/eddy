import { useEffect, useRef, useState } from "react";
import { logsUrl } from "../api/endpoints";
import type { Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { useKeys } from "../lib/keys";
import { Icon } from "./Icon";
import { KeyHint } from "./Status";

const MAX_LINES = 5_000;
const TAIL = 500;

type Level = "error" | "warn" | "info" | "meta";

interface Line {
  id: number;
  text: string;
  level: Level;
}

function levelOf(text: string): Level {
  if (/\b(error|err|fatal|panic|failed)\b/i.test(text)) return "error";
  if (/\bwarn(ing)?\b/i.test(text)) return "warn";
  return "info";
}

type StreamStatus = "connecting" | "streaming" | "ended" | "error";

/** Streams GET …/pods/{ns}/{name}/logs over SSE. Follow keeps the view pinned to the newest line. */
export function LogsView({ cluster, pod }: { cluster: string; pod: Resource }) {
  const [lines, setLines] = useState<Line[]>([]);
  const [follow, setFollow] = useState(true);
  const [status, setStatus] = useState<StreamStatus>("connecting");
  const [attempt, setAttempt] = useState(0);
  const body = useRef<HTMLDivElement>(null);
  const { ask } = useAppState();
  const containers = pod.containers ?? [];
  const [container, setContainer] = useState<string | undefined>(containers[0]);
  // Keep the choice valid when the pod summary changes (for example after a restart).
  const current = container && containers.includes(container) ? container : containers[0];

  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` restarts the stream on retry
  useEffect(() => {
    let next = 0;
    setLines([]);
    setStatus("connecting");
    const q = new URLSearchParams({ tail: String(TAIL), follow: "true" });
    if (current) q.set("container", current);
    const url = `${logsUrl(cluster, pod.namespace, pod.name)}?${q}`;
    const source = new EventSource(url);
    const append = (texts: string[], level?: Level) =>
      setLines((prev) => {
        const added = texts.map((text) => ({ id: next++, text, level: level ?? levelOf(text) }));
        const all = prev.concat(added);
        return all.length > MAX_LINES ? all.slice(all.length - MAX_LINES) : all;
      });
    source.addEventListener("open", () => setStatus("streaming"));
    source.addEventListener("log", (ev) => {
      try {
        const data = JSON.parse((ev as MessageEvent<string>).data) as { lines?: string[] };
        if (data.lines?.length) append(data.lines);
      } catch {
        // Ignore a malformed frame; the next one will do.
      }
    });
    source.addEventListener("end", () => {
      source.close();
      setStatus("ended");
      append(["— end of stream —"], "meta");
    });
    source.addEventListener("error", () => {
      source.close();
      setStatus((s) => (s === "ended" ? s : "error"));
    });
    return () => source.close();
  }, [cluster, pod.namespace, pod.name, current, attempt]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: re-pin to the bottom whenever lines arrive
  useEffect(() => {
    if (follow && body.current) body.current.scrollTop = body.current.scrollHeight;
  }, [lines, follow]);

  const scrollBy = (dy: number) => body.current?.scrollBy({ top: dy });
  useKeys({
    follow: () => setFollow((f) => !f),
    down: () => scrollBy(60),
    up: () => {
      setFollow(false);
      scrollBy(-60);
    },
    top: () => {
      setFollow(false);
      body.current?.scrollTo({ top: 0 });
    },
    bottom: () => setFollow(true),
  });

  const statusText = {
    connecting: "connecting…",
    streaming: "streaming",
    ended: "stream ended",
    error: "disconnected",
  }[status];

  const headBtn =
    "flex h-[34px] items-center gap-[7px] rounded-[9px] border border-white/14 px-[11px] text-13 aria-pressed:bg-white/10 [&_kbd]:border-white/20 [&_kbd]:bg-transparent [&_kbd]:text-code-dim";

  return (
    <section
      className="flex h-[max(360px,calc(100vh-400px))] flex-1 flex-col overflow-hidden rounded-[14px] bg-code-bg text-code-ink"
      aria-label={`Logs of ${pod.name}`}
    >
      <div className="flex flex-wrap items-center gap-3 border-b border-code-line px-3.5 py-2.5">
        <div className="flex min-w-0 flex-1 flex-col">
          <b className="truncate font-mono text-13-5 font-semibold">{pod.name}</b>
          <span className="text-12 text-code-dim">
            {current ? `${current} in ` : ""}
            {pod.namespace} on {cluster}, {statusText}
          </span>
        </div>
        {containers.length > 1 && (
          <label className="flex items-center gap-2 text-12 text-code-dim">
            Container
            <select
              value={current}
              onChange={(e) => setContainer(e.target.value)}
              className="h-[34px] rounded-[9px] border border-white/14 bg-code-bg px-2.5 font-mono text-12-5 text-code-ink outline-none focus:border-c"
              aria-label="Container"
            >
              {containers.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
          </label>
        )}
        {status === "error" && (
          <button type="button" className={headBtn} onClick={() => setAttempt((a) => a + 1)}>
            <Icon name="sync" />
            Retry
          </button>
        )}
        <button
          type="button"
          className={headBtn}
          onClick={() => ask("Summarize these logs. Any errors or warnings I should care about?")}
        >
          <Icon name="spark" />
          Explain logs
        </button>
        <button type="button" className={headBtn} aria-pressed={follow} onClick={() => setFollow(!follow)}>
          <span
            className={`size-2 rounded-full ${follow ? "bg-code-ok shadow-[0_0_0_3px_rgb(74_222_128/0.2)]" : "bg-code-dim"}`}
          />
          Follow <KeyHint id="follow" />
        </button>
      </div>
      <div
        ref={body}
        className="min-h-0 flex-1 overflow-auto pt-2 pb-6 font-mono text-12-5 leading-[1.6]"
        role="log"
        aria-live="off"
        tabIndex={-1}
        onScroll={(e) => {
          const el = e.currentTarget;
          if (follow && el.scrollHeight - el.scrollTop - el.clientHeight > 24) setFollow(false);
        }}
      >
        {lines.length === 0 && status !== "error" && (
          <div className={`${LINE} lg-meta`}>Waiting for log lines…</div>
        )}
        {status === "error" && lines.length === 0 && (
          <div className={`${LINE} lg-meta`}>The log stream is not available.</div>
        )}
        {lines.map((l) => (
          <div key={l.id} className={`${LINE} lg-${l.level}`}>
            {l.text}
          </div>
        ))}
      </div>
    </section>
  );
}

const LINE = "px-4 py-px break-words whitespace-pre-wrap hover:bg-white/4";
