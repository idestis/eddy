import { createContext, type ReactNode, useCallback, useContext, useEffect, useRef, useState } from "react";
import { isApiError } from "../api/client";
import { askAI } from "../api/endpoints";
import { useMe } from "../api/queries";
import type { AskStep, ClusterInfo, Message, Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { bytes } from "../lib/format";
import { kindInfo } from "../lib/kinds";
import { Icon } from "./Icon";
import { Markdown } from "./Markdown";
import { StatusIcon } from "./Status";

type Turn =
  | { kind: "user"; text: string }
  | { kind: "ai"; message: Message; steps: AskStep[] }
  | { kind: "error"; text: string };

interface Conversation {
  threadId?: string;
  turns: Turn[];
  busy: boolean;
}

interface AskAIStore {
  get: (key: string) => Conversation | undefined;
  send: (key: string, req: { cluster: string; resourceId?: string; question: string }) => Promise<void>;
  reset: (key: string) => void;
}

const AskAIContext = createContext<AskAIStore | null>(null);

function errorText(err: unknown): string {
  if (isApiError(err, "disabled")) return "Ask AI is turned off on this hub.";
  if (isApiError(err, "rate_limited")) return "Too many questions at once. Wait a moment, then ask again.";
  if (isApiError(err, "disconnected")) return "The cluster is disconnected, so there is nothing to read.";
  if (isApiError(err)) return `Couldn't get an answer: ${err.message}`;
  return "Couldn't get an answer. Try again.";
}

/** Keeps Ask AI conversations per context (cluster, or cluster + resource) for the session. */
export function AskAIProvider({ children }: { children: ReactNode }) {
  const [conversations, setConversations] = useState<ReadonlyMap<string, Conversation>>(new Map());
  const ref = useRef(conversations);
  ref.current = conversations;

  const patch = useCallback((key: string, fn: (c: Conversation) => Conversation) => {
    setConversations((prev) => {
      const next = new Map(prev);
      next.set(key, fn(prev.get(key) ?? { turns: [], busy: false }));
      return next;
    });
  }, []);

  const send = useCallback<AskAIStore["send"]>(
    async (key, req) => {
      const current = ref.current.get(key);
      if (current?.busy) return;
      patch(key, (c) => ({ ...c, busy: true, turns: [...c.turns, { kind: "user", text: req.question }] }));
      try {
        const res = await askAI({ ...req, threadId: current?.threadId });
        patch(key, (c) => ({
          threadId: res.threadId,
          busy: false,
          turns: [...c.turns, { kind: "ai", message: res.message, steps: res.steps }],
        }));
      } catch (err) {
        patch(key, (c) => ({
          ...c,
          busy: false,
          turns: [...c.turns, { kind: "error", text: errorText(err) }],
        }));
      }
    },
    [patch],
  );

  const reset = useCallback((key: string) => {
    setConversations((prev) => {
      const next = new Map(prev);
      next.delete(key);
      return next;
    });
  }, []);

  const get = useCallback((key: string) => conversations.get(key), [conversations]);

  return <AskAIContext.Provider value={{ get, send, reset }}>{children}</AskAIContext.Provider>;
}

function useAskAI(): AskAIStore {
  const s = useContext(AskAIContext);
  if (!s) throw new Error("useAskAI must be used inside <AskAIProvider>");
  return s;
}

/** Suggested first questions, adapted to what the user is looking at. */
export function suggestions(cluster: ClusterInfo, r: Resource | undefined): string[] {
  if (!r) {
    const failing = cluster.counts?.failed ?? 0;
    return failing
      ? [`What's failing on ${cluster.name} and why?`, "Which chart versions are deployed here?"]
      : [`Is anything unhealthy on ${cluster.name}?`, "Summarize this cluster in 3 bullets"];
  }
  if (r.kind === "Pod")
    return r.status === "ready"
      ? ["Summarize the recent logs", "Which image is this?"]
      : ["Why is this pod not running?", "Summarize the recent logs"];
  if (r.status === "failed")
    return [`Why is ${r.name} failing?`, "How do I fix it?", "What was the last working version?"];
  if (r.status === "suspended")
    return [`Why is ${r.name} suspended and who did it?`, "Is it safe to resume?"];
  if (r.kind === "HelmRelease") return ["Which version is running?", "What does this release manage?"];
  if (r.kind === "Kustomization")
    return ["What does this Kustomization apply?", "Which revision is applied?"];
  return [`What is ${r.name}?`, "Is it healthy?"];
}

function stepLabel(s: AskStep): string {
  const args = Object.entries(s.args)
    .filter(([, v]) => typeof v === "string" || typeof v === "number" || typeof v === "boolean")
    .map(([k, v]) => `${k}=${String(v)}`)
    .join(" ");
  return args ? `${s.tool} ${args}` : s.tool;
}

function AIMessage({ turn, provider }: { turn: Extract<Turn, { kind: "ai" }>; provider?: string }) {
  const model = turn.message.author.client;
  return (
    <div className="ma">
      <div className="who">
        <span className="badge badge-ai">
          <Icon name="spark" />
          AI
        </span>
        <span>{[model, provider && `via ${provider}`].filter(Boolean).join(" ")}</span>
      </div>
      {turn.steps.length > 0 && (
        <ul className="steps" aria-label="Tools used">
          {turn.steps.map((s, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: steps are an ordered log
            <li key={i} className="step">
              <Icon name="chev" />
              <code>{stepLabel(s)}</code>
              <span>· {bytes(s.bytes)}</span>
            </li>
          ))}
        </ul>
      )}
      <Markdown source={turn.message.body} />
    </div>
  );
}

interface AskAIPanelProps {
  cluster: ClusterInfo;
  resource?: Resource;
}

export function AskAIPanel({ cluster, resource }: AskAIPanelProps) {
  const { data: me } = useMe();
  const store = useAskAI();
  const { askFocus, pendingQuestion, clearPendingQuestion, setPane } = useAppState();
  const [wholeCluster, setWholeCluster] = useState(false);
  const [draft, setDraft] = useState("");
  const input = useRef<HTMLTextAreaElement>(null);
  const log = useRef<HTMLDivElement>(null);

  const target = wholeCluster ? undefined : resource;
  const key = `${cluster.name}:${target?.id ?? "*"}`;
  const convo = store.get(key);
  const turns = convo?.turns ?? [];
  const enabled = Boolean(me?.features.ai) && cluster.connected;

  const send = useCallback(
    (question: string) => {
      const q = question.trim();
      if (!q || !enabled) return;
      setDraft("");
      void store.send(key, { cluster: cluster.name, resourceId: target?.id, question: q });
    },
    [enabled, store, key, cluster.name, target?.id],
  );

  useEffect(() => {
    if (askFocus) input.current?.focus();
  }, [askFocus]);

  useEffect(() => {
    if (!pendingQuestion) return;
    clearPendingQuestion();
    send(pendingQuestion);
  }, [pendingQuestion, clearPendingQuestion, send]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: scroll when the transcript grows
  useEffect(() => {
    log.current?.scrollTo({ top: log.current.scrollHeight });
  }, [turns.length, convo?.busy]);

  if (!me?.features.ai) {
    return (
      <div className="ai-off">
        <strong>Ask AI is turned off</strong>
        An administrator can enable it in the hub configuration (<code>ai.enabled</code>).
      </div>
    );
  }

  const subject = target ? target.name : `all of ${cluster.name}`;

  return (
    <div className="ai">
      <div className="aictx">
        About
        <span className="ctxchip">
          <span className="dot" />
          <span>{target ? `${kindInfo(target.kind).abbr} ${target.name}` : `all of ${cluster.name}`}</span>
          {resource && (
            <button
              type="button"
              onClick={() => setWholeCluster(!wholeCluster)}
              aria-label={
                wholeCluster ? `Ask about ${resource.name} instead` : "Ask about the whole cluster instead"
              }
              title={wholeCluster ? `Ask about ${resource.name}` : "Ask about the whole cluster"}
            >
              <Icon name={wholeCluster ? "arrowUp" : "x"} />
            </button>
          )}
        </span>
        {turns.length > 0 && (
          <button type="button" className="btn sm new" onClick={() => store.reset(key)}>
            New chat
          </button>
        )}
      </div>
      <div className="aimsgs" ref={log} aria-live="polite">
        {turns.length === 0 && (
          <div className="ahello">
            <h4>Ask about {subject}</h4>
            <p>Answers come from the statuses, events and YAML you are allowed to see. Nothing is changed.</p>
            <div className="qs">
              {suggestions(cluster, target).map((q) => (
                <button type="button" key={q} className="qchip" onClick={() => send(q)} disabled={!enabled}>
                  <Icon name="spark" />
                  {q}
                </button>
              ))}
            </div>
          </div>
        )}
        {turns.map((t, i) =>
          t.kind === "user" ? (
            // biome-ignore lint/suspicious/noArrayIndexKey: the transcript only grows
            <div key={i} className="mu">
              {t.text}
            </div>
          ) : t.kind === "ai" ? (
            <AIMessage key={t.message.id} turn={t} provider={me.features.aiProvider} />
          ) : (
            // biome-ignore lint/suspicious/noArrayIndexKey: the transcript only grows
            <div key={i} className="ma">
              <div className="err">{t.text}</div>
            </div>
          ),
        )}
        {convo?.busy && (
          <div className="ma">
            <div className="think">
              <StatusIcon status="reconciling" />
              Thinking…
            </div>
          </div>
        )}
      </div>
      <form
        className="comp"
        onSubmit={(e) => {
          e.preventDefault();
          send(draft);
        }}
      >
        <div className="cbox">
          <textarea
            ref={input}
            rows={1}
            value={draft}
            placeholder={enabled ? `Ask about ${subject}…` : `${cluster.name} is disconnected`}
            aria-label="Ask AI"
            disabled={!enabled}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                send(draft);
              } else if (e.key === "Escape") {
                e.preventDefault();
                input.current?.blur();
                setPane("details");
              }
            }}
          />
          <button
            type="submit"
            className="send"
            aria-label="Send"
            disabled={!draft.trim() || convo?.busy || !enabled}
          >
            <Icon name="arrowUp" />
          </button>
        </div>
        <div className="fine">
          <span>AI answers can be wrong. Check before acting on {cluster.name}.</span>
          <span>
            <kbd>↵</kbd> send · <kbd>esc</kbd> close
          </span>
        </div>
      </form>
    </div>
  );
}
