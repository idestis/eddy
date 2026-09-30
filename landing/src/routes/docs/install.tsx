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
          An internal ingress (nginx or an AWS ALB) and a TLS certificate for the UI, plus a second internal
          endpoint for agents.
        </li>
      </ul>
      <h2 id="steps">Steps</h2>
      <ol>
        <li>
          <strong>Install the hub</strong> in the management cluster with the <code>eddy-hub</code> Helm
          chart, giving it a <code>publicURL</code>, an <code>agentsPublicURL</code>, local users (argon2id
          hashes) and persistence.
          <CodeBlock
            lines={[
              "$ helm install eddy-hub oci://ghcr.io/idestis/charts/eddy-hub --version 1.0.0 \\",
              "    --namespace eddy -f hub-values.yaml",
            ]}
          />
        </li>
        <li>
          <strong>Register clusters</strong> in <code>clusters[]</code> (name, environment, colour,{" "}
          <code>protected</code>). The chart creates a <code>Cluster</code> resource and a token Secret for
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
          <strong>Optional:</strong> sign in through your identity provider with oauth2-proxy, enable Ask AI,
          and configure the kill switches.
        </li>
      </ol>
      <h2 id="know">Good to know</h2>
      <ul>
        <li>
          The hub runs <strong>one replica</strong> with SQLite on a PersistentVolumeClaim. Raising{" "}
          <code>replicaCount</code> is rejected at install time. Multiple hub replicas (HA) are planned for
          v1.1.
        </li>
        <li>
          With persistence off, every restart logs everyone out and loses threads. Use that for demos only.
        </li>
        <li>
          Expose the UI through an <strong>internal</strong> ingress only. <code>/mcp</code> must not sit
          behind your sign-in proxy, because MCP clients send a personal access token.
        </li>
        <li>
          Until mTLS arrives in v1.1, the agent token is the main control on the agent endpoint, so restrict
          it by source network.
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
