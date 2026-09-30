import type { ReactNode } from "react";

/*
 * Architecture diagram, drawn twice from the same parts: a wide layout and a stacked one for phones,
 * so text never shrinks below a readable size. Every arrow runs down the centre line of the boxes it
 * joins, and connector labels sit on the line with a knockout in the card colour.
 * Colours come from theme tokens through Tailwind fill-/stroke- utilities, so both themes work.
 */

const DESC =
  "Browsers and MCP clients connect to the hub in the management cluster. Agents in the prod-eu and staging workload clusters open outbound WebSocket connections to the hub. Each agent talks to its local Kubernetes API server as the impersonated user.";

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
}
function Box({ x, y, w, h, hub }: BoxProps) {
  return (
    <rect
      x={x}
      y={y}
      width={w}
      height={h}
      rx={hub ? 16 : 12}
      className={
        hub
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
  // Centre lines: 220 and 660 (mirror images about the hub centre, x = 440).
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

      <Box x={90} y={16} w={260} h={60} />
      <Title x={220} y={42}>
        Browser
      </Title>
      <Sub x={220} y={60}>
        session cookie, SSE
      </Sub>
      <Box x={530} y={16} w={260} h={60} />
      <Title x={660} y={42}>
        Claude Code / MCP client
      </Title>
      <Sub x={660} y={60}>
        Bearer eddy_pat_…
      </Sub>

      <Connector x={220} from={76} to={140} label="HTTPS" labelWidth={64} markerId="w" />
      <Connector x={660} from={76} to={140} label="HTTPS · /mcp" labelWidth={104} markerId="w" />

      <Box x={40} y={140} w={800} h={112} hub />
      <text x={62} y={164} className="fill-ink-3 font-sans text-[11.5px]">
        Management cluster
      </text>
      <Title x={440} y={190}>
        Hub
      </Title>
      <Mono x={440} y={210}>
        UI · /api · /mcp on :8080 · agent endpoint on :8443
      </Mono>
      <Sub x={440} y={232}>
        SQLite: sessions, tokens, threads, audit
      </Sub>

      <Connector
        x={220}
        from={324}
        to={252}
        label="WebSocket, agent dials out"
        labelWidth={200}
        out
        markerId="w"
      />
      <Connector
        x={660}
        from={324}
        to={252}
        label="WebSocket, agent dials out"
        labelWidth={200}
        out
        markerId="w"
      />

      <Box x={40} y={324} w={360} h={92} />
      <Sub x={220} y={346}>
        Workload cluster · prod-eu
      </Sub>
      <Title x={220} y={370}>
        Agent
      </Title>
      <Sub x={220} y={392}>
        informers, summaries, read-only ServiceAccount
      </Sub>
      <Box x={480} y={324} w={360} h={92} />
      <Sub x={660} y={346}>
        Workload cluster · staging
      </Sub>
      <Title x={660} y={370}>
        Agent
      </Title>
      <Sub x={660} y={392}>
        impersonated reads and actions
      </Sub>
    </svg>
  );
}

function Stacked() {
  // Two columns of the same shape; centre lines at 81 and 259.
  return (
    <svg
      viewBox="0 0 340 440"
      role="img"
      aria-labelledby="dg-n-t dg-n-d"
      className="mx-auto block h-auto w-full max-w-[460px] min-[900px]:hidden"
    >
      <title id="dg-n-t">Eddy architecture</title>
      <desc id="dg-n-d">{DESC}</desc>
      <Defs id="n" />

      <Box x={0} y={1} w={162} h={62} />
      <Title x={81} y={27}>
        Browser
      </Title>
      <Sub x={81} y={46} size={12}>
        cookie, SSE
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

      <Box x={0} y={124} w={340} h={124} hub />
      <Sub x={170} y={146} size={12}>
        Management cluster
      </Sub>
      <Title x={170} y={172}>
        Hub
      </Title>
      <Mono x={170} y={192}>
        UI · /api · /mcp :8080
      </Mono>
      <Mono x={170} y={209}>
        agent endpoint :8443
      </Mono>
      <Sub x={170} y={232} size={12}>
        SQLite: sessions, tokens, threads
      </Sub>

      <Connector x={81} from={324} to={248} label="WebSocket, dials out" labelWidth={148} out markerId="n" />
      <Connector x={259} from={324} to={248} label="WebSocket, dials out" labelWidth={148} out markerId="n" />

      <Box x={0} y={324} w={162} h={112} />
      <Sub x={81} y={346} size={12}>
        prod-eu cluster
      </Sub>
      <Title x={81} y={371}>
        Agent
      </Title>
      <Sub x={81} y={392} size={12}>
        informers, summaries,
      </Sub>
      <Sub x={81} y={410} size={12}>
        read-only ServiceAccount
      </Sub>
      <Box x={178} y={324} w={162} h={112} />
      <Sub x={259} y={346} size={12}>
        staging cluster
      </Sub>
      <Title x={259} y={371}>
        Agent
      </Title>
      <Sub x={259} y={392} size={12}>
        impersonated reads
      </Sub>
      <Sub x={259} y={410} size={12}>
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
    </figure>
  );
}
