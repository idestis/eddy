import { createFileRoute, Link } from "@tanstack/react-router";
import { CodeBlock } from "../../components/CodeBlock";
import { FullRef } from "../../components/FullRef";
import { Kbd } from "../../components/Kbd";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/mcp")({
  head: () =>
    pageHead({
      title: "MCP & Claude Code · Eddy docs",
      description:
        "Connect Claude Code, or any MCP client, to your Flux fleet: create a token, add the server, the tools, threads and protected clusters.",
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
      <FullRef path="docs/mcp.md" />

      <h2 id="connect">Connect Claude Code</h2>
      <ol>
        <li>
          <strong>Create a personal access token (PAT).</strong> In Eddy open <strong>Access tokens</strong>{" "}
          (press <Kbd>g</Kbd> then <Kbd>k</Kbd>). Choose the smallest scope you need:
          <ul>
            <li>
              <code>read</code>: the read tools, plus creating and replying to threads.
            </li>
            <li>
              <code>operate</code>: also reconcile, suspend and resume.
            </li>
          </ul>
          Give it an expiry (30 days by default). The token, <code>eddy_pat_…</code>, is shown once, so copy
          it now.
        </li>
        <li>
          <strong>Add the server</strong>, keeping the token in an environment variable so it stays out of
          shell history and config files:
          <CodeBlock
            code={`
$ export EDDY_PAT=eddy_pat_...
$ claude mcp add --transport http eddy https://eddy.internal.example.com/mcp \\
    --header "Authorization: Bearer $EDDY_PAT"
`}
          />
          Add <code>--scope user</code> to make it available in every project, or <code>--scope project</code>{" "}
          to write a shared <code>.mcp.json</code>. A shared file should reference the variable, not the
          token:
          <CodeBlock
            code={`
{
  "mcpServers": {
    "eddy": {
      "type": "http",
      "url": "https://eddy.internal.example.com/mcp",
      "headers": {"Authorization": "Bearer \${EDDY_PAT}"}
    }
  }
}
`}
          />
        </li>
        <li>
          <strong>Verify.</strong> Run <code>claude mcp list</code> (or <code>/mcp</code> inside Claude Code)
          and check that <code>eddy</code> is connected:
          <CodeBlock lines={["$ claude mcp list"]} />
        </li>
      </ol>
      <p>
        The hub address must be reachable from your machine, usually over VPN. <code>/mcp</code> does not go
        through your sign-in proxy: the token is the credential. Check <code>claude mcp add --help</code> if
        your version differs.
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
                <code>get_thread</code>, and <code>get_logs</code> when the operator enables it (
                <code>mcp.allowLogs</code>, and <code>pods/log</code> RBAC)
              </td>
            </tr>
            <tr>
              <th scope="row">threads</th>
              <td>
                <code>create_thread</code>, <code>reply_thread</code>, <code>resolve_thread</code>. They need{" "}
                <code>read</code>, or <code>operate</code> if the operator set{" "}
                <code>auth.tokens.threadWriteScope: operate</code>
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>operate</code>
              </th>
              <td>
                <code>reconcile</code>, <code>suspend</code>, <code>resume</code>, when MCP writes are enabled
                (<code>mcp.writes</code>)
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        <code>get_resource</code> returns a summary plus redacted YAML. Secrets and ConfigMaps are never
        available. Arguments and exact contracts are in{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/docs/mcp.md">docs/mcp.md</a> and{" "}
        <a href="https://github.com/idestis/eddy/blob/HEAD/docs/api.md">docs/api.md</a>.
      </p>

      <h2 id="threads">Threads</h2>
      <p>
        Threads are plain text (at most 8 KiB) on a resource or a cluster. Those written over MCP carry a{" "}
        <code>via mcp</code> badge in the UI, so teammates can see what an assistant wrote, and reply or
        resolve them. See{" "}
        <Link to="/docs/using/" hash="threads">
          Using Eddy
        </Link>
        .
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
      <p>
        Claude Code calls <code>list_unhealthy</code>, then <code>get_resource</code> and{" "}
        <code>get_events</code> for each HelmRelease, then <code>create_thread</code> with a short diagnosis.
        To act on a finding, use an <code>operate</code> token and expect approval prompts.
      </p>

      <h2 id="protected">Protected clusters and confirm_cluster</h2>
      <p>
        On a cluster marked <code>protected: true</code>, every write tool needs <code>confirm_cluster</code>{" "}
        equal to the cluster name. Without it the tool returns an error that tells the assistant to ask you.
        Claude Code also asks for approval before it runs <code>suspend</code>, which is marked destructive.
        Treat the typed name as a speed bump: the real controls are your approval, your RBAC and the audit
        log. Operators can set <code>config.mcp.protectedClusters: deny</code> to block MCP writes there
        entirely.
      </p>

      <h2 id="guard">Guardrails</h2>
      <ul>
        <li>
          <strong>Your RBAC, always.</strong> Calls are impersonated as the token's owner, and group
          membership can only shrink after a token is issued.
        </li>
        <li>
          <strong>
            Bearer only, <code>/mcp</code> only.
          </strong>{" "}
          Tokens are refused everywhere else, and <code>/mcp</code> ignores cookies. The hub rejects a foreign{" "}
          <code>Origin</code> or <code>Host</code> and sends no CORS headers.
        </li>
        <li>
          <strong>Redaction and untrusted data.</strong> YAML and logs are redacted and marked as data, not
          instructions. That limits prompt injection but does not remove it, so review write actions before
          approving.
        </li>
        <li>
          <strong>Limits.</strong> 256 KiB requests, 64 KiB results, 60 calls a minute per token and 10 writes
          a minute.
        </li>
        <li>
          <strong>Audit.</strong> Every call is logged with <code>via: mcp</code>, the user, the token id, the
          tool and the target.
        </li>
        <li>
          <strong>Expiry.</strong> Tokens expire (30 days by default, at most 90 for local users and 30 for
          proxy, GitHub and OIDC users) and you can revoke them in the UI. Never commit a token: the{" "}
          <code>eddy_pat_</code> prefix lets secret scanners catch leaks.
        </li>
        <li>
          <strong>Kill switch.</strong> Operators can turn off <code>/mcp</code>, its writes or{" "}
          <code>get_logs</code> without a restart. See{" "}
          <Link to="/docs/operations/" hash="kill">
            Operations
          </Link>
          .
        </li>
      </ul>

      <h2 id="trouble">Troubleshooting</h2>
      <ul>
        <li>
          <strong>401 Unauthorized.</strong> The token expired, was revoked, or is sent to another path than{" "}
          <code>/mcp</code>. It only works as <code>Authorization: Bearer</code> on <code>/mcp</code>. After a
          hub restart with <code>store.driver: memory</code>, all tokens are gone.
        </li>
        <li>
          <strong>The server does not connect.</strong> The hub is not reachable from your machine (VPN), or
          your sign-in proxy sits in front of <code>/mcp</code>. Exempt it: MCP clients cannot do a browser
          login.
        </li>
        <li>
          <strong>A tool is refused.</strong> The token scope is too small, or the operator switched off{" "}
          <code>mcpEnabled</code>, <code>mcpWrites</code> or <code>mcpAllowLogs</code>.
        </li>
        <li>
          <strong>A tool says the cluster needs confirmation.</strong> Pass <code>confirm_cluster</code> with
          the cluster name.
        </li>
      </ul>
      <Pager current="/docs/mcp/" />
    </>
  );
}
