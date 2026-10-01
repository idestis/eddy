const envs = [
  { label: "Production", swatch: "bg-linear-to-br from-prod to-prod-2" },
  { label: "Staging", swatch: "bg-linear-to-br from-stage to-stage-2" },
  { label: "Development", swatch: "bg-linear-to-br from-dev to-dev-2" },
];

/** The environment colours, from the same tokens the app uses. */
export function EnvLegend({ className = "" }: { className?: string }) {
  return (
    <ul
      aria-label="Environment colours"
      className={`m-0 flex list-none flex-wrap justify-center gap-x-4 gap-y-1 p-0 text-[0.8rem] text-ink-3 ${className}`}
    >
      {envs.map((e) => (
        <li key={e.label} className="flex items-center gap-1.5">
          <span aria-hidden="true" className={`size-2.5 rounded-full ${e.swatch}`} />
          {e.label}
        </li>
      ))}
    </ul>
  );
}
