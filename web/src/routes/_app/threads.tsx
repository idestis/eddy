import { useInfiniteQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { z } from "zod";
import { threadsInfiniteQuery } from "../../api/queries";
import { Screen } from "../../components/Screen";
import { ThreadList } from "../../components/Threads";

export const Route = createFileRoute("/_app/threads")({
  validateSearch: z.object({ status: z.enum(["open", "resolved"]).optional().catch(undefined) }),
  component: ThreadsPage,
});

function ThreadsPage() {
  const { status = "open" } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const query = useInfiniteQuery(threadsInfiniteQuery({ status, type: "discussion", limit: 50 }));
  const threads = query.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <Screen crumbs={["Threads"]}>
      <h1>Threads</h1>
      <p className="page-sub">
        Discussions on resources and clusters across the fleet, including notes left from Claude Code.
      </p>
      <fieldset className="chips">
        <legend className="sr-only">Status</legend>
        {(["open", "resolved"] as const).map((s) => (
          <button
            type="button"
            key={s}
            className="chip"
            aria-pressed={status === s}
            onClick={() => void navigate({ search: { status: s === "open" ? undefined : s } })}
          >
            {s === "open" ? "Open" : "Resolved"}
          </button>
        ))}
      </fieldset>
      {query.isPending && <p className="muted">Loading threads…</p>}
      {query.error && <p className="error-text">Couldn't load threads: {query.error.message}</p>}
      {query.data && threads.length === 0 && (
        <div className="empty">
          <strong>No {status} threads</strong>
          Press t on any resource to start one.
        </div>
      )}
      <ThreadList threads={threads} showTarget />
      {query.hasNextPage && (
        <button
          type="button"
          className="btn"
          onClick={() => void query.fetchNextPage()}
          disabled={query.isFetchingNextPage}
        >
          Load more
        </button>
      )}
    </Screen>
  );
}
