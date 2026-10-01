import { createFileRoute, Link } from "@tanstack/react-router";
import { FullRef } from "../../components/FullRef";
import { Kbd } from "../../components/Kbd";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/")({
  head: () =>
    pageHead({
      title: "Eddy docs: overview",
      description:
        "What Eddy is, how the hub and agents fit together, and where to start: install, sign-in, clusters, access and operations.",
      path: "/docs/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Overview</h1>
      <p className="lead">
        Eddy shows every Flux cluster you run in one place. It is a fast, keyboard-first web UI, it is open
        source (Apache-2.0), and it is at <strong>v1.0</strong>. These docs take you from a local demo to a
        production install, one step at a time.
      </p>
      <FullRef path="docs/architecture.md" label="docs/architecture.md" />
      <h2 id="parts">Two parts</h2>
      <ul>
        <li>
          The <strong>hub</strong> runs in your management cluster, two replicas by default. It serves the UI,
          the API and the MCP endpoint, and keeps its own data (sessions, tokens, threads, audit) in
          PostgreSQL.
        </li>
        <li>
          An <strong>agent</strong> runs in every workload cluster. It watches Flux and workloads, and it{" "}
          <strong>dials out</strong> to the hub over one WebSocket. Workload API servers are never exposed,
          and the hub holds no credentials for them.
        </li>
      </ul>
      <p>
        Eddy has no permission model of its own. Each person signs in to the hub, and their requests run in
        each cluster as that user, impersonated by the agent. What they can see and do is decided by your
        normal Kubernetes RBAC. Only summaries leave a cluster: never Secret or ConfigMap data.
      </p>
      <h2 id="what">What you get in v1.0</h2>
      <ul>
        <li>
          Flux Kustomizations, HelmReleases, Git, OCI and Helm repositories, HelmCharts and Buckets, plus
          workloads, pods and an inventory tree. Optional presets add Karpenter and External Secrets.
        </li>
        <li>Reconcile (with source), suspend and resume, with a typed confirmation on protected clusters.</li>
        <li>
          A <Kbd>⌘K</Kbd> palette across every cluster, a live dependency graph, YAML, events and logs, and
          dark mode.
        </li>
        <li>Sign-in with GitHub or OIDC (Google, Okta, Entra ID, Dex), local users, or a trusted proxy.</li>
        <li>An Add cluster wizard, review threads, an MCP endpoint for Claude Code, and Ask AI.</li>
      </ul>
      <h2 id="read">How to read these docs</h2>
      <p>The pages follow the order you will need them. Each one ends with how to check that it worked.</p>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">You want to</th>
              <th scope="col">Read</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">Try Eddy in five minutes</th>
              <td>
                <Link to="/docs/quickstart/">Quickstart (kind)</Link>
              </td>
            </tr>
            <tr>
              <th scope="row">Run it for real</th>
              <td>
                <Link to="/docs/install/">Install the hub</Link>, then{" "}
                <Link to="/docs/sign-in/">Sign-in</Link>, <Link to="/docs/clusters/">Add clusters</Link> and{" "}
                <Link to="/docs/access/">Access and RBAC</Link>
              </td>
            </tr>
            <tr>
              <th scope="row">Learn the interface</th>
              <td>
                <Link to="/docs/using/">Using Eddy</Link>
              </td>
            </tr>
            <tr>
              <th scope="row">Turn on AI or connect Claude Code</th>
              <td>
                <Link to="/docs/ask-ai/">Ask AI</Link>, <Link to="/docs/mcp/">MCP &amp; Claude Code</Link>
              </td>
            </tr>
            <tr>
              <th scope="row">Upgrade, back up, switch things off</th>
              <td>
                <Link to="/docs/operations/">Operations</Link>
              </td>
            </tr>
            <tr>
              <th scope="row">Review the risks</th>
              <td>
                <Link to="/docs/security/">Security model</Link>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        Examples use made-up names: the domain <code>eddy.internal.example.com</code>, the organization{" "}
        <code>acme</code> and the clusters <code>prod-eu</code> and <code>staging</code>. Replace them with
        yours. Shell blocks show the command after a dimmed <code>$</code>; the Copy button leaves the prompt
        out.
      </p>
      <p>
        The reference material lives in the repository:{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/docs/api.md">API</a> and the{" "}
        <a href="https://github.com/idestis/eddy/tree/HEAD/docs/adr">design decisions</a>.
      </p>
      <Pager current="/docs/" />
    </>
  );
}
