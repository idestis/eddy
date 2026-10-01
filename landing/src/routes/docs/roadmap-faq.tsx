import { createFileRoute } from "@tanstack/react-router";
import { FullRef } from "../../components/FullRef";
import { Kbd } from "../../components/Kbd";
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
      <FullRef path="README.md#whats-in-v10-and-whats-next" label="README, what's in v1.0 and what's next" />
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
              <th scope="row">Clusters</th>
              <td>
                Hub and agents over outbound WebSocket; <strong>Add cluster wizard</strong> with install
                guide, one-time join tokens and a live connection checklist; 1..N agent replicas per cluster
              </td>
              <td>mTLS for agents</td>
            </tr>
            <tr>
              <th scope="row">Flux</th>
              <td>
                Kustomization, HelmRelease, Git/OCI/Helm repositories, HelmChart, Bucket; Karpenter and
                External Secrets presets; workloads, pods and core kinds; inventory with YAML and events
              </td>
              <td>
                Image automation, notification alerts, <code>flux diff</code> view
              </td>
            </tr>
            <tr>
              <th scope="row">Actions</th>
              <td>Reconcile (with source), suspend, resume; typed confirmation on protected clusters</td>
              <td>Workload restart, bulk actions</td>
            </tr>
            <tr>
              <th scope="row">UI</th>
              <td>
                Keyboard-first SPA, <Kbd>⌘K</Kbd> palette with fleet-wide search,{" "}
                <strong>live dependency graph</strong>, detail views (YAML, events, workload logs),
                environment colours, dark mode
              </td>
              <td>Saved views</td>
            </tr>
            <tr>
              <th scope="row">Sign-in</th>
              <td>
                <strong>GitHub (OAuth App or GitHub App) and OIDC</strong> (Google, Okta, Entra ID, Dex);
                local users, optionally break-glass only; trusted reverse-proxy headers
              </td>
              <td>Hub-managed access (assign users to groups, expiring grants). Future: SAML 2.0</td>
            </tr>
            <tr>
              <th scope="row">Access tokens</th>
              <td>
                Personal access tokens (<code>read</code> / <code>operate</code>, mandatory expiry) for MCP
              </td>
              <td>MCP OAuth 2.1, per-cluster scoping</td>
            </tr>
            <tr>
              <th scope="row">MCP</th>
              <td>
                <code>/mcp</code> with fleet-wide read tools, guarded actions and thread tools
              </td>
              <td>Log follow, subscriptions</td>
            </tr>
            <tr>
              <th scope="row">Threads</th>
              <td>Review threads on any resource or cluster, from people, Claude Code and Ask AI</td>
              <td>Mentions, notifications, webhooks</td>
            </tr>
            <tr>
              <th scope="row">Ask AI</th>
              <td>
                Anthropic API, or AWS Bedrock with any model that supports tool use (IRSA, Guardrails);
                read-only tools, redaction, log and YAML attachments
              </td>
              <td>OpenAI-compatible endpoints (Ollama, vLLM, Azure OpenAI), streaming answers</td>
            </tr>
            <tr>
              <th scope="row">Storage and HA</th>
              <td>
                PostgreSQL (bring your own, for example CloudNativePG); <strong>multiple hub replicas</strong>
                , active/active
              </td>
              <td>Read replicas</td>
            </tr>
            <tr>
              <th scope="row">Ops</th>
              <td>Helm charts, internal ingress examples, audit log, runtime kill switches</td>
              <td>Prometheus dashboards, audit webhook</td>
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
        Eddy is v1.0, an early release. It already includes GitHub and OIDC sign-in and active/active hub
        replicas on PostgreSQL, which you provide. mTLS for agents, MCP OAuth and hub-managed access are
        planned next.
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
        Yes. Eddy signs in with GitHub or OIDC (Google, Okta, Entra, Dex) natively, and a trusted reverse
        proxy such as oauth2-proxy also works. SAML 2.0 is a possible future addition. See the{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/docs/auth.md">sign-in guide</a>.
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
