import { createContext, type ReactNode, useCallback, useContext, useEffect, useRef, useState } from "react";
import { isApiError } from "../api/client";
import { askAI } from "../api/endpoints";
import { useMe } from "../api/queries";
import type { AskStep, ClusterInfo, Message, Resource } from "../api/types";
import { ASK_PANEL_ATTR, useAppState } from "../lib/appState";
import { bytes } from "../lib/format";
import { kindInfo } from "../lib/kinds";
import { AutoGrowTextarea } from "./AutoGrowTextarea";
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
    <div className="max-w-full">
      <div className="mb-1.5 flex items-center gap-1.5 text-11-5 text-ink-3">
        <span className="badge badge-ai">
          <Icon name="spark" />
          AI
        </span>
        <span>{[model, provider && `via ${provider}`].filter(Boolean).join(" ")}</span>
      </div>
      {turn.steps.length > 0 && (
        <ul className="mb-2 flex flex-col gap-[3px]" aria-label="Tools used">
          {turn.steps.map((s, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: steps are an ordered log
            <li key={i} className="flex items-center gap-1.5 text-12 text-ink-3">
              <Icon name="chev" className="size-3 shrink-0 text-c" />
              <code className="min-w-0 truncate text-11-5">{stepLabel(s)}</code>
              <span className="shrink-0">· {bytes(s.bytes)}</span>
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
  const { askFocus, takeAskFocus, pendingQuestion, clearPendingQuestion, setPane } = useAppState();
  const [wholeCluster, setWholeCluster] = useState(false);
  const [draft, setDraft] = useState("");
  const input = useRef<HTMLTextAreaElement>(null);
  const log = useRef<HTMLDivElement>(null);

  const target = wholeCluster ? undefined : resource;
  const key = `${cluster.name}:${target?.id ?? "*"}`;
  const convo = store.get(key);
  const turns = convo?.turns ?? [];
  const aiOn = Boolean(me?.features.ai);
  const enabled = aiOn && cluster.connected;

  const send = useCallback(
    (question: string) => {
      const q = question.trim();
      if (!q || !enabled) return;
      setDraft("");
      void store.send(key, { cluster: cluster.name, resourceId: target?.id, question: q });
    },
    [enabled, store, key, cluster.name, target?.id],
  );

  // Focus the question box when Ask AI was opened (tab, `a`, header button, palette), but
  // not merely because the panel re-mounted on another page.
  // biome-ignore lint/correctness/useExhaustiveDependencies: askFocus is the trigger
  useEffect(() => {
    if (takeAskFocus()) input.current?.focus();
  }, [askFocus, takeAskFocus]);

  useEffect(() => {
    if (!pendingQuestion) return;
    clearPendingQuestion();
    send(pendingQuestion);
  }, [pendingQuestion, clearPendingQuestion, send]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: scroll when the transcript grows
  useEffect(() => {
    log.current?.scrollTo({ top: log.current.scrollHeight });
  }, [turns.length, convo?.busy]);

  if (!aiOn) {
    return (
      <div className="m-4 rounded-[14px] border border-dashed border-line-strong p-4 text-13 text-ink-3">
        <strong className="mb-1 block text-ink">Ask AI is turned off</strong>
        An administrator can enable it in the hub configuration (<code>ai.enabled</code>).
      </div>
    );
  }

  const subject = target ? target.name : `all of ${cluster.name}`;

  return (
    <div className="flex min-h-0 flex-1 flex-col" {...{ [ASK_PANEL_ATTR]: "" }}>
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-line px-4 py-3 text-12-5 text-ink-3">
        About
        <span className="inline-flex min-h-[30px] max-w-full items-center gap-[7px] rounded-full border border-line-strong bg-surface py-1 pr-1.5 pl-[9px] font-mono text-12 text-ink">
          <span className="size-[7px] shrink-0 rounded-full bg-c" />
          <span className="truncate">
            {target ? `${kindInfo(target.kind).abbr} ${target.name}` : `all of ${cluster.name}`}
          </span>
          {resource && (
            <button
              type="button"
              className="inline-flex size-[22px] items-center justify-center rounded-full text-ink-3 hover:bg-surface-sunken"
              onClick={() => setWholeCluster(!wholeCluster)}
              aria-label={
                wholeCluster ? `Ask about ${resource.name} instead` : "Ask about the whole cluster instead"
              }
              title={wholeCluster ? `Ask about ${resource.name}` : "Ask about the whole cluster"}
            >
              <Icon name={wholeCluster ? "arrowUp" : "x"} className="size-3.5" />
            </button>
          )}
        </span>
        {turns.length > 0 && (
          <button type="button" className="btn btn-sm ml-auto" onClick={() => store.reset(key)}>
            New chat
          </button>
        )}
      </div>
      {/* The conversation sits at the bottom, like a chat, and grows upwards. */}
      <div className="flex min-h-0 flex-1 flex-col overflow-auto p-4" ref={log} aria-live="polite">
        <div className="mt-auto flex flex-col gap-3.5">
          {!cluster.connected ? (
            <div className="flex flex-col items-center gap-2 rounded-card border border-dashed border-line-strong px-5 py-8 text-center">
              <Icon name="alert" className="size-5 text-warn" />
              <strong className="text-14 font-semibold">{cluster.name} is disconnected.</strong>
              <span className="text-13 text-ink-3">
                Ask AI needs a live agent. It reads statuses, events and YAML through the agent.
              </span>
            </div>
          ) : (
            turns.length === 0 && (
              <div className="rounded-card border border-c/28 bg-surface bg-[radial-gradient(120%_140%_at_0%_0%,color-mix(in_oklab,var(--c)_14%,transparent),transparent_60%),radial-gradient(120%_140%_at_100%_100%,color-mix(in_oklab,var(--c2)_14%,transparent),transparent_60%)] p-4">
                <h4 className="mb-1 text-15 font-semibold">Ask about {subject}</h4>
                <p className="mb-3 text-12-5 text-ink-2">
                  Answers come from the statuses, events and YAML you are allowed to see. Nothing is changed.
                </p>
                <div className="flex flex-col items-start gap-1.5">
                  {suggestions(cluster, target).map((q) => (
                    <QuestionChip key={q} question={q} onAsk={send} />
                  ))}
                </div>
              </div>
            )
          )}
          {turns.map((t, i) =>
            t.kind === "user" ? (
              <div
                // biome-ignore lint/suspicious/noArrayIndexKey: the transcript only grows
                key={i}
                className="max-w-[88%] self-end rounded-[14px_14px_4px_14px] bg-ink px-[13px] py-[9px] text-13-5 break-words whitespace-pre-wrap text-surface"
              >
                {t.text}
              </div>
            ) : t.kind === "ai" ? (
              <AIMessage key={t.message.id} turn={t} provider={me?.features.aiProvider} />
            ) : (
              // biome-ignore lint/suspicious/noArrayIndexKey: the transcript only grows
              <div key={i} className="text-12-5 text-bad">
                {t.text}
              </div>
            ),
          )}
          {convo?.busy && (
            <div className="flex items-center gap-2 text-13 text-ink-3">
              <StatusIcon status="reconciling" />
              Thinking…
            </div>
          )}
        </div>
      </div>
      <form
        className="flex shrink-0 flex-col gap-2 border-t border-line px-3.5 pt-3 pb-3"
        onSubmit={(e) => {
          e.preventDefault();
          send(draft);
        }}
      >
        <div className="flex items-end gap-2 rounded-[14px] border border-line-strong bg-surface py-1.5 pr-1.5 pl-3 focus-within:border-c focus-within:ring-3 focus-within:ring-c/15 has-disabled:opacity-60">
          <AutoGrowTextarea
            ref={input}
            value={draft}
            maxHeight={140}
            placeholder={enabled ? `Ask about ${subject}…` : `${cluster.name} is disconnected`}
            aria-label="Ask AI"
            disabled={!enabled}
            className="min-h-6 flex-1 border-0 bg-transparent py-1.5 text-14 leading-[1.45] text-ink outline-none placeholder:text-ink-3 disabled:cursor-not-allowed"
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
            className="inline-flex size-9 shrink-0 items-center justify-center rounded-control bg-c text-c-ink disabled:cursor-not-allowed disabled:opacity-40"
            aria-label="Send"
            disabled={!draft.trim() || convo?.busy || !enabled}
          >
            <Icon name="arrowUp" />
          </button>
        </div>
        <div className="flex items-center gap-3 overflow-hidden text-11-5 whitespace-nowrap text-ink-3">
          <span
            className="min-w-0 truncate"
            title={`AI answers can be wrong. Check before acting on ${cluster.name}.`}
          >
            AI can be wrong. Check before acting.
          </span>
          <span className="ml-auto inline-flex shrink-0 items-center gap-1">
            <kbd>↵</kbd> send <span aria-hidden="true">·</span> <kbd>esc</kbd> close
          </span>
        </div>
      </form>
    </div>
  );
}

/** A suggested question. */
export function QuestionChip({
  question,
  onAsk,
  disabled,
}: {
  question: string;
  onAsk: (q: string) => void;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      className="inline-flex min-h-[34px] items-center gap-1.5 rounded-full border border-line-strong bg-surface/80 px-[11px] py-1.5 text-left text-12-5 leading-[1.3] hover:border-c disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:border-line-strong [&_svg]:size-[13px] [&_svg]:text-c"
      onClick={() => onAsk(question)}
      disabled={disabled}
    >
      <Icon name="spark" />
      {question}
    </button>
  );
}
