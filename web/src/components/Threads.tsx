import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useRef, useState } from "react";
import { isApiError } from "../api/client";
import { createThread, replyThread, setThreadStatus } from "../api/endpoints";
import { keys, threadQuery, threadsQuery } from "../api/queries";
import type { Author, Message, ResourceRef, Thread } from "../api/types";
import { ago } from "../lib/format";
import { detailLink } from "../lib/links";
import { Icon } from "./Icon";
import { Markdown } from "./Markdown";
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
        {author.client && <span className="muted mono">{author.client}</span>}
      </>
    );
  }
  if (author.via === "mcp") {
    const client = author.client ? (CLIENT_NAMES[author.client] ?? author.client) : "MCP";
    return <span className="badge accent">via {client}</span>;
  }
  if (author.type === "system") return <span className="badge">system</span>;
  return null;
}

function MessageItem({ m }: { m: Message }) {
  const ai = m.author.type === "ai";
  return (
    <li className={`tmsg${ai ? " ai" : ""}`}>
      <span className="av" aria-hidden="true">
        {ai ? <Icon name="spark" /> : m.author.display.charAt(0)}
      </span>
      <div>
        <div className="mh">
          <b>{ai ? "Ask AI" : m.author.display}</b>
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

  return (
    <article className="thread">
      <button
        type="button"
        className="thread-head"
        aria-expanded={open}
        aria-controls={bodyId}
        onClick={() => setOpen(!open)}
      >
        <Icon name={thread.status === "resolved" ? "check" : "chat"} />
        <span className="tt">{thread.title}</span>
        {thread.status === "resolved" && <span className="badge ok">Resolved</span>}
        <AuthorBadges author={thread.createdBy} />
        <span className="tm">
          {thread.createdBy.display} · {thread.messageCount} · {ago(thread.updatedAt)}
        </span>
      </button>
      {open && (
        <div className="thread-body" id={bodyId}>
          {showTarget && (
            <div className="muted">
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
            </div>
          )}
          {detail.isPending && <p className="muted">Loading…</p>}
          {detail.error && <p className="error-text">{detail.error.message}</p>}
          {detail.data && (
            <ul className="msgs">
              {detail.data.messages.map((m) => (
                <MessageItem key={m.id} m={m} />
              ))}
            </ul>
          )}
          <form
            className="reply"
            onSubmit={(e) => {
              e.preventDefault();
              if (reply.trim()) send.mutate();
            }}
          >
            <textarea
              aria-label={`Reply to ${thread.title}`}
              placeholder="Reply… (Markdown: `code`, **bold**, lists, links)"
              value={reply}
              onChange={(e) => setReply(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && reply.trim()) send.mutate();
              }}
            />
            <div className="row-btns">
              <button
                type="button"
                className="btn sm"
                onClick={() => status.mutate()}
                disabled={status.isPending}
              >
                {thread.status === "open" ? "Resolve" : "Reopen"}
              </button>
              <button type="submit" className="btn sm primary" disabled={!reply.trim() || send.isPending}>
                Reply
              </button>
            </div>
          </form>
        </div>
      )}
    </article>
  );
}

export function ThreadList({ threads, showTarget }: { threads: Thread[]; showTarget?: boolean }) {
  return (
    <div className="threads">
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
      className="panel"
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
          placeholder="What should others know? Markdown is supported; HTML and images are not."
        />
      </label>
      <div className="row-btns">
        <button type="button" className="btn sm" onClick={onDone}>
          Cancel
        </button>
        <button
          type="submit"
          className="btn sm primary"
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
    <div className="threads">
      {compose ? (
        <NewThread target={target} onDone={() => onCompose(false)} />
      ) : (
        <div className="row-btns">
          <button type="button" className="btn sm" onClick={() => onCompose(true)}>
            <Icon name="chat" />
            New thread <kbd>t</kbd>
          </button>
        </div>
      )}
      {isPending && <p className="muted">Loading threads…</p>}
      {error && <p className="error-text">Couldn't load threads: {error.message}</p>}
      {data && data.items.length === 0 && !compose && (
        <p className="muted">No threads yet. Start one to leave context for the next person on call.</p>
      )}
      {data && <ThreadList threads={data.items} />}
    </div>
  );
}
