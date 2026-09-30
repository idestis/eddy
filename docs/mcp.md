# Using Eddy from Claude Code (MCP)

Eddy's hub exposes a [Model Context Protocol](https://modelcontextprotocol.io) endpoint at `/mcp`. Claude Code and other MCP clients can list your clusters, find unhealthy Flux resources across the fleet, read events, and leave review threads that show up in the Eddy UI. Everything runs as **you**: the tools see only what your Kubernetes RBAC allows.

## Connect Claude Code

1. In the Eddy UI, open your settings and create a personal access token (PAT). Choose the smallest scope you need:
   - `read`: read tools, plus creating and replying to threads.
   - `operate`: also reconcile, suspend and resume.
   
   Give it an expiry. The token (`eddy_pat_…`) is shown once, so copy it now.

2. Add the server. Keep the token in an environment variable so it stays out of shell history and config files:

   ```sh
   export EDDY_PAT=eddy_pat_...
   claude mcp add --transport http eddy https://eddy.internal.example.com/mcp \
     --header "Authorization: Bearer $EDDY_PAT"
   ```

   `claude mcp add` takes the server name first, then the URL (check `claude mcp add --help` if your version differs). Add `--scope user` to make it available in every project, or `--scope project` to write a shared `.mcp.json`. For a shared file, reference the variable instead of the token:

   ```json
   {
     "mcpServers": {
       "eddy": {
         "type": "http",
         "url": "https://eddy.internal.example.com/mcp",
         "headers": {"Authorization": "Bearer ${EDDY_PAT}"}
       }
     }
   }
   ```

3. Run `claude mcp list` (or `/mcp` inside Claude Code) and check that `eddy` is connected.

The hub address must be reachable from your machine (usually VPN). `/mcp` does not go through your sign-in proxy. The token is the credential.

## Tools

Read tools (scope `read`):

| Tool | Arguments | Notes |
|---|---|---|
| `list_clusters` | | Connection state and health counts |
| `list_resources` | `cluster?, kind?, namespace?, status?, query?, limit?` | Fleet-wide when `cluster` is empty |
| `list_unhealthy` | `cluster?` | Failed or suspended Flux objects across the fleet |
| `get_resource` | `cluster, kind, namespace, name, include_yaml?` | Summary plus redacted YAML. Secrets and ConfigMaps are never available |
| `get_events` | `cluster, kind, namespace, name` | |
| `get_logs` | `cluster, namespace, pod, container?, tail?` | Off unless the operator sets `mcp.allowLogs`. Needs `pods/log` RBAC |
| `list_threads` | `cluster?, kind?, namespace?, name?, status?` | Only threads on resources you can see |
| `get_thread` | `id` | |

Thread tools (scope `read`, or `operate` if the operator set `auth.tokens.threadWriteScope: operate`):

| Tool | Arguments |
|---|---|
| `create_thread` | `cluster, kind?, namespace?, name?, title, body` |
| `reply_thread` | `id, body` |
| `resolve_thread` | `id` |

Threads are plain text (at most 8 KiB) and carry a `via mcp` badge in the UI, so teammates can see what an assistant wrote.

Action tools (scope `operate`, and `mcp.writes` enabled):

| Tool | Arguments |
|---|---|
| `reconcile` | `cluster, kind, namespace, name, with_source?, confirm_cluster?` |
| `suspend` | `cluster, kind, namespace, name, confirm_cluster?` |
| `resume` | `cluster, kind, namespace, name, confirm_cluster?` |

The exact contract is in [api.md](api.md).

## Protected clusters

Clusters marked `protected: true` (usually production) need `confirm_cluster` equal to the cluster name on every action. Without it, the tool returns an error that tells the assistant to ask you. Claude Code also asks for approval before it runs `suspend`, which is marked destructive. Treat the typed name as a speed bump: the real controls are your approval, your RBAC and the audit log. Operators can set `mcp.protectedClusters: deny` to block MCP writes to protected clusters entirely.

## Example: a fleet review with threads

With a `read` token, try:

> Use the eddy tools to review every failing HelmRelease across the fleet. For each one, read its events and summary, work out the likely cause, and leave a thread on the resource with the cause and the next step. Don't change anything.

Claude Code will:

1. Call `list_unhealthy` to find failed and suspended objects in all clusters.
2. Call `get_resource` and `get_events` for each HelmRelease.
3. Call `create_thread` on each one with a short diagnosis.

Your team then sees those threads on the resources in the Eddy UI, marked `via mcp`, and can reply or resolve them. A follow-up such as "reply to my open threads on prod-eu with what changed since" uses `list_threads` and `reply_thread`. To act on a finding ("reconcile the ones that failed on a transient error"), use an `operate` token and expect approval prompts.

## Guardrails

- **Your RBAC, always.** Calls are impersonated as the token's owner, and group membership can only shrink after a token is issued. The token can never do more than you can.
- **Bearer only, `/mcp` only.** PATs are refused everywhere else, and `/mcp` ignores cookies. The hub rejects foreign `Origin` and `Host` headers and sends no CORS headers.
- **Redaction and untrusted data.** Resource YAML and logs are redacted, and cluster content is marked as data, not instructions. That limits prompt injection from things like annotations or log lines, but does not remove it, so review write actions before approving them.
- **Limits.** 256 KiB requests, 64 KiB results, 60 calls a minute per token and 10 writes a minute.
- **Audit.** Every call is logged with `via: mcp`, your user, the token id, the tool and the target.
- **Expiry.** Tokens expire (30 days by default) and you can revoke them in the UI. Never commit a token. The `eddy_pat_` prefix lets secret scanners catch leaks.

## Kill switch

Operators can stop MCP instantly without a restart by editing the `eddy-runtime` ConfigMap:

- `mcpEnabled: false` turns `/mcp` off.
- `mcpWrites: false` keeps reads and threads but refuses reconcile, suspend and resume.
- `mcpAllowLogs: false` disables `get_logs`.

See [install.md](install.md#7-kill-switches) for the command.
