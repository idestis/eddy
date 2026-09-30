import { useEffect, useRef, useState } from "react";
import { logsUrl } from "../api/endpoints";
import type { Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { hint, useKeys } from "../lib/keys";
import { Icon } from "./Icon";
import { Keys } from "./Status";

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

  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` restarts the stream on retry
  useEffect(() => {
    let next = 0;
    setLines([]);
    setStatus("connecting");
    const url = `${logsUrl(cluster, pod.namespace, pod.name)}?tail=${TAIL}&follow=true`;
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
  }, [cluster, pod.namespace, pod.name, attempt]);

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

  const container = pod.labels?.["app.kubernetes.io/name"];
  const statusText = {
    connecting: "connecting…",
    streaming: "streaming",
    ended: "stream ended",
    error: "disconnected",
  }[status];

  return (
    <section className="logs" aria-label={`Logs of ${pod.name}`}>
      <div className="lgh">
        <div className="ttl">
          <b>{pod.name}</b>
          <span>
            {container ? `${container} in ` : ""}
            {pod.namespace} on {cluster}, {statusText}
          </span>
        </div>
        {status === "error" && (
          <button type="button" onClick={() => setAttempt((a) => a + 1)}>
            <Icon name="sync" />
            Retry
          </button>
        )}
        <button
          type="button"
          onClick={() => ask("Summarize these logs. Any errors or warnings I should care about?")}
        >
          <Icon name="spark" />
          Explain logs
        </button>
        <button type="button" aria-pressed={follow} onClick={() => setFollow(!follow)}>
          <span className="dot" />
          Follow <Keys keys={hint("follow")} />
        </button>
      </div>
      <div
        ref={body}
        className="lgb"
        role="log"
        aria-live="off"
        tabIndex={-1}
        onScroll={(e) => {
          const el = e.currentTarget;
          if (follow && el.scrollHeight - el.scrollTop - el.clientHeight > 24) setFollow(false);
        }}
      >
        {lines.length === 0 && status !== "error" && <div className="lg meta">Waiting for log lines…</div>}
        {status === "error" && lines.length === 0 && (
          <div className="lg meta">The log stream is not available.</div>
        )}
        {lines.map((l) => (
          <div key={l.id} className={`lg ${l.level}`}>
            {l.text}
          </div>
        ))}
      </div>
    </section>
  );
}
