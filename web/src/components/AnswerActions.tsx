// The row under each AI answer: save it as a discussion thread on the same object, and copy
// a ready prompt that hands that thread to Claude Code over MCP.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { isApiError } from "../api/client";
import { createThread } from "../api/endpoints";
import { keys, tokensQuery } from "../api/queries";
import type { Message, ResourceRef } from "../api/types";
import { claudeCodePrompt, threadBody, threadTitle } from "../lib/answerThread";
import { useAppState } from "../lib/appState";
import { detailLink } from "../lib/links";
import type { PendingAttachment } from "../lib/logAttachments";
import { Icon } from "./Icon";
import { Spinner } from "./Status";
import { useToast } from "./Toasts";

const HINT_KEY = "eddy.mcpHintShown";

function hintShown(): boolean {
  try {
    return localStorage.getItem(HINT_KEY) === "1";
  } catch {
    return false;
  }
}
function rememberHint() {
  try {
    localStorage.setItem(HINT_KEY, "1");
  } catch {
    // Private mode: the hint may show again.
  }
}

const BTN =
  "inline-flex h-7 items-center gap-1.5 rounded-full border border-line bg-surface px-2.5 text-12 text-ink-2 hover:border-line-strong disabled:cursor-not-allowed disabled:opacity-60";

function saveError(err: unknown): string {
  if (isApiError(err, "forbidden")) return "You can't start a thread on this object.";
  if (isApiError(err) && err.status === 409) return "A thread for this answer already exists.";
  return `Couldn't save the thread: ${isApiError(err) ? err.message : "try again"}`;
}

export function AnswerActions({
  message,
  provider,
  question,
  attachment,
  target,
}: {
  message: Message;
  provider?: string;
  question: string;
  attachment?: PendingAttachment;
  target: ResourceRef;
}) {
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { setPane } = useAppState();
  const [threadId, setThreadId] = useState<string>();
  const [hint, setHint] = useState(false);

  const open = (id: string) => {
    setPane("details");
    const d = detailLink(target.cluster, target, "threads");
    void navigate({ ...d, search: { ...d.search, thread: id } });
  };

  const save = useMutation({
    mutationFn: async () => {
      const res = await createThread({
        ref: target,
        title: threadTitle(question),
        body: threadBody({
          question,
          answer: message.body,
          model: message.author.client,
          provider,
          attachment,
        }),
      });
      return res.thread.id;
    },
    onSuccess: (id) => {
      setThreadId(id);
      void qc.invalidateQueries({ queryKey: keys.threadsAll });
    },
    onError: (err) => toast(saveError(err), "bad"),
  });

  const saveClick = async () => {
    if (threadId || save.isPending) return;
    const id = await save.mutateAsync().catch(() => undefined);
    if (id) toast("Saved as thread", "ok", { action: { label: "Open", run: () => open(id) } });
  };

  const copyClick = async () => {
    if (save.isPending) return;
    const id = threadId ?? (await save.mutateAsync().catch(() => undefined));
    if (!id) return;
    try {
      await navigator.clipboard.writeText(claudeCodePrompt(id, target.cluster, target));
    } catch {
      toast("Couldn't copy to the clipboard.", "bad");
      return;
    }
    toast("Copied — paste into Claude Code", "ok");
    if (!hintShown()) {
      rememberHint();
      const tokens = await qc.fetchQuery(tokensQuery).catch(() => undefined);
      if (tokens && tokens.length === 0) setHint(true);
    }
  };

  return (
    <div className="mt-2 flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-1.5">
        <button
          type="button"
          className={BTN}
          disabled={save.isPending || Boolean(threadId)}
          onClick={saveClick}
        >
          {save.isPending && !threadId ? (
            <Spinner />
          ) : (
            <Icon name={threadId ? "check" : "chat"} className="size-3.5" />
          )}
          {threadId ? "Saved" : "Save as thread"}
        </button>
        <button type="button" className={BTN} disabled={save.isPending} onClick={copyClick}>
          <Icon name="copy" className="size-3.5" />
          Copy for Claude Code
        </button>
        {threadId && (
          <button type="button" className={`${BTN} border-transparent`} onClick={() => open(threadId)}>
            Open thread
          </button>
        )}
      </div>
      {hint && (
        <p className="text-12 text-ink-3" role="status">
          You have no access token yet.{" "}
          <Link to="/settings/tokens" className="text-c">
            Connect Claude Code
          </Link>{" "}
          to let it read this thread.
        </p>
      )}
    </div>
  );
}
