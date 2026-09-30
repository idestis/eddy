import { useInfiniteQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { auditQuery } from "../../api/queries";
import type { AuditEvent } from "../../api/types";
import { DataTable } from "../../components/DataTable";
import { Empty } from "../../components/Empty";
import { PageHead } from "../../components/PageHead";
import { Screen } from "../../components/Screen";
import { targetLabel } from "../../components/Threads";
import { dateTime } from "../../lib/format";
import { detailLink } from "../../lib/links";

export const Route = createFileRoute("/_app/audit")({
  component: AuditPage,
});

const RESULT_CLASS: Record<AuditEvent["result"], string> = {
  ok: "badge text-ok",
  denied: "badge text-bad",
  error: "badge text-bad",
};

function Target({ e }: { e: AuditEvent }) {
  const t = e.target;
  if (!t?.cluster) return <span className="text-ink-3">–</span>;
  if (!t.kind) return <span className="font-mono text-12-5">{t.cluster}</span>;
  return (
    <Link {...detailLink(t.cluster, t)} className="font-mono text-12-5 no-underline hover:underline">
      {t.cluster}/{targetLabel(t)}
    </Link>
  );
}

function AuditPage() {
  const query = useInfiniteQuery(auditQuery);
  const events = query.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <Screen crumbs={["Audit log"]} title="audit log" width="wide">
      <PageHead title="Audit log">
        Everything you did through Eddy: in the browser, over MCP and through Ask AI.
      </PageHead>
      {query.isPending && <p className="text-ink-3">Loading…</p>}
      {query.error && (
        <p className="text-12-5 text-bad">Couldn't load the audit log: {query.error.message}</p>
      )}
      {query.data && events.length === 0 && <Empty>No events yet.</Empty>}
      {events.length > 0 && (
        <DataTable>
          <thead>
            <tr>
              <th>Time</th>
              <th>Action</th>
              <th>Target</th>
              <th>Via</th>
              <th>Result</th>
            </tr>
          </thead>
          <tbody>
            {events.map((e) => (
              <tr key={e.id}>
                <td className="whitespace-nowrap text-ink-3">{dateTime(e.ts)}</td>
                <td className="font-mono text-12-5">{e.action}</td>
                <td>
                  <Target e={e} />
                </td>
                <td>
                  <span className="badge">{e.via}</span>
                </td>
                <td>
                  <span className={RESULT_CLASS[e.result]}>{e.result}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </DataTable>
      )}
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
