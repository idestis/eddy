import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { createContext, type ReactNode, useCallback, useContext, useEffect, useRef, useState } from "react";
import { isApiError } from "../api/client";
import { askAI, getThread } from "../api/endpoints";
import { threadsQuery, useMe } from "../api/queries";
import type { AskAttachment, AskStep, ClusterInfo, Message, Resource, Thread } from "../api/types";
import { ASK_PANEL_ATTR, useAppState } from "../lib/appState";
import { ago, bytes } from "../lib/format";
import { kindInfo } from "../lib/kinds";
import { detailLink } from "../lib/links";
import { attachmentLabel, type PendingAttachment } from "../lib/logAttachments";
import { AutoGrowTextarea } from "./AutoGrowTextarea";
import { Icon } from "./Icon";
import { Markdown } from "./Markdown";
import { StatusIcon } from "./Status";

export type Turn =
  | { kind: "user"; text: string; attachment?: PendingAttachment }
  | { kind: "ai"; message: Message; steps: AskStep[] }
  | { kind: "error"; text: string };

interface Conversation {
  threadId?: string;
  turns: Turn[];
  busy: boolean;
}

interface AskAIStore {
  get: (key: string) => Conversation | undefined;
  send: (
    key: string,
    req: { cluster: string; resourceId?: string; question: string; attachment?: PendingAttachment },
  ) => Promise<void>;
  reset: (key: string) => void;
  /** Replaces a conversation with a stored ask thread, so the next question continues it. */
  load: (key: string, threadId: string, turns: Turn[]) => void;
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
      const { attachment, ...rest } = req;
      patch(key, (c) => ({
        ...c,
        busy: true,
        turns: [...c.turns, { kind: "user", text: req.question, attachment }],
      }));
      try {
        const attachments: AskAttachment[] | undefined = attachment ? [attachment.attachment] : undefined;
        const res = await askAI({ ...rest, attachments, threadId: current?.threadId });
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

  const load = useCallback((key: string, threadId: string, turns: Turn[]) => {
    setConversations((prev) => new Map(prev).set(key, { threadId, turns, busy: false }));
  }, []);

  const get = useCallback((key: string) => conversations.get(key), [conversations]);

  return <AskAIContext.Provider value={{ get, send, reset, load }}>{children}</AskAIContext.Provider>;
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
  // Logs reach the AI only as attachments from the Logs tab, so no suggestion promises them.
  if (r.kind === "Pod")
    return r.status === "ready" || r.status === "completed"
      ? ["Which image is this?", "What owns this pod?"]
      : ["Why is this pod not running?", "What do its events say?"];
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

/** Rebuilds a conversation from a stored ask thread. */
export function turnsFromMessages(messages: readonly Message[]): Turn[] {
  return messages.map((m): Turn => {
    if (m.author.type !== "ai") return { kind: "user", text: m.body };
    const steps = (m.meta as { steps?: AskStep[] } | undefined)?.steps;
    return { kind: "ai", message: m, steps: Array.isArray(steps) ? steps : [] };
  });
}

/** The earlier private chats about this cluster or resource, newest first. */
function AskHistory({
  cluster,
  target,
  onOpen,
}: {
  cluster: string;
  target: Resource | undefined;
  onOpen: (t: Thread) => void;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const { data, isPending, isError } = useQuery({
    ...threadsQuery({
      type: "ask",
      cluster,
      kind: target?.kind,
      namespace: target?.namespace,
      name: target?.name,
      limit: 20,
    }),
    enabled: open,
  });
  // The API treats an empty kind as "any": keep the exact context only.
  const items = (data?.items ?? []).filter((t) =>
    target
      ? t.ref.kind === target.kind && t.ref.namespace === target.namespace && t.ref.name === target.name
      : t.ref.kind === "",
  );

  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => {
      if (e.target instanceof Node && !root.current?.contains(e.target)) setOpen(false);
    };
    document.addEventListener("pointerdown", onDown);
    return () => document.removeEventListener("pointerdown", onDown);
  }, [open]);

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: Esc closes the menu; the buttons inside are the controls
    <div
      className="relative"
      ref={root}
      onKeyDown={(e) => {
        if (e.key === "Escape" && open) {
          e.preventDefault();
          e.stopPropagation();
          setOpen(false);
          trigger.current?.focus();
        }
      }}
    >
      <button
        ref={trigger}
        type="button"
        className="btn btn-sm"
        aria-haspopup="true"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <Icon name="clock" />
        History
      </button>
      {open && (
        <div
          className="absolute bottom-full left-0 z-20 mb-1.5 flex max-h-72 w-[min(320px,calc(100vw-32px))] flex-col overflow-auto rounded-xl border border-line-strong bg-surface p-1.5 shadow-window"
          role="menu"
          aria-label="Previous chats"
        >
          {isPending && <div className="px-2.5 py-2 text-12-5 text-ink-3">Loading…</div>}
          {isError && <div className="px-2.5 py-2 text-12-5 text-bad">Couldn't load your chats.</div>}
          {!isPending && !isError && items.length === 0 && (
            <div className="px-2.5 py-2 text-12-5 text-ink-3">No earlier chats about this yet.</div>
          )}
          {items.map((t) => (
            <button
              key={t.id}
              type="button"
              role="menuitem"
              className="flex min-w-0 flex-col items-start gap-0.5 rounded-lg px-2.5 py-2 text-left hover:bg-surface-sunken"
              onClick={() => {
                setOpen(false);
                onOpen(t);
              }}
            >
              <span className="w-full truncate text-13 font-medium">{t.title}</span>
              <span className="text-11-5 text-ink-3">
                {ago(t.updatedAt)} · {t.messageCount} {t.messageCount === 1 ? "message" : "messages"}
              </span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function AIMessage({ turn, provider }: { turn: Extract<Turn, { kind: "ai" }>; provider?: string }) {
  const model = turn.message.author.client;
  return (
    <div className="min-w-0 max-w-full">
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
  const {
    askFocus,
    takeAskFocus,
    takeNewChat,
    pendingQuestion,
    clearPendingQuestion,
    setPane,
    pendingAttachment,
    clearPendingAttachment,
  } = useAppState();
  const [attachment, setAttachment] = useState<PendingAttachment | null>(null);
  const [selectDraft, setSelectDraft] = useState(0);
  const qc = useQueryClient();
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
      setAttachment(null);
      void store.send(key, {
        cluster: cluster.name,
        resourceId: target?.id,
        question: q,
        attachment: attachment ?? undefined,
      });
    },
    [enabled, store, key, cluster.name, target?.id, attachment],
  );

  const newChat = useCallback(() => {
    store.reset(key);
    setDraft("");
    setAttachment(null);
    input.current?.focus();
  }, [store, key]);

  const openThread = useCallback(
    async (t: Thread) => {
      try {
        const detail = await qc.fetchQuery({
          queryKey: ["threads", "detail", t.id],
          queryFn: () => getThread(t.id),
          staleTime: 0,
        });
        store.load(key, t.id, turnsFromMessages(detail.messages));
        input.current?.focus();
      } catch {
        store.load(key, t.id, [{ kind: "error", text: "Couldn't open that chat. Try again." }]);
      }
    },
    [qc, store, key],
  );

  // "New Ask AI chat" from the palette arrives as a signal, possibly before this panel mounted.
  // biome-ignore lint/correctness/useExhaustiveDependencies: askFocus is the trigger
  useEffect(() => {
    if (takeNewChat()) store.reset(key);
  }, [askFocus, takeNewChat, store, key]);

  // Focus the question box when Ask AI was opened (tab, `a`, header button, palette), but
  // not merely because the panel re-mounted on another page.
  // biome-ignore lint/correctness/useExhaustiveDependencies: askFocus is the trigger
  useEffect(() => {
    if (takeAskFocus()) input.current?.focus();
  }, [askFocus, takeAskFocus]);

  // Log lines from a Logs tab: a new chat, the lines in the composer, the suggested
  // question selected so typing replaces it.
  useEffect(() => {
    if (!pendingAttachment) return;
    clearPendingAttachment();
    store.reset(key);
    setAttachment(pendingAttachment);
    setDraft(pendingAttachment.question);
    setSelectDraft((n) => n + 1);
  }, [pendingAttachment, clearPendingAttachment, store, key]);

  // After the suggested question is in the box: focus it, all selected.
  useEffect(() => {
    if (!selectDraft) return;
    input.current?.focus();
    input.current?.select();
  }, [selectDraft]);

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
    // biome-ignore lint/a11y/noStaticElementInteractions: Esc anywhere in the panel returns to Details
    <div
      className="flex min-h-0 flex-1 flex-col"
      {...{ [ASK_PANEL_ATTR]: "" }}
      onKeyDown={(e) => {
        // Esc anywhere in the panel goes back to Details (the question box handles its own Esc).
        if (e.key === "Escape" && e.target !== input.current && !e.defaultPrevented) {
          e.stopPropagation();
          setPane("details");
        }
      }}
    >
      <div className="flex shrink-0 flex-col gap-2 border-b border-line px-4 py-3 text-12-5 text-ink-3">
        <div className="flex min-w-0 items-center gap-2">
          About
          <span className="inline-flex min-h-[30px] min-w-0 max-w-full items-center gap-[7px] rounded-full border border-line-strong bg-surface py-1 pr-1.5 pl-[9px] font-mono text-12 text-ink">
            <Link
              {...(target
                ? detailLink(cluster.name, target)
                : { to: "/c/$cluster" as const, params: { cluster: cluster.name } })}
              className="-my-1 -ml-[9px] inline-flex min-w-0 items-center gap-[7px] rounded-full py-1 pl-[9px] pr-1 text-ink no-underline hover:bg-surface-sunken"
              title={target ? `Open ${target.name}` : `Open ${cluster.name}`}
              onClick={() => setPane("details")}
            >
              <span className="size-[7px] shrink-0 rounded-full bg-c" />
              <span className="truncate">
                {target ? `${kindInfo(target.kind).abbr} ${target.name}` : `all of ${cluster.name}`}
              </span>
            </Link>
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
        </div>
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
                className="flex max-w-[88%] flex-col items-end gap-1.5 self-end"
              >
                {t.attachment && <AttachmentChip pending={t.attachment} sent />}
                <div className="rounded-[14px_14px_4px_14px] bg-ink px-[13px] py-[9px] text-13-5 break-words whitespace-pre-wrap text-surface">
                  {t.text}
                </div>
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
        {/* Conversation controls sit with the composer: start over or reopen an earlier chat. */}
        <div className="flex items-center gap-1.5">
          <AskHistory cluster={cluster.name} target={target} onOpen={(t) => void openThread(t)} />
          <button
            type="button"
            className="btn btn-sm"
            onClick={newChat}
            disabled={turns.length === 0 && !convo?.threadId}
          >
            <Icon name="plus" />
            New chat
          </button>
        </div>
        {attachment && (
          <AttachmentChip
            pending={attachment}
            onRemove={() => {
              setAttachment(null);
              input.current?.focus();
            }}
          />
        )}
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
                e.stopPropagation();
                // A draft goes first; Esc on an empty box returns to Details.
                if (draft) {
                  setDraft("");
                } else {
                  input.current?.blur();
                  setPane("details");
                }
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
            <kbd>↵</kbd> send <span aria-hidden="true">·</span> <kbd>esc</kbd> {draft ? "clear" : "details"}
          </span>
        </div>
      </form>
    </div>
  );
}

/**
 * Log lines attached to a question: in the composer (removable) or on a sent message.
 * The source opens its Logs tab; the lines expand into a preview.
 */
export function AttachmentChip({
  pending,
  onRemove,
  sent,
}: {
  pending: PendingAttachment;
  onRemove?: () => void;
  sent?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const { setPane } = useAppState();
  const link = pending.link;
  const label = attachmentLabel(pending);
  return (
    <div
      className={`anim-fade-in flex min-w-0 flex-col rounded-xl border text-12 ${sent ? "self-end border-line bg-surface-sunken" : "border-c/35 bg-c-soft"}`}
    >
      <div className="flex min-w-0 items-center gap-1.5 py-1 pr-1 pl-2">
        <Icon name="file" className="size-3.5 shrink-0 text-c" />
        {sent && <span className="shrink-0 font-semibold text-ink-2">attached</span>}
        {link ? (
          <Link
            {...detailLink(link.cluster, link.ref, "logs")}
            className="min-w-0 truncate font-mono text-ink no-underline hover:underline"
            title={`Open the logs of ${link.ref.name}`}
            onClick={() => setPane("details")}
          >
            {label}
          </Link>
        ) : (
          <span className="min-w-0 truncate font-mono">{label}</span>
        )}
        <button
          type="button"
          className="ml-auto inline-flex h-6 shrink-0 items-center rounded-md px-1.5 text-11-5 text-ink-3 hover:bg-surface hover:text-ink"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          {open ? "Hide" : "Preview"}
        </button>
        {onRemove && (
          <button
            type="button"
            className="inline-flex size-6 shrink-0 items-center justify-center rounded-md text-ink-3 hover:bg-surface hover:text-ink"
            aria-label="Remove the attached lines"
            onClick={onRemove}
          >
            <Icon name="x" className="size-3.5" />
          </button>
        )}
      </div>
      {open && (
        <pre className="anim-fade-in m-0 max-h-48 overflow-auto border-t border-line bg-code-bg px-2.5 py-2 font-mono text-11-5 leading-[1.5] whitespace-pre text-code-ink">
          {pending.attachment.lines.join("\n")}
        </pre>
      )}
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
