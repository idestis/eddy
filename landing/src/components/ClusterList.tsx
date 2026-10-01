import { EnvLegend } from "./EnvLegend";

const clusters = [
  { name: "dev", env: "Development", swatch: "bg-linear-to-br from-dev to-dev-2" },
  { name: "staging", env: "Staging", swatch: "bg-linear-to-br from-stage to-stage-2" },
  {
    name: "prod-eu",
    env: "Production",
    swatch: "bg-linear-to-br from-prod to-prod-2",
    protectedCluster: true,
  },
];

/** One row per cluster with the same three columns: swatch, name, optional tag. */
export function ClusterList() {
  return (
    <>
      <ul aria-label="Example clusters" className="m-0 mt-4 mb-3 grid list-none gap-2 p-0">
        {clusters.map((c) => (
          <li
            key={c.name}
            className="flex min-h-11 items-center gap-3 rounded-xl border border-line bg-surface-sunken py-1.5 pr-3 pl-1.5 text-[0.88rem] font-semibold"
          >
            <span aria-hidden="true" className={`size-7 shrink-0 rounded-lg ${c.swatch}`} />
            <span className="leading-none">{c.name}</span>
            {c.protectedCluster && (
              <span className="ml-auto rounded-md bg-prod px-2 py-1 text-[0.72rem] leading-none font-semibold text-on-accent">
                protected
              </span>
            )}
          </li>
        ))}
      </ul>
      <EnvLegend className="mb-3 justify-start" />
    </>
  );
}
