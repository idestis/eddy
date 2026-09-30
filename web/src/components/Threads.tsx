import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useRef, useState } from "react";
import { isApiError } from "../api/client";
import { createThread, replyThread, setThreadStatus } from "../api/endpoints";
import { keys, threadQuery, threadsQuery } from "../api/queries";
import type { Author, Message, ResourceRef, Thread } from "../api/types";
import { ago } from "../lib/format";
import { displayKeys } from "../lib/keys";
import { detailLink } from "../lib/links";
import { AutoGrowTextarea } from "./AutoGrowTextarea";
import { Icon } from "./Icon";
import { Markdown } from "./Markdown";
import { Keys } from "./Status";
import { useToast } from "./Toasts";

const CLIENT_NAMES: Record<string, string> = {
  "claude-code": "Claude Code",
  "claude-desktop": "Claude Desktop",
  cursor: "Cursor",
};

/** Human-readable origin of a message: AI, or the MCP client a person used. */
export function AuthorBadges({ author }: { author: Author }) {
  if (author.type === "ai") {
    return (
      <>
        <span className="badge badge-ai">
          <Icon name="spark" />
          AI
        </span>
        {author.client && <span className="font-mono text-ink-3">{author.client}</span>}
      </>
    );
  }
  if (author.via === "mcp") {
    const client = author.client ? (CLIENT_NAMES[author.client] ?? author.client) : "MCP";
    return <span className="badge badge-accent">via {client}</span>;
  }
  if (author.type === "system") return <span className="badge">system</span>;
  return null;
}

function MessageItem({ m }: { m: Message }) {
  const ai = m.author.type === "ai";
  return (
    <li className="grid grid-cols-[28px_minmax(0,1fr)] gap-2.5">
      <span
        className={`flex size-7 items-center justify-center rounded-full text-12 font-semibold uppercase ${ai ? "bg-linear-135 from-c to-c2 text-c-ink" : "border border-line bg-surface-sunken"}`}
        aria-hidden="true"
      >
        {ai ? <Icon name="spark" className="size-3.5" /> : m.author.display.charAt(0)}
      </span>
      <div className="min-w-0">
        <div className="mb-[3px] flex flex-wrap items-center gap-1.5 text-12 text-ink-3">
          <b className="font-semibold text-ink">{ai ? "Ask AI" : m.author.display}</b>
          <AuthorBadges author={m.author} />
          <span title={m.createdAt}>{ago(m.createdAt)}</span>
        </div>
        <Markdown source={m.body} />
      </div>
    </li>
  );
}

export const targetLabel = (ref: ResourceRef): string =>
  ref.kind ? `${ref.kind}/${ref.namespace ? `${ref.namespace}/` : ""}${ref.name}` : `cluster ${ref.cluster}`;

function ThreadItem({ thread, showTarget }: { thread: Thread; showTarget?: boolean }) {
  const [open, setOpen] = useState(false);
  const [reply, setReply] = useState("");
  const qc = useQueryClient();
  const toast = useToast();
  const detail = useQuery({ ...threadQuery(thread.id), enabled: open });
  const bodyId = useId();

  const invalidate = () => qc.invalidateQueries({ queryKey: keys.threadsAll });
  const send = useMutation({
    mutationFn: () => replyThread(thread.id, reply.trim()),
    onSuccess: () => {
      setReply("");
      void invalidate();
    },
    onError: (err) => toast(`Couldn't reply: ${err.message}`, "bad"),
  });
  const status = useMutation({
    mutationFn: () => setThreadStatus(thread.id, thread.status === "open" ? "resolved" : "open"),
    onSuccess: () => void invalidate(),
    onError: (err) =>
      toast(
        isApiError(err, "forbidden")
          ? "Only the author or someone who can patch the target can resolve this."
          : err.message,
        "bad",
      ),
  });

  const submit = () => {
    if (reply.trim() && !send.isPending) send.mutate();
  };

  return (
    <article className="overflow-hidden rounded-[14px] border border-line bg-surface">
      <button
        type="button"
        className="flex w-full items-center gap-2.5 px-3.5 py-3 text-left hover:bg-surface-sunken"
        aria-expanded={open}
        aria-controls={bodyId}
        onClick={() => setOpen(!open)}
      >
        <Icon name={thread.status === "resolved" ? "check" : "chat"} />
        <span className="min-w-0 flex-1 truncate font-semibold">{thread.title}</span>
        {thread.status === "resolved" && <span className="badge text-ok">Resolved</span>}
        <AuthorBadges author={thread.createdBy} />
        <span className="text-12 whitespace-nowrap text-ink-3">
          {thread.createdBy.display} · {thread.messageCount} · {ago(thread.updatedAt)}
        </span>
      </button>
      {open && (
        <div className="flex flex-col gap-3 border-t border-line px-3.5 py-3" id={bodyId}>
          <div className="flex items-center gap-2 text-13 text-ink-3">
            {showTarget && (
              <span>
                On{" "}
                {thread.ref.kind ? (
                  <Link {...detailLink(thread.ref.cluster, thread.ref, "threads")} className="linkbtn">
                    {targetLabel(thread.ref)}
                  </Link>
                ) : (
                  <Link to="/c/$cluster" params={{ cluster: thread.ref.cluster }} className="linkbtn">
                    {thread.ref.cluster}
                  </Link>
                )}{" "}
                in {thread.ref.cluster}
              </span>
            )}
            <button
              type="button"
              className="btn btn-sm btn-ghost ml-auto"
              onClick={() => status.mutate()}
              disabled={status.isPending}
            >
              <Icon name={thread.status === "open" ? "check" : "chat"} />
              {thread.status === "open" ? "Resolve" : "Reopen"}
            </button>
          </div>
          {detail.isPending && <p className="text-ink-3">Loading…</p>}
          {detail.error && <p className="text-12-5 text-bad">{detail.error.message}</p>}
          {detail.data && (
            <ul className="flex flex-col gap-3">
              {detail.data.messages.map((m) => (
                <MessageItem key={m.id} m={m} />
              ))}
            </ul>
          )}
          <form
            className="ml-[38px] flex items-end gap-2 rounded-xl border border-line-strong bg-surface-sunken py-1 pr-1 pl-3 focus-within:border-c"
            onSubmit={(e) => {
              e.preventDefault();
              submit();
            }}
          >
            <AutoGrowTextarea
              aria-label={`Reply to ${thread.title}`}
              placeholder="Reply…"
              value={reply}
              maxHeight={200}
              className="min-h-6 flex-1 border-0 bg-transparent py-1.5 text-13-5 leading-[1.45] text-ink outline-none placeholder:text-ink-3"
              onChange={(e) => setReply(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  submit();
                }
              }}
            />
            <span className="hidden pb-2 text-11 whitespace-nowrap text-ink-3 sm:inline">
              <Keys keys={displayKeys("$mod+Enter")} />
            </span>
            <button
              type="submit"
              className="inline-flex size-8 shrink-0 items-center justify-center rounded-lg bg-c text-c-ink disabled:cursor-not-allowed disabled:opacity-40"
              aria-label="Send reply"
              title="Send (⌘/Ctrl+Enter)"
              disabled={!reply.trim() || send.isPending}
            >
              <Icon name="arrowUp" />
            </button>
          </form>
        </div>
      )}
    </article>
  );
}

export function ThreadList({ threads, showTarget }: { threads: Thread[]; showTarget?: boolean }) {
  return (
    <div className="flex flex-col gap-2.5">
      {threads.map((t) => (
        <ThreadItem key={t.id} thread={t} showTarget={showTarget} />
      ))}
    </div>
  );
}

function NewThread({ target, onDone }: { target: ResourceRef; onDone: () => void }) {
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const qc = useQueryClient();
  const toast = useToast();
  const titleRef = useRef<HTMLInputElement>(null);
  useEffect(() => titleRef.current?.focus(), []);
  const create = useMutation({
    mutationFn: () => createThread({ ref: target, title: title.trim(), body: body.trim() }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.threadsAll });
      toast("Thread created", "ok");
      onDone();
    },
    onError: (err) => toast(`Couldn't create the thread: ${err.message}`, "bad"),
  });
  return (
    <form
      className="flex flex-col gap-3.5 rounded-card border border-line bg-surface p-[18px]"
      onSubmit={(e) => {
        e.preventDefault();
        if (title.trim() && body.trim()) create.mutate();
      }}
    >
      <label className="field">
        Title
        <input ref={titleRef} value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} />
      </label>
      <label className="field">
        Message
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          placeholder="What should others know?"
        />
      </label>
      <div className="flex flex-wrap justify-end gap-2">
        <button type="button" className="btn btn-sm" onClick={onDone}>
          Cancel
        </button>
        <button
          type="submit"
          className="btn btn-sm btn-primary"
          disabled={!title.trim() || !body.trim() || create.isPending}
        >
          Start thread
        </button>
      </div>
    </form>
  );
}

/** Threads attached to one resource (detail page tab). */
export function ResourceThreads({
  target,
  compose,
  onCompose,
}: {
  target: ResourceRef;
  compose: boolean;
  onCompose: (open: boolean) => void;
}) {
  const { data, isPending, error } = useQuery(
    threadsQuery({
      cluster: target.cluster,
      kind: target.kind,
      namespace: target.namespace,
      name: target.name,
      type: "discussion",
    }),
  );
  return (
    <div className="flex max-w-[900px] flex-col gap-2.5">
      {compose ? (
        <NewThread target={target} onDone={() => onCompose(false)} />
      ) : (
        <div className="flex gap-2">
          <button type="button" className="btn btn-sm" onClick={() => onCompose(true)}>
            <Icon name="chat" />
            New thread <kbd>t</kbd>
          </button>
        </div>
      )}
      {isPending && <p className="text-ink-3">Loading threads…</p>}
      {error && <p className="text-12-5 text-bad">Couldn't load threads: {error.message}</p>}
      {data && data.items.length === 0 && !compose && (
        <p className="text-ink-3">No threads yet. Start one to leave context for the next person on call.</p>
      )}
      {data && <ThreadList threads={data.items} />}
    </div>
  );
}
