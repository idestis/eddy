import type { ReactNode } from "react";
import { EnvLegend } from "./EnvLegend";

/*
 * Architecture diagram, drawn twice from the same parts: a wide layout and a stacked one for phones,
 * so text never shrinks below a readable size. Every arrow runs down the centre line of the boxes it
 * joins, and connector labels sit on the line with a knockout in the card colour.
 * Colours come from theme tokens through Tailwind fill-/stroke- utilities, so both themes work.
 */

const DESC =
  "Browsers and MCP clients connect to the hub in the management cluster. The hub runs as active/active replicas that keep their data in PostgreSQL. Agent replicas in the prod-eu and staging workload clusters open outbound WebSocket connections to the hub. Each agent talks to its local Kubernetes API server as the impersonated user.";

function Defs({ id }: { id: string }) {
  return (
    <defs>
      <marker
        id={`${id}-a`}
        viewBox="0 0 10 10"
        refX="9"
        refY="5"
        markerWidth="8"
        markerHeight="8"
        orient="auto"
      >
        <path d="M0 0 10 5 0 10z" className="fill-diagram-line" />
      </marker>
      <marker
        id={`${id}-b`}
        viewBox="0 0 10 10"
        refX="9"
        refY="5"
        markerWidth="8"
        markerHeight="8"
        orient="auto"
      >
        <path d="M0 0 10 5 0 10z" className="fill-accent" />
      </marker>
    </defs>
  );
}

interface BoxProps {
  x: number;
  y: number;
  w: number;
  h: number;
  hub?: boolean;
  env?: "prod" | "stage";
}
const ENV_STROKE = {
  prod: "fill-diagram-node stroke-prod stroke-[1.6]",
  stage: "fill-diagram-node stroke-stage stroke-[1.6]",
};
const ENV_FILL = { prod: "fill-prod", stage: "fill-stage" };

function Box({ x, y, w, h, hub, env }: BoxProps) {
  return (
    <rect
      x={x}
      y={y}
      width={w}
      height={h}
      rx={hub ? 16 : 12}
      className={
        env
          ? ENV_STROKE[env]
          : hub
            ? "fill-diagram-node stroke-accent stroke-[1.6]"
            : "fill-diagram-node stroke-line-strong stroke-[1.2]"
      }
    />
  );
}

function Title({ x, y, children }: { x: number; y: number; children: ReactNode }) {
  return (
    <text x={x} y={y} textAnchor="middle" className="fill-ink font-sans text-[13px] font-semibold">
      {children}
    </text>
  );
}
function Sub({ x, y, children, size = 11.5 }: { x: number; y: number; children: ReactNode; size?: number }) {
  return (
    <text x={x} y={y} textAnchor="middle" className="fill-ink-3 font-sans" style={{ fontSize: size }}>
      {children}
    </text>
  );
}
/** A cluster name led by a dot in its environment colour (the same tokens as the app and the legend). */
function ClusterLabel({
  x,
  y,
  env,
  children,
  size = 11.5,
}: {
  x: number;
  y: number;
  env: "prod" | "stage";
  children: string;
  size?: number;
}) {
  const width = children.length * size * 0.56 + 14;
  const left = x - width / 2;
  return (
    <>
      <circle cx={left + 4} cy={y - 4} r={4} className={ENV_FILL[env]} />
      <text x={left + 14} y={y} className="fill-ink-3 font-sans" style={{ fontSize: size }}>
        {children}
      </text>
    </>
  );
}

function Mono({ x, y, children }: { x: number; y: number; children: ReactNode }) {
  return (
    <text x={x} y={y} textAnchor="middle" className="fill-ink-2 font-mono text-[11px] font-medium">
      {children}
    </text>
  );
}

/** A connector with its label centred on the line, over a knockout pill. */
function Connector({
  x,
  from,
  to,
  label,
  labelWidth,
  out,
  markerId,
}: {
  x: number;
  from: number;
  to: number;
  label: string;
  labelWidth: number;
  out?: boolean;
  markerId: string;
}) {
  const mid = (from + to) / 2;
  return (
    <g>
      <path
        d={`M${x} ${from}V${to}`}
        fill="none"
        markerEnd={`url(#${markerId}-${out ? "b" : "a"})`}
        className={out ? "stroke-accent stroke-2 [stroke-dasharray:5_4]" : "stroke-diagram-line stroke-[1.5]"}
      />
      <rect
        x={x - labelWidth / 2}
        y={mid - 11}
        width={labelWidth}
        height={22}
        rx={11}
        className="fill-surface stroke-line"
      />
      <text
        x={x}
        y={mid}
        textAnchor="middle"
        dominantBaseline="central"
        className="fill-ink-2 font-mono text-[11px] font-medium"
      >
        {label}
      </text>
    </g>
  );
}

function Wide() {
  // Centre lines: 170 and 430. The hub spans 40-560 and PostgreSQL sits to its right.
  return (
    <svg
      viewBox="0 0 880 432"
      role="img"
      aria-labelledby="dg-w-t dg-w-d"
      className="hidden h-auto w-full min-[900px]:block"
    >
      <title id="dg-w-t">Eddy architecture</title>
      <desc id="dg-w-d">{DESC}</desc>
      <Defs id="w" />

      <Box x={40} y={16} w={260} h={60} />
      <Title x={170} y={40}>
        Browser
      </Title>
      <Sub x={170} y={59}>
        cookie, SSE · GitHub/OIDC sign-in
      </Sub>
      <Box x={300 + 20} y={16} w={220} h={60} />
      <Title x={430} y={40}>
        Claude Code / MCP client
      </Title>
      <Sub x={430} y={59}>
        Bearer eddy_pat_…
      </Sub>

      <Connector x={170} from={76} to={140} label="HTTPS" labelWidth={64} markerId="w" />
      <Connector x={430} from={76} to={140} label="HTTPS · /mcp" labelWidth={104} markerId="w" />

      <Box x={40} y={140} w={520} h={112} hub />
      <text x={62} y={164} className="fill-ink-3 font-sans text-[11.5px]">
        Management cluster
      </text>
      <Title x={300} y={188}>
        Hub × N · active/active replicas
      </Title>
      <Mono x={300} y={208}>
        UI · /api · /mcp on :8080 · agent endpoint on :8443
      </Mono>
      <Sub x={300} y={232}>
        replicas share state via PostgreSQL LISTEN/NOTIFY and a peer relay
      </Sub>

      <path d="M560 196H640" fill="none" markerEnd="url(#w-a)" className="stroke-diagram-line stroke-[1.5]" />
      <Box x={640} y={140} w={200} h={112} />
      <Title x={740} y={174}>
        PostgreSQL
      </Title>
      <Sub x={740} y={194}>
        bring your own, e.g.
      </Sub>
      <Sub x={740} y={210}>
        CloudNativePG
      </Sub>
      <Sub x={740} y={232}>
        sessions, tokens, threads, audit
      </Sub>

      <Connector
        x={170}
        from={324}
        to={252}
        label="WebSocket, each agent dials out"
        labelWidth={224}
        out
        markerId="w"
      />
      <Connector
        x={430}
        from={324}
        to={252}
        label="WebSocket, each agent dials out"
        labelWidth={224}
        out
        markerId="w"
      />

      <Box x={40} y={324} w={260} h={92} env="prod" />
      <ClusterLabel x={170} y={346} env="prod">
        Workload cluster · prod-eu
      </ClusterLabel>
      <Title x={170} y={370}>
        Agent × 2
      </Title>
      <Sub x={170} y={392}>
        informers, summaries, read-only
      </Sub>
      <Box x={300 + 20} y={324} w={220} h={92} env="stage" />
      <ClusterLabel x={430} y={346} env="stage">
        Workload cluster · staging
      </ClusterLabel>
      <Title x={430} y={370}>
        Agent × 2
      </Title>
      <Sub x={430} y={392}>
        impersonated reads, actions
      </Sub>
    </svg>
  );
}

function Stacked() {
  // Two columns of the same shape; centre lines at 81 and 259. PostgreSQL nests in the management cluster.
  return (
    <svg
      viewBox="0 0 340 520"
      role="img"
      aria-labelledby="dg-n-t dg-n-d"
      className="mx-auto block h-auto w-full max-w-[460px] min-[900px]:hidden"
    >
      <title id="dg-n-t">Eddy architecture</title>
      <desc id="dg-n-d">{DESC}</desc>
      <Defs id="n" />

      <Box x={0} y={1} w={162} h={62} />
      <Title x={81} y={25}>
        Browser
      </Title>
      <Sub x={81} y={43} size={12}>
        cookie, SSE
      </Sub>
      <Sub x={81} y={57} size={12}>
        GitHub/OIDC
      </Sub>
      <Box x={178} y={1} w={162} h={62} />
      <Title x={259} y={27}>
        Claude Code
      </Title>
      <Sub x={259} y={46} size={12}>
        Bearer eddy_pat_…
      </Sub>

      <Connector x={81} from={63} to={124} label="HTTPS" labelWidth={60} markerId="n" />
      <Connector x={259} from={63} to={124} label="HTTPS · /mcp" labelWidth={100} markerId="n" />

      <Box x={0} y={124} w={340} h={188} hub />
      <Sub x={170} y={146} size={12}>
        Management cluster
      </Sub>
      <Title x={170} y={170}>
        Hub × N · active/active
      </Title>
      <Mono x={170} y={190}>
        UI · /api · /mcp :8080
      </Mono>
      <Mono x={170} y={207}>
        agent endpoint :8443
      </Mono>
      <path d="M170 214V244" fill="none" markerEnd="url(#n-a)" className="stroke-diagram-line stroke-[1.5]" />
      <Box x={20} y={244} w={300} h={56} />
      <Title x={170} y={265}>
        PostgreSQL (bring your own)
      </Title>
      <Sub x={170} y={284} size={12}>
        sessions, tokens, threads, audit
      </Sub>

      <Connector x={81} from={388} to={312} label="WebSocket, dials out" labelWidth={148} out markerId="n" />
      <Connector x={259} from={388} to={312} label="WebSocket, dials out" labelWidth={148} out markerId="n" />

      <Box x={0} y={388} w={162} h={112} env="prod" />
      <ClusterLabel x={81} y={410} env="prod" size={12}>
        prod-eu cluster
      </ClusterLabel>
      <Title x={81} y={435}>
        Agent × 2
      </Title>
      <Sub x={81} y={456} size={12}>
        informers, summaries,
      </Sub>
      <Sub x={81} y={474} size={12}>
        read-only ServiceAccount
      </Sub>
      <Box x={178} y={388} w={162} h={112} env="stage" />
      <ClusterLabel x={259} y={410} env="stage" size={12}>
        staging cluster
      </ClusterLabel>
      <Title x={259} y={435}>
        Agent × 2
      </Title>
      <Sub x={259} y={456} size={12}>
        impersonated reads
      </Sub>
      <Sub x={259} y={474} size={12}>
        and actions
      </Sub>
    </svg>
  );
}

export function ArchDiagram() {
  return (
    <figure className="m-0 rounded-2xl border border-line bg-surface p-3 sm:p-5">
      <Wide />
      <Stacked />
      <EnvLegend className="mt-3" />
    </figure>
  );
}
