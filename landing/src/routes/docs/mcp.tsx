import { createFileRoute } from "@tanstack/react-router";
import { CodeBlock } from "../../components/CodeBlock";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/mcp")({
  head: () =>
    pageHead({
      title: "MCP & Claude Code · Eddy docs",
      description: "Connect Claude Code, or any MCP client, to your Flux fleet.",
      path: "/docs/mcp/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>MCP &amp; Claude Code</h1>
      <p className="lead">
        The hub exposes a <a href="https://modelcontextprotocol.io">Model Context Protocol</a> endpoint at{" "}
        <code>/mcp</code>. Claude Code can list your clusters, find unhealthy Flux resources across the fleet,
        read events and leave review threads that show up in the Eddy UI. Everything runs as you, so the tools
        see only what your RBAC allows.
      </p>
      <h2 id="connect">Connect Claude Code</h2>
      <ol>
        <li>
          In Eddy, create a personal access token under <strong>Settings → Access tokens</strong>. Choose{" "}
          <code>read</code> (read tools, plus threads) or <code>operate</code> (also reconcile, suspend and
          resume), and set an expiry. The token is shown once.
        </li>
        <li>
          Add the server, keeping the token in an environment variable:
          <CodeBlock
            lines={[
              "$ export EDDY_PAT=eddy_pat_...",
              "$ claude mcp add --transport http eddy https://<your-hub>/mcp \\",
              '    --header "Authorization: Bearer $EDDY_PAT"',
            ]}
          />
        </li>
        <li>
          Run <code>claude mcp list</code> and check that <code>eddy</code> is connected.
        </li>
      </ol>
      <p>
        The hub address must be reachable from your machine, usually over VPN. <code>/mcp</code> does not go
        through your sign-in proxy: the token is the credential.
      </p>
      <h2 id="tools">Tools</h2>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Scope</th>
              <th scope="col">Tools</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">
                <code>read</code>
              </th>
              <td>
                <code>list_clusters</code>, <code>list_resources</code>, <code>list_unhealthy</code>,{" "}
                <code>get_resource</code>, <code>get_events</code>, <code>list_threads</code>,{" "}
                <code>get_thread</code>, and <code>get_logs</code> when the operator enables it
              </td>
            </tr>
            <tr>
              <th scope="row">threads</th>
              <td>
                <code>create_thread</code>, <code>reply_thread</code>, <code>resolve_thread</code> (shown as{" "}
                <code>via mcp</code> in the UI)
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>operate</code>
              </th>
              <td>
                <code>reconcile</code>, <code>suspend</code>, <code>resume</code>, when MCP writes are enabled
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        Arguments and exact contracts are in{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/docs/mcp.md">docs/mcp.md</a> and{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/docs/api.md">docs/api.md</a>.
      </p>
      <h2 id="example">Example</h2>
      <p>
        With a <code>read</code> token, ask:
      </p>
      <blockquote className="callout">
        <p>
          Use the eddy tools to review every failing HelmRelease across the fleet. For each one, read its
          events and summary, work out the likely cause, and leave a thread on the resource with the cause and
          the next step. Don't change anything.
        </p>
      </blockquote>
      <h2 id="guard">Guardrails</h2>
      <ul>
        <li>
          <strong>Your RBAC, always.</strong> A token can never do more than its owner.
        </li>
        <li>
          <strong>
            Bearer only, <code>/mcp</code> only.
          </strong>{" "}
          Tokens are refused everywhere else, and <code>/mcp</code> ignores cookies.
        </li>
        <li>
          <strong>Protected clusters</strong> need <code>confirm_cluster</code> equal to the cluster name on
          every write. Operators can deny MCP writes there entirely.
        </li>
        <li>
          <strong>Redaction and untrusted data.</strong> Output is redacted and marked as data, not
          instructions. That limits prompt injection but does not remove it, so review write actions before
          approving.
        </li>
        <li>
          <strong>Limits and audit.</strong> Request, result and rate limits apply, and every call is audited
          with the token id.
        </li>
        <li>
          <strong>Kill switch.</strong> Operators can turn off <code>/mcp</code>, its writes or{" "}
          <code>get_logs</code> without a restart.
        </li>
      </ul>
      <Pager current="/docs/mcp/" />
    </>
  );
}
