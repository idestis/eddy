import { createFileRoute, Link } from "@tanstack/react-router";
import { Kbd } from "../../components/Kbd";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/")({
  head: () =>
    pageHead({
      title: "Eddy docs: overview",
      description: "Eddy is a fast, keyboard-first, multi-cluster web UI for Flux.",
      path: "/docs/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Overview</h1>
      <p className="lead">
        Eddy shows every Flux cluster you run in one place. It is open source (Apache-2.0) and at{" "}
        <strong>v1.0, an early release</strong>. The core is stable, and sign-in providers and high
        availability arrive in v1.1.
      </p>
      <h2 id="parts">Two parts</h2>
      <ul>
        <li>
          The <strong>hub</strong> runs once, in your management cluster. It serves the UI, the API and the
          MCP endpoint.
        </li>
        <li>
          An <strong>agent</strong> runs in every workload cluster. It dials out to the hub, so workload API
          servers are never exposed and the hub holds no cluster credentials.
        </li>
      </ul>
      <p>
        Eddy has no permission model of its own. Each person signs in to the hub, and their requests run in
        each cluster as that user, impersonated by the agent. What they can do is decided by your normal
        Kubernetes RBAC.
      </p>
      <h2 id="what">What you get in v1.0</h2>
      <ul>
        <li>
          Flux Kustomizations, HelmReleases, Git, OCI and Helm repositories, HelmCharts and Buckets, plus
          workloads, pods and an inventory tree.
        </li>
        <li>Reconcile (with source), suspend and resume, with a typed confirmation on protected clusters.</li>
        <li>
          A <Kbd>⌘K</Kbd> command palette across every cluster, detail views for YAML, events and logs, and
          dark mode.
        </li>
        <li>
          Sign-in with GitHub or OIDC (Google, Okta, Entra, Dex), local users, or trusted reverse-proxy
          headers.
        </li>
        <li>
          An Add cluster wizard, a live dependency graph, and fleet-wide search. The hub stores its data in
          PostgreSQL and runs active/active replicas.
        </li>
        <li>
          An MCP endpoint for Claude Code, review threads, and Ask AI on the Anthropic API or AWS Bedrock.
        </li>
      </ul>
      <h2 id="next">Where to go next</h2>
      <ul>
        <li>
          <Link to="/docs/quickstart/">Quickstart</Link>: run everything on a local kind cluster.
        </li>
        <li>
          <Link to="/docs/install/">Install</Link>: the production path, in summary.
        </li>
        <li>
          <Link to="/docs/security/">Security model</Link>: what crosses each boundary and why.
        </li>
        <li>
          <Link to="/docs/mcp/">MCP &amp; Claude Code</Link>: connect an assistant to your fleet.
        </li>
        <li>
          <Link to="/docs/roadmap-faq/">Roadmap &amp; FAQ</Link>: what is next, and honest answers.
        </li>
      </ul>
      <p>
        The source docs live in the repository:{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/docs/architecture.md">architecture</a>,{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/docs/api.md">API</a> and the{" "}
        <a href="https://github.com/idestis/eddy/tree/HEAD/docs/adr">design decisions</a>.
      </p>
      <Pager current="/docs/" />
    </>
  );
}
