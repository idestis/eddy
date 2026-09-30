import { Link } from "@tanstack/react-router";
import type { Finding } from "../api/types";
import { ago, dateTime, thousands } from "../lib/format";
import { jobsLink } from "../lib/links";
import { Icon } from "./Icon";

const TOP_GROUPS = 5;

/** The key counts of a job-buildup finding, as [label, value, title?]. */
function jobFacts(f: Finding): Array<[string, string, string?]> {
  const j = f.jobs;
  if (!j) return [];
  const facts: Array<[string, string, string?]> = [
    ["Finished", thousands(j.finished)],
    ["Hidden", thousands(j.hidden)],
    ["Failed", thousands(j.failed)],
    ["Without TTL", thousands(j.withoutTTL)],
  ];
  if (j.oldest) facts.push(["Oldest", ago(j.oldest), dateTime(j.oldest)]);
  return facts;
}

/**
 * A warning finding above the Jobs list: what is wrong, what to do, the key counts and
 * the biggest groups. `link` points at the Jobs of the finding's namespace.
 */
export function FindingCallout({
  cluster,
  finding,
  link,
}: {
  cluster: string;
  finding: Finding;
  link?: boolean;
}) {
  const groups = finding.jobs?.groups?.slice(0, TOP_GROUPS) ?? [];
  return (
    <section
      aria-label={`Finding in ${finding.namespace}`}
      className="flex shrink-0 items-start gap-2.5 rounded-card border border-warn/40 bg-warn/12 px-3.5 py-3 text-12-5 text-ink-2"
    >
      <Icon name="alert" className="mt-px size-4 shrink-0 text-warn" />
      <div className="flex min-w-0 flex-1 flex-col gap-1.5">
        <p className="m-0 font-semibold text-ink">{finding.message}</p>
        {finding.recommendation && <p className="m-0">Recommendation: {finding.recommendation}</p>}
        {finding.jobs && (
          <dl className="m-0 flex flex-wrap gap-x-4 gap-y-1">
            {jobFacts(finding).map(([k, v, title]) => (
              <div key={k} className="flex gap-1.5">
                <dt className="text-ink-3">{k}</dt>
                <dd className="m-0 font-mono tabular-nums text-ink" title={title}>
                  {v}
                </dd>
              </div>
            ))}
          </dl>
        )}
        {groups.length > 0 && (
          <ul className="m-0 flex list-none flex-wrap gap-1.5 p-0" aria-label="Largest groups">
            {groups.map((g) => (
              <li
                key={`${g.by}/${g.name}`}
                className="rounded-md border border-line bg-surface px-1.5 py-0.5 font-mono text-12"
                title={`Grouped by ${g.by}${g.failed ? `, ${thousands(g.failed)} failed` : ""}`}
              >
                {g.label}: <span className="tabular-nums">{thousands(g.count)}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
      {link && (
        <Link {...jobsLink(cluster, finding.namespace)} className="btn btn-sm shrink-0">
          Jobs in {finding.namespace}
        </Link>
      )}
    </section>
  );
}
