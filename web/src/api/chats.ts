// Ask AI chats (ADR-0007, docs/api.md "Ask AI"): query options, hooks and the cache
// updates of every chat mutation. A chat belongs to the signed-in user, so the list and
// the open chat stay in the query cache while the user moves around the app.

import {
  type InfiniteData,
  infiniteQueryOptions,
  type QueryClient,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { useEffect, useMemo } from "react";
import * as api from "./endpoints";
import { keys } from "./queries";
import type { AskRequest, AskResponse, Chat, ChatDetail, Message, Page, ResourceRef } from "./types";

/** Messages per request: a chat holds at most 1000, so two pages at most. */
const MESSAGE_PAGE = 500;
const STALE_MS = 30_000;

export const chatsQuery = infiniteQueryOptions({
  queryKey: keys.chatList,
  queryFn: ({ pageParam }) => api.listChats({ cursor: pageParam || undefined }),
  initialPageParam: "",
  getNextPageParam: (last: Page<Chat>) => last.next || undefined,
  staleTime: STALE_MS,
});

export const chatQuery = (id: string) =>
  infiniteQueryOptions({
    queryKey: keys.chat(id),
    queryFn: ({ pageParam }) => api.getChat(id, { cursor: pageParam || undefined, limit: MESSAGE_PAGE }),
    initialPageParam: "",
    getNextPageParam: (last: ChatDetail) => last.next || undefined,
    staleTime: STALE_MS,
  });

type ChatList = InfiniteData<Page<Chat>, string>;
type ChatPages = InfiniteData<ChatDetail, string>;

/** The signed-in user's chats, newest first, a page at a time. */
export function useChats(enabled = true) {
  const q = useInfiniteQuery({ ...chatsQuery, enabled });
  const items = useMemo(() => {
    const seen = new Set<string>();
    return (q.data?.pages ?? []).flatMap((p) =>
      p.items.filter((c) => !seen.has(c.id) && Boolean(seen.add(c.id))),
    );
  }, [q.data]);
  return { ...q, items };
}

/**
 * One chat and all of its messages, oldest first. Pages come oldest first, so the newest
 * messages are on the last page: the hook follows `next` until it has them all.
 */
export function useChat(id: string | null) {
  const q = useInfiniteQuery({ ...chatQuery(id ?? ""), enabled: Boolean(id) });
  const { hasNextPage, isFetchingNextPage, isError, fetchNextPage } = q;
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage && !isError) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, isError, fetchNextPage]);
  const pages = id ? q.data?.pages : undefined;
  const messages = useMemo(() => (pages ?? []).flatMap((p) => p.messages), [pages]);
  return {
    chat: pages?.[pages.length - 1]?.chat,
    messages,
    isPending: Boolean(id) && q.isPending,
    loadingRest: Boolean(hasNextPage),
    error: id ? q.error : null,
  };
}

// Cache updates, shared by the mutations below.

/** Puts a chat first in the list (it was just created or asked in). */
function bumpInList(qc: QueryClient, chat: Chat) {
  qc.setQueryData<ChatList>(keys.chatList, (d) => {
    if (!d) return d;
    const pages = d.pages.map((p) => ({ ...p, items: p.items.filter((c) => c.id !== chat.id) }));
    const [first, ...rest] = pages;
    return first ? { ...d, pages: [{ ...first, items: [chat, ...first.items] }, ...rest] } : d;
  });
}

/** Replaces a chat in the list where it is (a rename does not reorder). */
function replaceInList(qc: QueryClient, chat: Chat) {
  qc.setQueryData<ChatList>(keys.chatList, (d) =>
    d
      ? {
          ...d,
          pages: d.pages.map((p) => ({ ...p, items: p.items.map((c) => (c.id === chat.id ? chat : c)) })),
        }
      : d,
  );
}

/** Sets the chat of a cached detail and appends messages to its last page. */
function patchDetail(qc: QueryClient, chat: Chat, append: Message[] = []) {
  qc.setQueryData<ChatPages>(keys.chat(chat.id), (d) => {
    if (!d) return { pages: [{ chat, messages: append }], pageParams: [""] };
    const last = d.pages.length - 1;
    return {
      ...d,
      pages: d.pages.map((p, i) => ({
        ...p,
        chat,
        messages: i === last ? [...p.messages, ...append] : p.messages,
      })),
    };
  });
}

export function useCreateChat() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (context: ResourceRef[]) => api.createChat({ context }),
    onSuccess: (chat) => {
      bumpInList(qc, chat);
      patchDetail(qc, chat);
    },
  });
}

/** Renames a chat, or replaces its context. */
export function usePatchChat() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...body }: { id: string; title?: string; context?: ResourceRef[] }) =>
      api.updateChat(id, body),
    onSuccess: (chat) => {
      replaceInList(qc, chat);
      patchDetail(qc, chat);
    },
  });
}

export function useDeleteChat() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteChat(id),
    onSuccess: (_, id) => {
      qc.setQueryData<ChatList>(keys.chatList, (d) =>
        d ? { ...d, pages: d.pages.map((p) => ({ ...p, items: p.items.filter((c) => c.id !== id) })) } : d,
      );
      qc.removeQueries({ queryKey: keys.chat(id) });
    },
  });
}

/**
 * The asked question as the hub stored it. The ask response carries only the answer, so
 * the question is added locally until the chat is next fetched.
 */
function questionMessage(res: AskResponse, question: string): Message {
  return {
    id: `local-${res.message.id}`,
    threadId: res.chat.id,
    author: { type: "human", subject: res.chat.owner, display: res.chat.owner, via: "askai" },
    body: question,
    createdAt: res.message.createdAt,
  };
}

/** Applies an ask to the cache: the chat moves first, the question and the answer are appended. */
export function applyAsk(qc: QueryClient, res: AskResponse, req: AskRequest) {
  bumpInList(qc, res.chat);
  patchDetail(qc, res.chat, [questionMessage(res, req.question), res.message]);
}

export function useAskMutation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: AskRequest) => api.askAI(req),
    onSuccess: (res, req) => applyAsk(qc, res, req),
  });
}
