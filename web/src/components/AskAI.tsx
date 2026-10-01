// The Ask AI panel (ADR-0007). It shows the open chat, which lives at the app level
// (lib/chatState.tsx), so moving between resources, clusters and pages keeps it. The header
// names the chat and switches chats; the chips say what the chat is about; @mentions and the
// + picker add to them.

import { Link } from "@tanstack/react-router";
import {
  type RefObject,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useChat, usePatchChat } from "../api/chats";
import { isApiError } from "../api/client";
import { useCluster, useMe } from "../api/queries";
import type { AskStep, ClusterInfo, Message, Resource, ResourceRef } from "../api/types";
import { refsOf } from "../lib/answerRefs";
import { ASK_PANEL_ATTR, useAppState } from "../lib/appState";
import { caretOffset } from "../lib/caret";
import {
  hasRef,
  isClusterRef,
  MAX_CONTEXT,
  mentionAt,
  mentionText,
  resourceRef,
  sameContext,
  sameRef,
  screenContext,
  withoutRef,
  withRef,
} from "../lib/chatContext";
import { type PendingAsk, useChatState } from "../lib/chatState";
import { bytes } from "../lib/format";
import { detailLink } from "../lib/links";
import { attachmentLabel, type PendingAttachment } from "../lib/logAttachments";
import { AnswerActions } from "./AnswerActions";
import { AutoGrowTextarea } from "./AutoGrowTextarea";
import { ChatHistory, TitleInput } from "./ChatHistory";
import {
  ContextChip,
  ContextListbox,
  type ContextOption,
  ContextPicker,
  listKeys,
  optionId,
  SuggestChip,
  useActiveOption,
  useContextOptions,
} from "./ContextChips";
import { Icon } from "./Icon";
import { Markdown } from "./Markdown";
import { RemoveButton } from "./RemovableChip";
import { KeyHint, StatusIcon } from "./Status";
import { useToast } from "./Toasts";

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

const stepsOf = (m: Message): AskStep[] => {
  const steps = (m.meta as { steps?: unknown } | undefined)?.steps;
  return Array.isArray(steps) ? (steps as AskStep[]) : [];
};

function AIMessage({
  message,
  provider,
  question,
  attachment,
  targets,
}: {
  message: Message;
  provider?: string;
  /** The question this answers; with targets, the answer can be saved as a thread. */
  question?: string;
  attachment?: PendingAttachment;
  targets: readonly ResourceRef[];
}) {
  const model = message.author.client;
  const steps = stepsOf(message);
  const refs = useMemo(() => refsOf(message.meta), [message.meta]);
  return (
    <div className="min-w-0 max-w-full">
      <div className="mb-1.5 flex items-center gap-1.5 text-11-5 text-ink-3">
        <span className="badge badge-ai">
          <Icon name="spark" />
          AI
        </span>
        <span>{[model, provider && `via ${provider}`].filter(Boolean).join(" ")}</span>
      </div>
      {steps.length > 0 && (
        <ul className="mb-2 flex flex-col gap-[3px]" aria-label="Tools used">
          {steps.map((s, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: steps are an ordered log
            <li key={i} className="flex items-center gap-1.5 text-12 text-ink-3">
              <Icon name="chev" className="size-3 shrink-0 text-c" />
              <code className="min-w-0 truncate text-11-5">{stepLabel(s)}</code>
              <span className="shrink-0">· {bytes(s.bytes)}</span>
            </li>
          ))}
        </ul>
      )}
      <Markdown source={message.body} refs={refs} />
      {question !== undefined && targets.length > 0 && (
        <AnswerActions
          message={message}
          provider={provider}
          question={question}
          attachment={attachment}
          targets={targets}
        />
      )}
    </div>
  );
}

function UserMessage({
  text,
  attachment,
  failed,
}: {
  text: string;
  attachment?: PendingAttachment;
  failed?: boolean;
}) {
  return (
    <div className="flex max-w-[88%] flex-col items-end gap-1.5 self-end">
      {attachment && <AttachmentChip pending={attachment} sent />}
      <div
        className={`rounded-[14px_14px_4px_14px] bg-ink px-[13px] py-[9px] text-13-5 break-words whitespace-pre-wrap text-surface ${failed ? "opacity-60" : ""}`}
      >
        {text}
      </div>
    </div>
  );
}

/** What the chat is about, in a few words, for the empty state and the placeholder. */
function subjectOf(context: readonly ResourceRef[]): string {
  const first = context[0];
  if (!first) return "anything you can see";
  const name = isClusterRef(first) ? first.cluster : first.name;
  return context.length > 1 ? `${name} and ${context.length - 1} more` : name;
}

function firstQuestions(
  context: readonly ResourceRef[],
  current: ClusterInfo,
  firstCluster: ClusterInfo | undefined,
  selected: ResourceRef | undefined,
  resource: Resource | undefined,
): string[] {
  const first = context[0];
  if (!first) return ["What needs attention across the fleet?", `Is anything unhealthy on ${current.name}?`];
  if (isClusterRef(first)) return suggestions(firstCluster ?? current, undefined);
  if (selected && resource && sameRef(first, selected)) return suggestions(current, resource);
  return [`Is ${first.name} healthy?`, `What changed recently on ${first.name}?`];
}

interface AskAIPanelProps {
  /** The cluster on screen. */
  cluster: ClusterInfo;
  /** The resource on screen, if any. */
  resource?: Resource;
}

export function AskAIPanel({ cluster, resource }: AskAIPanelProps) {
  const { data: me } = useMe();
  const state = useChatState();
  const {
    chatId,
    edited,
    setEdited,
    view,
    setView,
    newChat,
    openHistory,
    forget,
    draft,
    setDraft,
    attachment,
    setAttachment,
    pending,
    send: sendAsk,
    killed,
    statusOf,
    attachmentFor,
    dismissError,
    scroll,
  } = state;
  const {
    askFocus,
    takeAskFocus,
    pendingQuestion,
    clearPendingQuestion,
    setPane,
    pendingAttachment,
    clearPendingAttachment,
  } = useAppState();
  const { chat, messages, isPending, error } = useChat(chatId);
  const patch = usePatchChat();
  const toast = useToast();
  const [selectDraft, setSelectDraft] = useState(0);
  const [picker, setPicker] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const input = useRef<HTMLTextAreaElement>(null);
  const log = useRef<HTMLDivElement>(null);
  const plus = useRef<HTMLButtonElement>(null);

  const canAsk = Boolean(me?.features.ai) && !killed;
  const selected = resource ? resourceRef(cluster.name, resource) : undefined;
  const onScreen = useMemo(() => screenContext(cluster.name, resource), [cluster.name, resource]);
  const stored = chat?.context;
  // A new chat follows the screen until the user edits its chips; an open chat keeps its own.
  const context = useMemo(
    () => edited ?? (chatId ? (stored ?? []) : onScreen),
    [edited, chatId, stored, onScreen],
  );
  const contextChanged = chatId ? edited !== null && !sameContext(edited, stored ?? []) : true;
  const fixed = Boolean(chatId) || edited !== null;
  const suggestion =
    fixed && selected && !hasRef(context, selected) && context.length < MAX_CONTEXT ? selected : undefined;
  const firstCluster = useCluster(context[0]?.cluster);
  const pickerSources = useMemo(
    () => (resource && selected ? [{ ref: selected, resource }] : []),
    [resource, selected],
  );
  const shownPending: PendingAsk | null = pending && pending.chat === (chatId ?? "") ? pending : null;
  const busy = Boolean(pending && !pending.error);
  const visible = useMemo(
    () => context.filter((r) => !(chatId && statusOf(chatId, r) === "hidden")),
    [context, chatId, statusOf],
  );

  // The stored chat is gone (deleted elsewhere, or expired): start over.
  useEffect(() => {
    if (chatId && isApiError(error, "not_found")) {
      forget(chatId);
      toast("That chat no longer exists. Starting a new one.");
    }
  }, [chatId, error, forget, toast]);

  const setContext = useCallback((refs: ResourceRef[]) => setEdited(refs), [setEdited]);
  const addRef = useCallback((r: ResourceRef) => setContext(withRef(context, r)), [context, setContext]);

  const send = useCallback(
    (question: string) => {
      const q = question.trim();
      if (!q || !canAsk || busy) return;
      const att = attachment ?? undefined;
      setDraft("");
      setAttachment(null);
      void sendAsk({ question: q, attachment: att, context, contextChanged });
    },
    [canAsk, busy, attachment, setDraft, setAttachment, sendAsk, context, contextChanged],
  );

  const retry = useCallback(() => {
    if (!shownPending?.error || !canAsk) return;
    void sendAsk({
      question: shownPending.question,
      attachment: shownPending.attachment,
      context,
      contextChanged,
    });
  }, [shownPending, canAsk, sendAsk, context, contextChanged]);

  // Focus the question box when Ask AI was opened (tab, `a`, header button, palette), but
  // not merely because the panel re-mounted on another page.
  // biome-ignore lint/correctness/useExhaustiveDependencies: askFocus is the trigger
  useEffect(() => {
    if (takeAskFocus()) input.current?.focus();
  }, [askFocus, takeAskFocus]);

  // Lines from a Logs tab or a YAML selection: into the open chat's composer, with the
  // suggested question selected so typing replaces it.
  useEffect(() => {
    if (!pendingAttachment) return;
    clearPendingAttachment();
    setView("chat");
    setAttachment(pendingAttachment);
    setDraft(pendingAttachment.question);
    setSelectDraft((n) => n + 1);
  }, [pendingAttachment, clearPendingAttachment, setAttachment, setDraft, setView]);

  // After the suggested question is in the box: focus it, all selected.
  useEffect(() => {
    if (!selectDraft) return;
    input.current?.focus();
    input.current?.select();
  }, [selectDraft]);

  useEffect(() => {
    if (!pendingQuestion) return;
    clearPendingQuestion();
    setView("chat");
    send(pendingQuestion);
  }, [pendingQuestion, clearPendingQuestion, send, setView]);

  // The transcript opens where it was left; a new message scrolls to the bottom.
  const scrollKey = chatId ?? "";
  const count = messages.length + (shownPending ? 1 : 0);
  const seen = useRef<{ key: string; count: number } | null>(null);
  useLayoutEffect(() => {
    const el = log.current;
    if (!el || view !== "chat") return;
    const prev = seen.current;
    seen.current = { key: scrollKey, count };
    const saved = scroll.get(scrollKey);
    if ((!prev || prev.key !== scrollKey) && saved !== undefined && count > 0) el.scrollTop = saved;
    else if (!prev || prev.key !== scrollKey || count > prev.count) el.scrollTo?.({ top: el.scrollHeight });
  }, [scrollKey, count, view, scroll]);

  const title = chat?.title || (chatId ? "Chat" : "New chat");
  const subject = subjectOf(context);

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: Esc anywhere in the panel returns to Details
    <div
      className="flex min-h-0 flex-1 flex-col"
      {...{ [ASK_PANEL_ATTR]: "" }}
      onKeyDown={(e) => {
        // Esc anywhere in the panel goes back to Details (inputs handle their own Esc first).
        if (e.key === "Escape" && e.target !== input.current && !e.defaultPrevented) {
          e.stopPropagation();
          if (view === "history") setView("chat");
          else setPane("details");
        }
      }}
    >
      <div className="flex min-h-[52px] shrink-0 items-center gap-1.5 border-b border-line py-2 pr-2.5 pl-4">
        {view === "history" ? (
          <h3 className="min-w-0 flex-1 truncate text-14 font-semibold">Your chats</h3>
        ) : renaming && chat ? (
          <TitleInput
            value={chat.title}
            label="Chat title"
            className="min-w-0 flex-1 text-14 font-semibold"
            onCancel={() => setRenaming(false)}
            onSave={(t) => {
              setRenaming(false);
              patch.mutate(
                { id: chat.id, title: t },
                { onError: () => toast("Couldn't rename the chat. Try again.", "bad") },
              );
            }}
          />
        ) : chat ? (
          <button
            type="button"
            className="group/title -ml-1.5 flex min-w-0 flex-1 items-center gap-1.5 rounded-lg px-1.5 py-1 text-left text-14 font-semibold hover:bg-surface-sunken"
            title="Rename this chat"
            aria-label={`Rename chat: ${title}`}
            onClick={() => setRenaming(true)}
          >
            <span className="min-w-0 truncate">{title}</span>
            <Icon
              name="pencil"
              className="size-3 shrink-0 text-ink-3 opacity-0 group-hover/title:opacity-100 group-focus-visible/title:opacity-100"
            />
          </button>
        ) : (
          <h3 className="min-w-0 flex-1 truncate text-14 font-semibold">{title}</h3>
        )}
        {view === "history" ? (
          <button type="button" className="btn btn-sm btn-ghost" onClick={() => setView("chat")}>
            <Icon name="back" />
            Back to chat
          </button>
        ) : (
          <button
            type="button"
            className="btn btn-sm btn-ghost"
            aria-label="History"
            title="Your earlier chats"
            onClick={openHistory}
          >
            <Icon name="clock" />
            <span className="max-[420px]:hidden">History</span>
          </button>
        )}
        <button
          type="button"
          className="btn btn-sm"
          onClick={newChat}
          disabled={!canAsk || (view === "chat" && !chatId && edited === null && !draft)}
          aria-label="New chat"
        >
          <Icon name="plus" />
          <span className="max-[420px]:hidden">New chat</span>
          <KeyHint id="newChat" />
        </button>
      </div>

      {view === "history" ? (
        <ChatHistory />
      ) : (
        <>
          <div className="relative flex shrink-0 flex-wrap items-center gap-1.5 border-b border-line px-4 py-2.5 text-12-5 text-ink-3">
            <span className="mr-0.5">About</span>
            {context.map((r) => (
              <ContextChip
                key={`${r.cluster}/${r.group}/${r.kind}/${r.namespace}/${r.name}`}
                r={r}
                current={cluster.name}
                hidden={Boolean(chatId) && statusOf(chatId ?? "", r) === "hidden"}
                onRemove={canAsk ? () => setContext(withoutRef(context, r)) : undefined}
              />
            ))}
            {context.length === 0 && <span className="italic">nothing yet</span>}
            {canAsk && context.length < MAX_CONTEXT && (
              <button
                ref={plus}
                type="button"
                className="inline-flex size-[30px] shrink-0 items-center justify-center rounded-full border border-line-strong text-ink-2 hover:border-c hover:text-ink focus-visible:ring-2 focus-visible:ring-c focus-visible:outline-none aria-expanded:border-c"
                aria-label="Add a resource or cluster"
                aria-expanded={picker}
                title="Add a resource or cluster"
                onClick={() => setPicker(!picker)}
              >
                <Icon name="plus" className="size-3.5" />
              </button>
            )}
            {canAsk && suggestion && <SuggestChip r={suggestion} onAdd={() => addRef(suggestion)} />}
            {picker && (
              <ContextPicker
                current={cluster.name}
                onScreen={pickerSources}
                exclude={context}
                onPick={(r) => {
                  addRef(r);
                  setPicker(false);
                  // Back to the question: the next Enter sends.
                  input.current?.focus();
                }}
                onClose={() => {
                  setPicker(false);
                  plus.current?.focus();
                }}
              />
            )}
          </div>

          {/* The conversation sits at the bottom, like a chat, and grows upwards. */}
          <div
            className="flex min-h-0 flex-1 flex-col overflow-auto p-4"
            ref={log}
            aria-live="polite"
            onScroll={(e) => scroll.set(scrollKey, e.currentTarget.scrollTop)}
          >
            <div className="mt-auto flex flex-col gap-3.5">
              {!canAsk && (
                <div
                  className="flex items-start gap-2 rounded-tile border border-dashed border-line-strong px-3.5 py-3 text-12-5 text-ink-2"
                  role="status"
                >
                  <Icon name="info" className="mt-px size-4 shrink-0 text-ink-3" />
                  <span>
                    <strong className="block text-13 text-ink">Ask AI is turned off on this hub</strong>
                    You can still read, rename and delete your chats. An administrator can turn it back on (
                    <code>ai.enabled</code>).
                  </span>
                </div>
              )}
              {chatId && isPending ? (
                <div className="flex items-center gap-2 text-13 text-ink-3">
                  <StatusIcon status="reconciling" />
                  Loading the chat…
                </div>
              ) : chatId && error && !isApiError(error, "not_found") ? (
                <div className="text-12-5 text-bad">Couldn't load this chat. Try again in a moment.</div>
              ) : (
                messages.length === 0 &&
                !shownPending &&
                canAsk && (
                  <div className="rounded-card border border-c/28 bg-surface bg-[radial-gradient(120%_140%_at_0%_0%,color-mix(in_oklab,var(--c)_14%,transparent),transparent_60%),radial-gradient(120%_140%_at_100%_100%,color-mix(in_oklab,var(--c2)_14%,transparent),transparent_60%)] p-4">
                    <h4 className="mb-1 text-15 font-semibold">Ask about {subject}</h4>
                    <p className="mb-3 text-12-5 text-ink-2">
                      Answers come from the statuses, events and YAML you are allowed to see. Nothing is
                      changed. Type <kbd>@</kbd> to bring in another resource.
                    </p>
                    <div className="flex flex-col items-start gap-1.5">
                      {firstQuestions(context, cluster, firstCluster, selected, resource).map((q) => (
                        <QuestionChip key={q} question={q} onAsk={send} />
                      ))}
                    </div>
                  </div>
                )
              )}
              {messages.map((m, i) => {
                if (m.author.type !== "ai") {
                  const next = messages[i + 1];
                  return (
                    <UserMessage
                      key={m.id}
                      text={m.body}
                      attachment={next?.author.type === "ai" ? attachmentFor(next.id) : undefined}
                    />
                  );
                }
                const prev = messages[i - 1];
                return (
                  <AIMessage
                    key={m.id}
                    message={m}
                    provider={me?.features.aiProvider}
                    question={prev && prev.author.type !== "ai" ? prev.body : undefined}
                    attachment={attachmentFor(m.id)}
                    targets={visible}
                  />
                );
              })}
              {shownPending && (
                <UserMessage
                  text={shownPending.question}
                  attachment={shownPending.attachment}
                  failed={Boolean(shownPending.error)}
                />
              )}
              {shownPending?.error ? (
                <div className="flex flex-wrap items-center gap-2 text-12-5 text-bad" role="alert">
                  <span className="min-w-0">{shownPending.error}</span>
                  {canAsk && (
                    <button type="button" className="btn btn-sm" onClick={retry}>
                      Try again
                    </button>
                  )}
                  <button type="button" className="btn btn-sm btn-ghost" onClick={dismissError}>
                    Dismiss
                  </button>
                </div>
              ) : (
                shownPending && (
                  <div className="flex items-center gap-2 text-13 text-ink-3" role="status">
                    <StatusIcon status="reconciling" />
                    Thinking…
                  </div>
                )
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
            {attachment && (
              <AttachmentChip
                pending={attachment}
                onRemove={() => {
                  setAttachment(null);
                  input.current?.focus();
                }}
              />
            )}
            <Composer
              inputRef={input}
              value={draft}
              onChange={setDraft}
              onSubmit={() => send(draft)}
              disabled={!canAsk}
              busy={busy}
              placeholder={canAsk ? `Ask about ${subject}… (@ to mention)` : "Ask AI is turned off"}
              current={cluster.name}
              onScreen={pickerSources}
              context={context}
              onMention={addRef}
              onEscapeEmpty={() => {
                input.current?.blur();
                setPane("details");
              }}
            />
            <div className="flex items-center gap-3 overflow-hidden text-11-5 whitespace-nowrap text-ink-3">
              <span className="min-w-0 truncate" title="AI answers can be wrong. Check before acting.">
                AI can be wrong. Check before acting.
              </span>
              <span className="ml-auto inline-flex shrink-0 items-center gap-1">
                <kbd>@</kbd> mention <span aria-hidden="true">·</span> <kbd>↵</kbd> send{" "}
                <span aria-hidden="true">·</span> <kbd>esc</kbd> {draft ? "clear" : "details"}
              </span>
            </div>
          </form>
        </>
      )}
    </div>
  );
}

/**
 * The question box. Typing `@` opens a list of resources and clusters under the caret
 * (↑ ↓ Enter, Esc); picking one writes `@namespace/name` and adds it to the chips.
 */
function Composer({
  inputRef,
  value,
  onChange,
  onSubmit,
  disabled,
  busy,
  placeholder,
  current,
  onScreen,
  context,
  onMention,
  onEscapeEmpty,
}: {
  inputRef: RefObject<HTMLTextAreaElement | null>;
  value: string;
  onChange: (v: string) => void;
  onSubmit: () => void;
  disabled: boolean;
  busy: boolean;
  placeholder: string;
  current: string;
  onScreen: ReadonlyArray<{ ref: ResourceRef; resource?: Resource }>;
  context: readonly ResourceRef[];
  onMention: (r: ResourceRef) => void;
  onEscapeEmpty: () => void;
}) {
  const listId = useId();
  const box = useRef<HTMLDivElement>(null);
  const [mention, setMention] = useState<{ start: number; query: string } | null>(null);
  const [dismissed, setDismissed] = useState<number | null>(null);
  const [pos, setPos] = useState<{ left: number; top?: number; bottom?: number }>({ left: 0 });
  const caretAfter = useRef<number | null>(null);
  const found = useContextOptions(mention?.query ?? "", {
    current,
    onScreen,
    exclude: context,
    enabled: mention !== null,
  });
  const nav = useActiveOption(found.options);
  const open = mention !== null && !disabled;

  const track = (el: HTMLTextAreaElement) => {
    const caret = el.selectionStart;
    const m = el.selectionStart === el.selectionEnd ? mentionAt(el.value, caret) : undefined;
    if (!m || m.start === dismissed) {
      setMention(null);
      if (!m) setDismissed(null);
      return;
    }
    setMention((prev) => (prev && prev.start === m.start && prev.query === m.query ? prev : m));
  };

  const pick = (o: ContextOption) => {
    const el = inputRef.current;
    if (!mention || !el) return;
    const caret = el.selectionStart;
    const insert = `${mentionText(o.ref)} `;
    const rest = value.slice(caret).replace(/^ /, "");
    const next = value.slice(0, mention.start) + insert + rest;
    caretAfter.current = mention.start + insert.length;
    onChange(next);
    onMention(o.ref);
    setMention(null);
  };

  // Puts the caret after an inserted mention.
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs after the value changed
  useLayoutEffect(() => {
    const at = caretAfter.current;
    const el = inputRef.current;
    if (at === null || !el) return;
    caretAfter.current = null;
    el.setSelectionRange(at, at);
  }, [value]);

  // Under the caret when there is room below, otherwise above it.
  useLayoutEffect(() => {
    const el = inputRef.current;
    const wrap = box.current;
    if (!open || !el || !wrap || !mention) return;
    const c = caretOffset(el, mention.start);
    const left = Math.max(0, Math.min(el.offsetLeft + c.left - 8, wrap.clientWidth - 380));
    const caretTop = el.offsetTop + c.top;
    const below = window.innerHeight - (el.getBoundingClientRect().top + c.top + c.height);
    setPos(
      below > 280
        ? { left, top: caretTop + c.height + 6 }
        : { left, bottom: wrap.clientHeight - caretTop + 6 },
    );
  }, [open, mention, inputRef]);

  return (
    <div
      ref={box}
      className="relative flex items-end gap-2 rounded-[14px] border border-line-strong bg-surface py-1.5 pr-1.5 pl-3 focus-within:border-c focus-within:ring-3 focus-within:ring-c/15 has-disabled:opacity-60"
    >
      <AutoGrowTextarea
        ref={inputRef}
        value={value}
        maxHeight={140}
        placeholder={placeholder}
        aria-label="Ask AI"
        role="combobox"
        aria-autocomplete="list"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-activedescendant={open && found.options.length ? optionId(listId, nav.active) : undefined}
        disabled={disabled}
        className="min-h-6 flex-1 border-0 bg-transparent py-1.5 text-14 leading-[1.45] text-ink outline-none placeholder:text-ink-3 disabled:cursor-not-allowed"
        onChange={(e) => {
          onChange(e.target.value);
          track(e.target);
        }}
        onSelect={(e) => track(e.currentTarget)}
        onBlur={() => setMention(null)}
        onKeyDown={(e) => {
          if (
            open &&
            listKeys(e, nav, found.options, pick, () => {
              setDismissed(mention?.start ?? null);
              setMention(null);
            })
          )
            return;
          if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            onSubmit();
          } else if (e.key === "Escape") {
            e.preventDefault();
            e.stopPropagation();
            // A draft goes first; Esc on an empty box returns to Details.
            if (value) onChange("");
            else onEscapeEmpty();
          }
        }}
      />
      <button
        type="submit"
        className="inline-flex size-9 shrink-0 items-center justify-center rounded-control bg-c text-c-ink disabled:cursor-not-allowed disabled:opacity-40"
        aria-label="Send"
        disabled={!value.trim() || busy || disabled}
      >
        <Icon name="arrowUp" />
      </button>
      {open && (
        <div
          className="anim-fade-in absolute z-30 w-[min(380px,100%)] overflow-hidden rounded-xl border border-line-strong bg-surface shadow-pop"
          style={pos}
        >
          <div className="border-b border-line px-2.5 py-1.5 text-11-5 text-ink-3">
            Mention a resource or cluster
          </div>
          <ContextListbox
            id={listId}
            label="Mention"
            options={found.options}
            active={nav.active}
            onActive={nav.setActive}
            onPick={pick}
            searching={found.searching}
            current={current}
            empty={
              mention?.query ? `Nothing matches “${mention.query}”.` : "Type a name to search every cluster."
            }
          />
        </div>
      )}
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
            {...detailLink(link.cluster, link.ref, pending.attachment.kind === "yaml" ? "yaml" : "logs")}
            className="min-w-0 truncate font-mono text-ink no-underline hover:underline"
            title={`Open the ${pending.attachment.kind === "yaml" ? "YAML" : "logs"} of ${link.ref.name}`}
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
        {onRemove && <RemoveButton label="Remove the attached lines" onClick={onRemove} />}
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
