import { createFileRoute } from "@tanstack/react-router";
import { CodeBlock } from "../../components/CodeBlock";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/install")({
  head: () =>
    pageHead({
      title: "Install · Eddy docs",
      description: "A summary of the production install. The full guide is in the repository.",
      path: "/docs/install/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Install</h1>
      <p className="lead">
        This page is a summary. The full guide, with every value, ingress example, Bedrock policy and
        troubleshooting, is{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/docs/install.md">docs/install.md</a> in the
        repository.
      </p>
      <h2 id="prereq">Prerequisites</h2>
      <ul>
        <li>Kubernetes 1.27 or later in every cluster, with Flux installed in the workload clusters.</li>
        <li>Helm 3.12 or later (Helm 4 works).</li>
        <li>
          PostgreSQL 14 or later for the hub: bring your own, for example CloudNativePG, RDS or Cloud SQL. It
          needs no extensions.
        </li>
        <li>
          An internal ingress (nginx or an AWS ALB) and a TLS certificate for the UI, plus a second internal
          endpoint for agents.
        </li>
      </ul>
      <h2 id="steps">Steps</h2>
      <ol>
        <li>
          <strong>Install the hub</strong> in the management cluster with the <code>eddy-hub</code> Helm
          chart, giving it a <code>publicURL</code>, an <code>agentsPublicURL</code>, a Secret holding the
          PostgreSQL DSN (<code>store.postgres.dsnSecret</code>) and a sign-in method: GitHub or OIDC, or
          local users (argon2id hashes).
          <CodeBlock
            lines={[
              "$ helm install eddy-hub oci://ghcr.io/idestis/charts/eddy-hub --version 1.0.0 \\",
              "    --namespace eddy -f hub-values.yaml",
            ]}
          />
        </li>
        <li>
          <strong>Add clusters</strong> with the <strong>Add cluster</strong> wizard in the UI, which prints
          the agent install command with a one-time join token and ticks a live checklist as the agent
          connects. Or declare them in <code>clusters[]</code> (name, environment, colour,{" "}
          <code>protected</code>) and the chart creates a <code>Cluster</code> resource and a token Secret for
          each.
        </li>
        <li>
          <strong>Install an agent</strong> in each workload cluster with the <code>eddy-agent</code> chart.
          The hub's install notes print the exact command for each cluster.
        </li>
        <li>
          <strong>Apply user RBAC.</strong> Nobody sees anything until you grant it. Start from{" "}
          <code>deploy/rbac/eddy-user-rbac.yaml</code>.
        </li>
        <li>
          <strong>Optional:</strong> enable Ask AI and configure the kill switches. Sign-in through a trusted
          reverse proxy such as oauth2-proxy still works if you prefer it.
        </li>
      </ol>
      <h2 id="know">Good to know</h2>
      <ul>
        <li>
          The hub runs <strong>two replicas by default</strong>, active/active, with PostgreSQL holding
          sessions, tokens, threads and audit. Agents can run several replicas per cluster too. Sessions and
          rate-limit counters live in <code>UNLOGGED</code> tables, so a database failover signs everyone out
          and nothing else is lost.
        </li>
        <li>
          <code>store.driver: memory</code> needs no database but runs one replica and loses everything on
          restart. Use it for demos only.
        </li>
        <li>
          GitHub and OIDC sign-in (Google, Okta, Entra, Dex) are set up in the hub values. See the{" "}
          <a href="https://github.com/idestis/eddy/blob/HEAD/docs/auth.md">sign-in guide</a>.
        </li>
        <li>
          Expose the UI through an <strong>internal</strong> ingress only. <code>/mcp</code> must not sit
          behind your sign-in proxy, because MCP clients send a personal access token.
        </li>
        <li>
          Until mTLS for agents arrives (planned), the agent token is the main control on the agent endpoint,
          so restrict it by source network.
        </li>
      </ul>
      <p>
        The images are <code>ghcr.io/idestis/eddy-hub</code> and <code>ghcr.io/idestis/eddy-agent</code>, and
        the charts are published under <code>oci://ghcr.io/idestis/charts</code>.
      </p>
      <Pager current="/docs/install/" />
    </>
  );
}
