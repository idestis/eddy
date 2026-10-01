import { createFileRoute, Link } from "@tanstack/react-router";
import { CodeBlock } from "../../components/CodeBlock";
import { FullRef } from "../../components/FullRef";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/operations")({
  head: () =>
    pageHead({
      title: "Operations · Eddy docs",
      description:
        "Run Eddy day to day: upgrades and migrations, backups, runtime kill switches, the audit log, health and metrics endpoints, and uninstalling.",
      path: "/docs/operations/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Operations</h1>
      <p className="lead">
        Day-to-day tasks for a running install: upgrading, backing up, switching features off, reading the
        audit log, watching health and removing Eddy.
      </p>
      <FullRef
        path="docs/install.md#9-backup-upgrade-uninstall"
        label="docs/install.md, backup, upgrade, uninstall"
      />

      <h2 id="upgrade">Upgrade</h2>
      <p>
        Upgrade the hub first, then each agent to the same chart version. The hub runs its database migrations
        itself when it starts, under a PostgreSQL advisory lock, so replicas that start together are fine.
        Migrations only move forward, and the first new replica applies them.
      </p>
      <ol>
        <li>
          Read the release notes for the version, then <strong>apply CRD changes first</strong>. Helm installs
          the <code>Cluster</code> resource definition from a chart's <code>crds/</code> directory on first
          install but never upgrades it:
          <CodeBlock
            code={`
$ helm pull oci://ghcr.io/idestis/charts/eddy-hub --version <new> --untar --untardir /tmp/eddy
$ kubectl apply -f /tmp/eddy/eddy-hub/crds/
`}
          />
        </li>
        <li>
          Upgrade the hub with the same values file:
          <CodeBlock
            code={`
$ helm upgrade eddy-hub oci://ghcr.io/idestis/charts/eddy-hub --version <new> \\
    --namespace eddy -f hub-values.yaml
`}
          />
        </li>
        <li>
          Upgrade each agent with the same chart version, and the values you installed it with:
          <CodeBlock
            code={`
$ helm upgrade eddy-agent oci://ghcr.io/idestis/charts/eddy-agent --version <new> \\
    --kube-context prod-eu --namespace eddy-system --reuse-values
`}
          />
        </li>
      </ol>
      <p>
        The hub rolls one replica at a time, so the UI stays up. Agents of the replica being replaced
        reconnect to another within seconds, and with two agent replicas the standby keeps the cluster
        connected. Sessions and tokens survive, because they are in PostgreSQL.
      </p>
      <h3 id="upgrade-verify">Verify</h3>
      <CodeBlock
        lines={[
          "$ kubectl -n eddy rollout status deploy/eddy-hub",
          "$ kubectl --context mgmt get clusters",
          "$ kubectl -n eddy logs deploy/eddy-hub",
        ]}
      />

      <h2 id="backup">Backups</h2>
      <p>
        All hub state is in the PostgreSQL database: personal access tokens, threads, audit events and
        preferences. Sessions and rate-limit counters are there too, but they are throwaway. Cluster
        definitions and agent tokens live in Kubernetes, which in practice means your Helm values and the
        token Secrets. The database holds emails and message text, and no credentials: token secrets are
        stored only as HMACs.
      </p>
      <ul>
        <li>
          <strong>CloudNativePG:</strong> a <code>ScheduledBackup</code> with barman to object storage, or
          volume snapshots.
        </li>
        <li>
          <strong>RDS, Aurora, Cloud SQL:</strong> their automated backups and point-in-time recovery.
        </li>
        <li>
          <strong>Anywhere:</strong>
          <CodeBlock lines={['$ pg_dump --format=custom "$DSN" > eddy.dump']} />
          Restore with <code>pg_restore</code>. The <code>UNLOGGED</code> tables are dumped too, and restoring
          them is harmless.
        </li>
      </ul>
      <p>
        Backups contain personal data (emails, message text), so encrypt them and restrict access. Also keep a
        copy of your Helm values and any Secrets you create by hand, such as the credentials Secret and the
        session key.
      </p>

      <h2 id="kill">Runtime kill switches</h2>
      <p>
        Four flags switch a feature off without a restart. They live in the <code>eddy-runtime</code>{" "}
        ConfigMap, under the key <code>flags.yaml</code>, which is mounted into the hub. The hub re-reads the
        file every 30 seconds and checks the flags on every request. Kubernetes takes up to a minute to
        propagate ConfigMap edits, so expect about 90 seconds in total.
      </p>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Flag</th>
              <th scope="col">When false</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">
                <code>aiEnabled</code>
              </th>
              <td>Ask AI returns 503 and its button disappears</td>
            </tr>
            <tr>
              <th scope="row">
                <code>mcpEnabled</code>
              </th>
              <td>
                <code>/mcp</code> refuses all calls
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>mcpWrites</code>
              </th>
              <td>MCP reconcile, suspend and resume are refused. Reads still work</td>
            </tr>
            <tr>
              <th scope="row">
                <code>mcpAllowLogs</code>
              </th>
              <td>
                The MCP <code>get_logs</code> tool is refused
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <ol>
        <li>
          <strong>Flip a switch now:</strong>
          <CodeBlock
            code={`
$ kubectl -n eddy patch configmap eddy-runtime --type merge \\
    -p '{"data":{"flags.yaml":"aiEnabled: false\\nmcpEnabled: true\\nmcpWrites: false\\nmcpAllowLogs: false\\n"}}'
`}
          />
        </li>
        <li>
          <strong>Make it stick:</strong> the chart owns this ConfigMap, so the next <code>helm upgrade</code>{" "}
          resets a manual patch. Set the same values in <code>hub-values.yaml</code>:
          <CodeBlock
            code={`
runtimeFlags:
  aiEnabled: false
  mcpEnabled: true
  mcpWrites: false
  mcpAllowLogs: false
`}
          />
        </li>
      </ol>
      <p>
        A flag can only turn something off. It cannot enable what <code>config.*</code> disables (for example{" "}
        <code>config.ai.enabled: false</code> stays off whatever <code>aiEnabled</code> says). A missing file
        or key keeps the default.
      </p>

      <h2 id="audit">Audit log</h2>
      <ul>
        <li>
          Every write, MCP call and Ask AI step is logged as a JSON line on the hub's standard output, with
          user, groups, <code>via</code> (<code>web</code>, <code>mcp</code> or <code>askai</code>), cluster,
          target and result. Ship the hub logs to your log store.
        </li>
        <li>
          A copy is kept in PostgreSQL for 90 days by default (<code>store.retention.auditDays</code>).
        </li>
        <li>
          In the UI, press <kbd>g</kbd> <kbd>a</kbd> for your own events. Users in a group listed in{" "}
          <code>config.auth.auditViewerGroups</code> (prefixed, for example <code>eddy:platform</code>) can
          see other people's.
        </li>
        <li>
          Because actions are impersonated, each cluster's own audit log also records the real user. Alert on
          impersonation by the agent's ServiceAccount in those logs if you want a second view.
        </li>
        <li>
          To cut off a person at once, end every session and token they hold:
          <CodeBlock
            lines={[
              "$ kubectl -n eddy exec deploy/eddy-hub -- /eddy-hub admin revoke --user alice@example.com",
            ]}
          />
        </li>
      </ul>

      <h2 id="monitoring">Monitoring</h2>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Endpoint</th>
              <th scope="col">Where</th>
              <th scope="col">Use</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">
                <code>/readyz</code>
              </th>
              <td>Hub metrics port, 9090</td>
              <td>Readiness. Names the missing peer link or unsynced cluster when not ready</td>
            </tr>
            <tr>
              <th scope="row">
                <code>/metrics</code>
              </th>
              <td>Hub metrics port, 9090</td>
              <td>Prometheus metrics</td>
            </tr>
            <tr>
              <th scope="row">
                <code>/healthz</code>
              </th>
              <td>Hub UI port 8080 and agent port 8443</td>
              <td>Liveness, and the ALB health check path</td>
            </tr>
            <tr>
              <th scope="row">
                <code>/healthz</code>, <code>/readyz</code>
              </th>
              <td>
                Agent health port, 8081 (<code>healthPort</code>)
              </td>
              <td>The agent's probes</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        Cluster state is visible too: <code>kubectl get clusters</code> (short name <code>ecl</code>) shows
        each cluster's phase. Logs from both components are JSON on standard output. Set{" "}
        <code>networkPolicy.metrics.from</code> to your Prometheus namespace if you turn the hub's network
        policy on.
      </p>

      <h2 id="uninstall">Uninstall</h2>
      <ol>
        <li>
          Remove each agent, then the hub:
          <CodeBlock
            lines={[
              "$ helm uninstall eddy-agent -n eddy-system --kube-context prod-eu",
              "$ helm uninstall eddy-hub -n eddy",
            ]}
          />
        </li>
        <li>
          The database, the signing-key Secret and the CRD remain on purpose. To remove everything, drop the
          database (or delete the CloudNativePG <code>Cluster</code>), then:
          <CodeBlock
            lines={[
              "$ kubectl -n eddy delete secret eddy-hub-key",
              "$ kubectl delete crd clusters.gitops.eddy.dev",
            ]}
          />
          Deleting the CRD removes every <code>Cluster</code> resource with it.
        </li>
      </ol>
      <p>
        Next: review the <Link to="/docs/security/">security model</Link>.
      </p>
      <Pager current="/docs/operations/" />
    </>
  );
}
