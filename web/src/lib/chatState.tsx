// The open Ask AI chat, kept at the app level (ADR-0007): moving between resources, clusters
// and pages keeps the same chat, its draft and its scroll position. Only the user switches
// chats (New chat, History, the palette). The open chat id and edited chips survive a reload
// of the tab (sessionStorage); the chats themselves are server state in TanStack Query.

import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useAskMutation } from "../api/chats";
import { isApiError } from "../api/client";
import type { AskRequest, ContextStatus, ResourceRef } from "../api/types";
import { useAppState } from "./appState";
import { contextKey } from "./chatContext";
import type { PendingAttachment } from "./logAttachments";

export type ChatView = "chat" | "history";

/** A question on its way, or one that failed. `chat` is the chat id, or "" for a new chat. */
export interface PendingAsk {
  chat: string;
  question: string;
  attachment?: PendingAttachment;
  error?: string;
}

export interface SendRequest {
  question: string;
  attachment?: PendingAttachment;
  /** The chips shown when the question is sent. */
  context: ResourceRef[];
  /** The chips differ from the stored chat's context, so the ask replaces it. */
  contextChanged: boolean;
}

interface ChatState {
  /** The open chat, or null for a new one that has not been asked yet. */
  chatId: string | null;
  /**
   * The chips after the user changed them and before the next ask. Null: the chat's stored
   * context, or for a new chat what is on screen.
   */
  edited: ResourceRef[] | null;
  setEdited: (refs: ResourceRef[] | null) => void;
  view: ChatView;
  setView: (v: ChatView) => void;
  /** Opens a chat in the Ask AI panel. */
  openChat: (id: string) => void;
  /** Starts a new chat in the Ask AI panel; it takes what is on screen. */
  newChat: () => void;
  /** Opens the chat list in the Ask AI panel. */
  openHistory: () => void;
  /** A chat was deleted or no longer exists: if it is open, start over. */
  forget: (id: string) => void;
  draft: string;
  setDraft: (s: string) => void;
  attachment: PendingAttachment | null;
  setAttachment: (a: PendingAttachment | null) => void;
  pending: PendingAsk | null;
  dismissError: () => void;
  send: (req: SendRequest) => Promise<void>;
  /** The hub's kill switch answered: chats stay readable, asking is off. */
  killed: boolean;
  /** What the last ask said about a context reference of a chat. */
  statusOf: (chatId: string, r: ResourceRef) => ContextStatus | undefined;
  /** The lines or YAML sent with the question an AI message answers (kept in this tab only). */
  attachmentFor: (aiMessageId: string) => PendingAttachment | undefined;
  /** The transcript's scroll offset per chat, so the panel re-opens where it was. */
  scroll: Map<string, number>;
}

const ChatContext = createContext<ChatState | null>(null);

export function useChatState(): ChatState {
  const s = useContext(ChatContext);
  if (!s) throw new Error("useChatState must be used inside <ChatProvider>");
  return s;
}

const STORAGE_KEY = "eddy.ai.chat";

interface Saved {
  chatId: string | null;
  edited: ResourceRef[] | null;
}

function load(): Saved {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    const v = raw ? (JSON.parse(raw) as Partial<Saved>) : {};
    return {
      chatId: typeof v.chatId === "string" ? v.chatId : null,
      edited: Array.isArray(v.edited) ? v.edited : null,
    };
  } catch {
    return { chatId: null, edited: null };
  }
}

function save(v: Saved) {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(v));
  } catch {
    // Private mode or storage full: the chat stays open until the tab reloads.
  }
}

export function askErrorText(err: unknown): string {
  if (isApiError(err, "disabled")) return "Ask AI is turned off on this hub. Your chats stay readable.";
  if (isApiError(err, "rate_limited"))
    return "You've asked a lot in the last hour. Wait a moment, then try again.";
  if (isApiError(err, "not_found")) return "This chat no longer exists. Start a new one.";
  if (isApiError(err, "bad_request")) return `The hub didn't accept the question: ${err.message}`;
  if (isApiError(err)) return `Couldn't get an answer: ${err.message}`;
  return "Couldn't get an answer. Try again.";
}

export function ChatProvider({ children }: { children: ReactNode }) {
  const { ask } = useAppState();
  const [initial] = useState(load);
  const [chatId, setChatId] = useState<string | null>(initial.chatId);
  const [edited, setEdited] = useState<ResourceRef[] | null>(initial.edited);
  const [view, setView] = useState<ChatView>("chat");
  const [draft, setDraft] = useState("");
  const [attachment, setAttachment] = useState<PendingAttachment | null>(null);
  const [pending, setPending] = useState<PendingAsk | null>(null);
  const [killed, setKilled] = useState(false);
  const [statuses, setStatuses] = useState<ReadonlyMap<string, Record<string, ContextStatus>>>(new Map());
  const attachments = useRef(new Map<string, PendingAttachment>());
  const scroll = useRef(new Map<string, number>()).current;
  const chatRef = useRef(chatId);
  chatRef.current = chatId;
  const busy = useRef(false);
  const mutation = useAskMutation();
  const askHub = mutation.mutateAsync;

  useEffect(() => save({ chatId, edited }), [chatId, edited]);

  const openChat = useCallback(
    (id: string) => {
      setChatId(id);
      setEdited(null);
      setView("chat");
      setPending((p) => (p?.error ? null : p));
      ask();
    },
    [ask],
  );

  const newChat = useCallback(() => {
    setChatId(null);
    setEdited(null);
    setView("chat");
    setDraft("");
    setAttachment(null);
    setPending((p) => (p?.error ? null : p));
    scroll.delete("");
    ask();
  }, [ask, scroll]);

  const openHistory = useCallback(() => {
    setView("history");
    ask();
  }, [ask]);

  const forget = useCallback((id: string) => {
    if (chatRef.current !== id) return;
    setChatId(null);
    setEdited(null);
  }, []);

  const send = useCallback(
    async ({ question, attachment: att, context, contextChanged }: SendRequest) => {
      if (busy.current) return;
      busy.current = true;
      const id = chatRef.current;
      setPending({ chat: id ?? "", question, attachment: att });
      const req: AskRequest = { question };
      if (id) req.chatId = id;
      // A new chat starts with its chips; an open one sends them only once they changed.
      if (id ? contextChanged : context.length > 0) req.context = context;
      if (att) req.attachments = [att.attachment];
      try {
        const res = await askHub(req);
        if (att) attachments.current.set(res.message.id, att);
        setStatuses((prev) =>
          new Map(prev).set(
            res.chat.id,
            Object.fromEntries(res.chat.context.map((r, i) => [contextKey(r), res.contextStatus[i] ?? "ok"])),
          ),
        );
        // The user may have switched chats while waiting; then the answer just lands in its chat.
        if (chatRef.current === id) {
          setChatId(res.chat.id);
          setEdited(null);
        }
        setPending(null);
      } catch (err) {
        if (isApiError(err, "disabled")) setKilled(true);
        setPending({ chat: id ?? "", question, attachment: att, error: askErrorText(err) });
      } finally {
        busy.current = false;
      }
    },
    [askHub],
  );

  const dismissError = useCallback(() => setPending((p) => (p?.error ? null : p)), []);
  const statusOf = useCallback((id: string, r: ResourceRef) => statuses.get(id)?.[contextKey(r)], [statuses]);
  const attachmentFor = useCallback((id: string) => attachments.current.get(id), []);

  const value = useMemo<ChatState>(
    () => ({
      chatId,
      edited,
      setEdited,
      view,
      setView,
      openChat,
      newChat,
      openHistory,
      forget,
      draft,
      setDraft,
      attachment,
      setAttachment,
      pending,
      dismissError,
      send,
      killed,
      statusOf,
      attachmentFor,
      scroll,
    }),
    [
      chatId,
      edited,
      view,
      openChat,
      newChat,
      openHistory,
      forget,
      draft,
      attachment,
      pending,
      dismissError,
      send,
      killed,
      statusOf,
      attachmentFor,
      scroll,
    ],
  );

  return <ChatContext.Provider value={value}>{children}</ChatContext.Provider>;
}
