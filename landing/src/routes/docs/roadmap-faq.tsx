import { createFileRoute } from "@tanstack/react-router";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/roadmap-faq")({
  head: () =>
    pageHead({
      title: "Roadmap & FAQ · Eddy docs",
      description: "What is ready in v1.0, what is next, and honest answers.",
      path: "/docs/roadmap-faq/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Roadmap &amp; FAQ</h1>
      <h2 id="roadmap">Roadmap</h2>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Area</th>
              <th scope="col">Ready in v1.0</th>
              <th scope="col">Next (v1.1+)</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">Sign-in</th>
              <td>Local users, trusted proxy headers</td>
              <td>GitHub OAuth2, OIDC (Google, Okta, Entra, Dex), SAML 2.0</td>
            </tr>
            <tr>
              <th scope="row">Clusters</th>
              <td>
                Outbound WebSocket, <code>Cluster</code> CRD, token auth
              </td>
              <td>
                mTLS for agents, <code>eddy register</code> CLI
              </td>
            </tr>
            <tr>
              <th scope="row">Storage</th>
              <td>Embedded SQLite, no external database needed</td>
              <td>Multiple hub replicas (HA)</td>
            </tr>
            <tr>
              <th scope="row">Flux</th>
              <td>Kustomization, HelmRelease, sources, HelmChart, Bucket</td>
              <td>
                Image automation, notification alerts, <code>flux diff</code> view
              </td>
            </tr>
            <tr>
              <th scope="row">Actions</th>
              <td>Reconcile, suspend, resume</td>
              <td>Workload restart, bulk actions</td>
            </tr>
            <tr>
              <th scope="row">MCP and tokens</th>
              <td>PATs, fleet-wide tools, threads</td>
              <td>MCP OAuth 2.1, per-cluster scoping, log follow</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        The full table is in the{" "}
        <a href="https://github.com/idestis/eddy#whats-in-v10-and-whats-next">README</a>. Plans can change.
      </p>
      <h2 id="faq">FAQ</h2>
      <h3>What is the status?</h3>
      <p>
        Eddy is v1.0, an early release. The core is stable, and sign-in providers and high availability arrive
        in v1.1. Until then the hub runs a single replica with embedded SQLite, so plan for that.
      </p>
      <h3>Do you need to expose workload clusters?</h3>
      <p>No. Agents dial out to the hub. The hub holds no workload-cluster credentials.</p>
      <h3>Can Eddy read my Secrets?</h3>
      <p>No. The agent does not watch Secrets or ConfigMaps, and their data never leaves a cluster.</p>
      <h3>Who can do what?</h3>
      <p>
        Your Kubernetes RBAC decides. Eddy runs each request as the signed-in user through impersonation, and
        invents no roles of its own.
      </p>
      <h3>Is AI required?</h3>
      <p>
        No. Ask AI is off by default, read-only, and works with the Anthropic API or AWS Bedrock. MCP is
        optional too, and both can be switched off at runtime.
      </p>
      <h3>Can I use my company login?</h3>
      <p>
        Today through a reverse proxy such as oauth2-proxy or Pomerium. Native GitHub, OIDC and SAML sign-in
        are planned for v1.1.
      </p>
      <h3>How do I contribute or report a problem?</h3>
      <p>
        Read <a href="https://github.com/idestis/eddy/blob/HEAD/CONTRIBUTING.md">CONTRIBUTING.md</a> and open
        an issue on <a href="https://github.com/idestis/eddy/issues">GitHub</a>. For vulnerabilities, see{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/SECURITY.md">SECURITY.md</a>.
      </p>
      <h3>What is the license?</h3>
      <p>
        <a href="https://github.com/idestis/eddy/blob/HEAD/LICENSE">Apache License 2.0</a>.
      </p>
      <Pager current="/docs/roadmap-faq/" />
    </>
  );
}
