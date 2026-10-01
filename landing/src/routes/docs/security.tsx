import { createFileRoute } from "@tanstack/react-router";
import { FullRef } from "../../components/FullRef";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/security")({
  head: () =>
    pageHead({
      title: "Security model · Eddy docs",
      description: "How Eddy protects your clusters.",
      path: "/docs/security/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Security model</h1>
      <p className="lead">
        A short version of the security model. The full reasoning and threat model are in{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/docs/security.md">docs/security.md</a> and the{" "}
        <a href="https://github.com/idestis/eddy/tree/HEAD/docs/adr">ADRs</a>. To report a vulnerability, see{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/SECURITY.md">SECURITY.md</a>.
      </p>
      <FullRef path="docs/security.md" />
      <h2 id="short">The short version</h2>
      <ul>
        <li>
          <strong>Agents only dial out.</strong> Workload API servers are never exposed, and the hub holds no
          credentials for any workload cluster.
        </li>
        <li>
          <strong>Kubernetes RBAC is the only permission model.</strong> Every read and write runs as the
          signed-in person, impersonated in the cluster.
        </li>
        <li>
          <strong>Agents have no write access of their own.</strong> Their ServiceAccount is read-only, apart
          from <code>impersonate</code> and SubjectAccessReviews.
        </li>
        <li>
          <strong>No Secret or ConfigMap data leaves a cluster.</strong> Agents send summaries only.
        </li>
        <li>
          <strong>Ask AI is read-only.</strong> Everything the AI or an MCP client sees is filtered by the
          asking user's RBAC and redacted first.
        </li>
      </ul>
      <h2 id="impersonation">Impersonation</h2>
      <p>
        Users appear in the cluster as <code>local:&lt;username&gt;</code> (or the proxy identity), with
        groups prefixed <code>eddy:</code>. <code>system:*</code> users and groups are never impersonated. The
        cluster's own audit log records the real user. A compromised hub cannot reach any cluster directly,
        and the damage is bounded by the RBAC you granted to <code>eddy:</code> groups.
      </p>
      <h2 id="signin">Signing in and tokens</h2>
      <ul>
        <li>
          v1.0 supports GitHub and OIDC sign-in (Google, Okta, Entra, Dex), local users (argon2id) and trusted
          reverse-proxy headers. SAML 2.0 is a possible future addition. See the{" "}
          <a href="https://github.com/idestis/eddy/blob/HEAD/docs/auth.md">sign-in guide</a>.
        </li>
        <li>
          Sessions are server-side behind a <code>__Host-</code> cookie, with CSRF protection and a strict
          CSP.
        </li>
        <li>
          Personal access tokens (<code>eddy_pat_…</code>) work only as a bearer token on <code>/mcp</code>,
          are stored hashed, expire, and are revocable.
        </li>
      </ul>
      <h2 id="ai">AI and MCP</h2>
      <ul>
        <li>
          Ask AI has no write tools. Cluster data is wrapped as untrusted content and output is redacted.
        </li>
        <li>
          Bedrock keeps data in your AWS account and region. The Anthropic API sends data to Anthropic, and
          the UI shows which provider is in use.
        </li>
        <li>
          MCP writes need the <code>operate</code> scope and <code>confirm_cluster</code> on protected
          clusters. Operators can switch AI, MCP or MCP writes off at runtime with the{" "}
          <code>eddy-runtime</code> ConfigMap.
        </li>
      </ul>
      <h2 id="harden">Hardening checklist</h2>
      <ul>
        <li>
          Set <code>publicURL</code> to the real https origin.
        </li>
        <li>Keep the UI and agent ingress internal and restrict source CIDRs.</li>
        <li>Encrypt the PostgreSQL storage and its backups.</li>
        <li>
          Pin agent impersonation with <code>impersonation.groups</code> and keep the agent NetworkPolicy on.
        </li>
        <li>
          Keep <code>mcp.allowLogs</code> and <code>ai.allowLogs</code> off unless you need them.
        </li>
        <li>
          Mark production clusters <code>protected: true</code>.
        </li>
        <li>
          Grant <code>eddy:</code> groups only the RBAC people should have.
        </li>
      </ul>
      <Pager current="/docs/security/" />
    </>
  );
}
