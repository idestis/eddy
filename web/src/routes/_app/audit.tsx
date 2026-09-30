import { useInfiniteQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { auditQuery } from "../../api/queries";
import type { AuditEvent } from "../../api/types";
import { Screen } from "../../components/Screen";
import { targetLabel } from "../../components/Threads";
import { dateTime } from "../../lib/format";
import { detailLink } from "../../lib/links";

export const Route = createFileRoute("/_app/audit")({
  component: AuditPage,
});

const RESULT_CLASS: Record<AuditEvent["result"], string> = {
  ok: "badge ok",
  denied: "badge bad",
  error: "badge bad",
};

function Target({ e }: { e: AuditEvent }) {
  const t = e.target;
  if (!t?.cluster) return <span className="muted">–</span>;
  if (!t.kind) return <span className="mono">{t.cluster}</span>;
  return (
    <Link {...detailLink(t.cluster, t)} className="mono">
      {t.cluster}/{targetLabel(t)}
    </Link>
  );
}

function AuditPage() {
  const query = useInfiniteQuery(auditQuery);
  const events = query.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <Screen crumbs={["Audit log"]}>
      <h1>Audit log</h1>
      <p className="page-sub">
        Everything you did through Eddy: in the browser, over MCP and through Ask AI.
      </p>
      {query.isPending && <p className="muted">Loading…</p>}
      {query.error && <p className="error-text">Couldn't load the audit log: {query.error.message}</p>}
      {query.data && events.length === 0 && <div className="empty">No events yet.</div>}
      {events.length > 0 && (
        <table className="dtable">
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
                <td className="muted">{dateTime(e.ts)}</td>
                <td className="mono">{e.action}</td>
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
        </table>
      )}
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
