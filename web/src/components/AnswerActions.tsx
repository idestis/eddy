// The row under each AI answer: save it as a discussion thread on one of the chat's context
// entries (the first by default, picked when there are several), and copy a ready prompt that
// hands that thread to Claude Code over MCP.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { isApiError } from "../api/client";
import { createThread } from "../api/endpoints";
import { keys, tokensQuery } from "../api/queries";
import type { Message, ResourceRef } from "../api/types";
import { claudeCodePrompt, threadBody, threadTitle } from "../lib/answerThread";
import { useAppState } from "../lib/appState";
import { contextKey, isClusterRef } from "../lib/chatContext";
import { kindInfo } from "../lib/kinds";
import { detailLink } from "../lib/links";
import type { PendingAttachment } from "../lib/logAttachments";
import { Icon } from "./Icon";
import { Select } from "./Select";
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
  targets,
}: {
  message: Message;
  provider?: string;
  question: string;
  attachment?: PendingAttachment;
  /** Where the thread can go: the chat's context entries the user can still see. */
  targets: readonly ResourceRef[];
}) {
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { setPane } = useAppState();
  const [saved, setSaved] = useState<{ id: string; target: ResourceRef }>();
  const [hint, setHint] = useState(false);
  const [picked, setPicked] = useState<string>();
  const target = (saved?.target ?? targets.find((t) => contextKey(t) === picked) ?? targets[0]) as
    | ResourceRef
    | undefined;
  const threadId = saved?.id;

  const open = (id: string, t: ResourceRef) => {
    setPane("details");
    if (isClusterRef(t)) {
      void navigate({ to: "/threads" });
      return;
    }
    const d = detailLink(t.cluster, t, "threads");
    void navigate({ ...d, search: { ...d.search, thread: id } });
  };

  const save = useMutation({
    mutationFn: async (ref: ResourceRef) => {
      const res = await createThread({
        ref,
        title: threadTitle(question),
        body: threadBody({
          question,
          answer: message.body,
          model: message.author.client,
          provider,
          attachment,
        }),
      });
      return { id: res.thread.id, target: ref };
    },
    onSuccess: (s) => {
      setSaved(s);
      void qc.invalidateQueries({ queryKey: keys.threadsAll });
    },
    onError: (err) => toast(saveError(err), "bad"),
  });

  if (!target) return null;

  const saveClick = async () => {
    if (threadId || save.isPending) return;
    const s = await save.mutateAsync(target).catch(() => undefined);
    if (s) toast("Saved as thread", "ok", { action: { label: "Open", run: () => open(s.id, s.target) } });
  };

  const copyClick = async () => {
    if (save.isPending) return;
    const id = threadId ?? (await save.mutateAsync(target).catch(() => undefined))?.id;
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
        {targets.length > 1 && !threadId && (
          <Select
            label="Save the thread on"
            value={contextKey(target)}
            onChange={setPicked}
            options={targets.map((t) => ({
              value: contextKey(t),
              label: isClusterRef(t) ? t.cluster : `${kindInfo(t.kind).abbr} ${t.name}`,
              text: isClusterRef(t) ? `the cluster ${t.cluster}` : `${t.kind} ${t.name}`,
              meta: isClusterRef(t) ? "cluster" : t.cluster,
            }))}
            renderTrigger={({ ref, props }) => (
              <button ref={ref} {...props} className={`${BTN} border-dashed`} title="Save the thread on">
                on{" "}
                <span className="max-w-40 truncate font-mono">
                  {isClusterRef(target) ? target.cluster : target.name}
                </span>
                <Icon name="updown" className="size-3" />
              </button>
            )}
          />
        )}
        {threadId && saved && (
          <button
            type="button"
            className={`${BTN} border-transparent`}
            onClick={() => open(threadId, saved.target)}
          >
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
