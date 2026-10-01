// The signed-in user's Ask AI chats, newest first, in place of the open chat. Open, rename
// inline, delete after an inline confirmation. Nobody else can list them (ADR-0007).

import { useState } from "react";
import { useChats, useDeleteChat, usePatchChat } from "../api/chats";
import { isApiError } from "../api/client";
import type { Chat } from "../api/types";
import { isClusterRef } from "../lib/chatContext";
import { useChatState } from "../lib/chatState";
import { ago, plural } from "../lib/format";
import { ClusterTag, KindBadge } from "./ContextChips";
import { Icon } from "./Icon";
import { Spinner } from "./Status";
import { useToast } from "./Toasts";

export const TITLE_MAX = 120;

/** The first context entry, small: what the chat started about. */
function FirstRef({ chat }: { chat: Chat }) {
  const r = chat.context[0];
  if (!r) return null;
  const more = chat.context.length - 1;
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5 text-11-5 text-ink-3">
      {isClusterRef(r) ? (
        <ClusterTag name={r.cluster} />
      ) : (
        <>
          <KindBadge kind={r.kind} />
          <span className="min-w-0 truncate font-mono text-ink-2">{r.name}</span>
        </>
      )}
      {more > 0 && <span className="shrink-0">+{more}</span>}
    </span>
  );
}

/** An inline title editor: Enter saves, Esc cancels, an empty title is not saved. */
export function TitleInput({
  value,
  label,
  onSave,
  onCancel,
  className = "",
}: {
  value: string;
  label: string;
  onSave: (title: string) => void;
  onCancel: () => void;
  className?: string;
}) {
  const [text, setText] = useState(value);
  const commit = () => {
    const t = text.trim();
    if (t && t !== value) onSave(t.slice(0, TITLE_MAX));
    else onCancel();
  };
  return (
    <input
      className={`input min-h-0! py-1! ${className}`}
      aria-label={label}
      value={text}
      maxLength={TITLE_MAX}
      onChange={(e) => setText(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          commit();
        } else if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          onCancel();
        }
      }}
      // biome-ignore lint/a11y/noAutofocus: rename opens to type into
      autoFocus
    />
  );
}

function ChatRow({ chat, open }: { chat: Chat; open: boolean }) {
  const { openChat, forget } = useChatState();
  const toast = useToast();
  const patch = usePatchChat();
  const del = useDeleteChat();
  const [mode, setMode] = useState<"view" | "rename" | "delete">("view");

  const rename = (title: string) => {
    setMode("view");
    patch.mutate(
      { id: chat.id, title },
      {
        onError: (err) =>
          toast(`Couldn't rename the chat: ${isApiError(err) ? err.message : "try again"}`, "bad"),
      },
    );
  };
  const remove = () =>
    del.mutate(chat.id, {
      onSuccess: () => {
        forget(chat.id);
        toast("Chat deleted", "ok");
      },
      onError: (err) => {
        setMode("view");
        toast(`Couldn't delete the chat: ${isApiError(err) ? err.message : "try again"}`, "bad");
      },
    });

  return (
    <li
      className={`group flex min-w-0 items-center gap-1 rounded-tile border px-1 py-1 ${open ? "border-c/35 bg-c-soft" : "border-transparent hover:bg-surface-sunken"}`}
    >
      {mode === "rename" ? (
        <div className="flex min-w-0 flex-1 px-1.5 py-1">
          <TitleInput
            value={chat.title}
            label="Chat title"
            className="text-13"
            onSave={rename}
            onCancel={() => setMode("view")}
          />
        </div>
      ) : mode === "delete" ? (
        <div className="flex min-w-0 flex-1 items-center gap-2 px-2 py-1.5 text-12-5" role="alert">
          <span className="min-w-0 flex-1 truncate">
            Delete <b className="font-semibold">{chat.title || "this chat"}</b>?
          </span>
          <button type="button" className="btn btn-sm btn-ghost" onClick={() => setMode("view")}>
            Cancel
          </button>
          <button type="button" className="btn btn-sm btn-danger" disabled={del.isPending} onClick={remove}>
            {del.isPending ? <Spinner /> : null}
            Delete
          </button>
        </div>
      ) : (
        <>
          <button
            type="button"
            className="flex min-w-0 flex-1 flex-col items-start gap-1 rounded-lg px-2 py-1.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-c"
            aria-current={open || undefined}
            onClick={() => openChat(chat.id)}
          >
            <span className="w-full truncate text-13 font-medium">{chat.title || "Untitled chat"}</span>
            <span className="flex w-full min-w-0 items-center gap-2 text-11-5 text-ink-3">
              <span className="shrink-0">
                {ago(chat.updatedAt)} · {plural(chat.messageCount, "message")}
              </span>
              <FirstRef chat={chat} />
            </span>
          </button>
          <button
            type="button"
            className="ib size-8! opacity-70 group-hover:opacity-100 focus-visible:opacity-100"
            aria-label={`Rename ${chat.title || "chat"}`}
            title="Rename"
            onClick={() => setMode("rename")}
          >
            <Icon name="pencil" className="size-3.5" />
          </button>
          <button
            type="button"
            className="ib size-8! opacity-70 group-hover:opacity-100 hover:text-bad! focus-visible:opacity-100"
            aria-label={`Delete ${chat.title || "chat"}`}
            title="Delete"
            onClick={() => setMode("delete")}
          >
            <Icon name="trash" className="size-3.5" />
          </button>
        </>
      )}
    </li>
  );
}

export function ChatHistory() {
  const { chatId } = useChatState();
  const { items, isPending, isError, hasNextPage, fetchNextPage, isFetchingNextPage, refetch } = useChats();

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="min-h-0 flex-1 overflow-auto p-2.5">
        {isPending ? (
          <div className="flex flex-col gap-2 p-1.5" aria-busy="true">
            {[0, 1, 2].map((i) => (
              <span key={i} className="skeleton block h-12 rounded-lg" aria-hidden="true" />
            ))}
          </div>
        ) : isError ? (
          <div className="flex flex-col items-start gap-2 p-3 text-13 text-bad">
            Couldn't load your chats.
            <button type="button" className="btn btn-sm" onClick={() => void refetch()}>
              Try again
            </button>
          </div>
        ) : items.length === 0 ? (
          <div className="p-4 text-13 text-ink-3">No chats yet. Ask a question to start one.</div>
        ) : (
          <ul className="flex flex-col gap-0.5" aria-label="Your chats">
            {items.map((c) => (
              <ChatRow key={c.id} chat={c} open={c.id === chatId} />
            ))}
          </ul>
        )}
        {hasNextPage && (
          <button
            type="button"
            className="btn btn-sm btn-ghost mt-1.5 w-full justify-center"
            disabled={isFetchingNextPage}
            onClick={() => void fetchNextPage()}
          >
            {isFetchingNextPage ? <Spinner /> : null}
            Show older chats
          </button>
        )}
      </div>
      <div className="shrink-0 border-t border-line px-4 py-2.5 text-11-5 text-ink-3">
        Chats are kept for 30 days. Only you can see them.
      </div>
    </div>
  );
}
