import { useInfiniteQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { z } from "zod";
import { threadsInfiniteQuery } from "../../api/queries";
import { Empty } from "../../components/Empty";
import { PageHead } from "../../components/PageHead";
import { Screen } from "../../components/Screen";
import { SEG, SEG_BTN } from "../../components/SidePanel";
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
    <Screen crumbs={["Threads"]} title="threads" width="narrow">
      <PageHead title="Threads">
        Discussions on resources and clusters across the fleet, including notes left from Claude Code.
      </PageHead>
      <fieldset className={`${SEG} self-start`}>
        <legend className="sr-only">Status</legend>
        {(["open", "resolved"] as const).map((s) => (
          <button
            type="button"
            key={s}
            className={`${SEG_BTN} px-4`}
            aria-pressed={status === s}
            onClick={() => void navigate({ search: { status: s === "open" ? undefined : s } })}
          >
            {s === "open" ? "Open" : "Resolved"}
          </button>
        ))}
      </fieldset>
      {query.isPending && <p className="text-ink-3">Loading threads…</p>}
      {query.error && <p className="text-12-5 text-bad">Couldn't load threads: {query.error.message}</p>}
      {query.data && threads.length === 0 && (
        <Empty title={`No ${status} threads`}>Press t on any resource to start one.</Empty>
      )}
      <ThreadList threads={threads} showTarget />
      {query.hasNextPage && (
        <button
          type="button"
          className="btn self-start"
          onClick={() => void query.fetchNextPage()}
          disabled={query.isFetchingNextPage}
        >
          Load more
        </button>
      )}
    </Screen>
  );
}
