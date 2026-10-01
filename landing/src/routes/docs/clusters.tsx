import { createFileRoute, Link } from "@tanstack/react-router";
import { CodeBlock } from "../../components/CodeBlock";
import { FullRef } from "../../components/FullRef";
import { Kbd } from "../../components/Kbd";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/clusters")({
  head: () =>
    pageHead({
      title: "Add clusters · Eddy docs",
      description:
        "Connect workload clusters to the Eddy hub: the Add cluster wizard or declarative clusters[], the eddy-agent chart, presets, replicas, protected clusters and network needs.",
      path: "/docs/clusters/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Add clusters</h1>
      <p className="lead">
        A workload cluster joins by running one agent, which dials out to the hub. There are two ways to
        register it: the <strong>Add cluster</strong> wizard in the UI, or the <code>clusters[]</code> list in
        your Helm values. Both create a <code>Cluster</code> resource in the management cluster and a token
        Secret named <code>eddy-agent-&lt;name&gt;</code> in the hub's namespace. You can mix them freely.
      </p>
      <FullRef path="docs/agent.md" label="docs/agent.md and docs/install.md" />

      <h2 id="wizard">Option A: the Add cluster wizard</h2>
      <p>
        With <code>onboarding.enabled: true</code> (the default), a user who may create{" "}
        <code>clusters.gitops.eddy.dev</code> in the <strong>management</strong> cluster sees{" "}
        <strong>Add cluster</strong> on the fleet page (press <Kbd>n</Kbd>).
      </p>
      <ol>
        <li>
          <strong>Allow your platform group</strong> to manage clusters. The hub asks the management cluster,
          as the signed-in user, whether they may create, update, delete and get{" "}
          <code>clusters.gitops.eddy.dev</code>. The hub chart grants that to the groups and users you list in{" "}
          <code>hub-values.yaml</code>, and to nobody by default:
          <CodeBlock
            code={`
onboarding:
  admins:
    groups: [eddy:platform]   # or a GitHub team, e.g. eddy:github:acme/platform
`}
          />
          Then run <code>helm upgrade</code>. The chart creates the ClusterRole{" "}
          <code>eddy-cluster-admin</code> (get, list, watch, create, update, patch and delete on clusters) and
          binds it to those subjects in the <strong>management</strong> cluster. Groups must start with{" "}
          <code>eddy:</code> (<code>config.auth.groups.prefix</code>), and <code>system:</code> subjects are
          refused. The install notes say who is bound. With <code>rbac.create: false</code>, create an
          equivalent ClusterRole and ClusterRoleBinding yourself.
        </li>
        <li>
          <strong>Fill in the form:</strong> a name (a DNS label such as <code>prod-eu</code>), display name,
          environment, region, colour, protection and order. The hub creates the <code>Cluster</code> (phase{" "}
          <code>Pending</code>), its token Secret and a <strong>one-time join token</strong>, valid for one
          hour by default (<code>onboarding.joinTokenTTL</code>, at most 24 hours).
        </li>
        <li>
          <strong>Copy the install command</strong> from the Connect screen. It comes in three forms: a{" "}
          <code>helm</code> command, a <code>values.yaml</code>, and plain manifests for GitOps-managed
          clusters. Run it against the <strong>workload</strong> cluster. The plain manifests do not include
          the user roles <code>eddy-viewer</code> and <code>eddy-operator</code>, so apply{" "}
          <code>deploy/rbac/eddy-user-rbac.yaml</code> as well (see{" "}
          <Link to="/docs/access/" hash="raw">
            Access and RBAC
          </Link>
          ).
        </li>
        <li>
          <strong>Watch the live checklist.</strong> It ticks as the agent connects: agent connected, protocol
          compatible, Flux detected, informers synced, SubjectAccessReview working, impersonation pinned and
          the watched namespaces. Rejected attempts (a bad, expired or used join token, the wrong cluster, a
          protocol mismatch) are listed with the reason. Click <strong>Finish</strong> when done.
        </li>
      </ol>
      <p>
        The agent trades the join token for a permanent token on its first connection and stores it in its own
        Secret, so the join token is then worthless. Later, the cluster card's <strong>Connection</strong>{" "}
        panel shows the same checklist, with <strong>Regenerate join token</strong> and{" "}
        <strong>Delete</strong> (a typed confirmation on protected clusters).
      </p>
      <p>
        Set <code>agentsPublicURL</code> in the hub values, so the guide shows the address agents really dial.
        With <code>onboarding.enabled: false</code> the button is hidden and the hub keeps read-only RBAC on
        the management cluster.
      </p>

      <h2 id="declarative">Option B: declare clusters in Helm values</h2>
      <p>
        List each workload cluster in the hub's <code>hub-values.yaml</code> and upgrade. Colour is a hex
        string, <code>environment</code> is free text shown on the cluster, and <code>protected: true</code>{" "}
        makes writes need a typed confirmation of the cluster name.
      </p>
      <CodeBlock
        code={`
clusters:
  - name: prod-eu
    displayName: prod-eu
    environment: Production
    region: eu-central-1
    color: "#C2410C"
    protected: true
    order: 1
  - name: staging
    environment: Staging
    color: "#0F766E"
    order: 2
`}
      />
      <CodeBlock
        code={`
$ helm upgrade eddy-hub oci://ghcr.io/idestis/charts/eddy-hub --version 1.0.0 \\
    --namespace eddy -f hub-values.yaml
`}
      />
      <p>
        For each entry the chart creates a <code>Cluster</code> and a random token Secret, and keeps both
        across upgrades. To bring your own token Secret (key <code>token</code>), set{" "}
        <code>existingTokenSecret</code> on the entry. Under Argo CD this is the way to go, because the chart
        cannot generate a token there.
      </p>
      <p>
        <code>helm upgrade</code> prints the exact agent install command for each cluster in its notes (
        <code>helm -n eddy get notes eddy-hub</code>).
      </p>

      <h2 id="agent">Install the agent</h2>
      <p>
        The agent runs in its own namespace, <code>eddy-system</code>, not in <code>flux-system</code>, so
        Flux stays untouched and the agent's RBAC and policies are isolated. Run these steps against each{" "}
        <strong>workload</strong> cluster.
      </p>
      <ol>
        <li>
          Create the namespace and apply the Pod Security label (the agent meets the <code>restricted</code>{" "}
          profile):
          <CodeBlock
            code={`
$ kubectl --context prod-eu create namespace eddy-system
$ kubectl --context prod-eu label namespace eddy-system pod-security.kubernetes.io/enforce=restricted
`}
          />
        </li>
        <li>
          Write <code>agent-values.yaml</code>. The <code>impersonation</code> and <code>userRBAC</code>{" "}
          blocks decide who sees and does what here; <Link to="/docs/access/">Access and RBAC</Link> explains
          them:
          <CodeBlock
            code={`
cluster:
  name: prod-eu            # must equal the Cluster name in the hub

hub:
  url: wss://eddy-agents.internal.example.com/agent/v1/connect   # wss:// only
  # caBundle: |            # PEM, only if the hub certificate is not publicly trusted
  #   -----BEGIN CERTIFICATE-----

watch:
  namespaces: []           # empty = the whole cluster
  presets: [karpenter, externalSecrets]   # optional, see below

impersonation:
  groups: [eddy:authenticated, eddy:github:acme/platform]   # pin group impersonation (recommended)

userRBAC:
  operator:
    groups: ["eddy:github:acme/platform"]   # who may reconcile, suspend and resume here

replicaCount: 2
`}
          />
        </li>
        <li>
          Install with the join token from the wizard:
          <CodeBlock
            code={`
$ helm install eddy-agent oci://ghcr.io/idestis/charts/eddy-agent --version 1.0.0 \\
    --kube-context prod-eu --namespace eddy-system \\
    -f agent-values.yaml \\
    --set-string joinToken=eddy_join_...
`}
          />
          For a cluster from <code>clusters[]</code>, pass the agent token instead, taken from the hub
          cluster. Prefer <code>token.existingSecret</code> under GitOps:
          <CodeBlock
            code={`
$ helm install eddy-agent oci://ghcr.io/idestis/charts/eddy-agent --version 1.0.0 \\
    --kube-context prod-eu --namespace eddy-system \\
    -f agent-values.yaml \\
    --set-string token.value="$(kubectl --context mgmt -n eddy get secret eddy-agent-prod-eu -o jsonpath='{.data.token}' | base64 -d)"
`}
          />
        </li>
      </ol>

      <h3 id="values">Agent values worth knowing</h3>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Value</th>
              <th scope="col">Purpose</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">
                <code>cluster.name</code>
              </th>
              <td>Must equal the Cluster name in the hub</td>
            </tr>
            <tr>
              <th scope="row">
                <code>hub.url</code>, <code>hub.caBundle</code>
              </th>
              <td>
                The agent endpoint (<code>wss://</code> only) and an optional CA for a private certificate (
                <code>--set-file hub.caBundle=ca.pem</code>)
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>joinToken</code>
              </th>
              <td>
                The one-time <code>eddy_join_…</code> token. The chart adds a Role that lets the agent{" "}
                <code>get</code> and <code>update</code> that one Secret, where it stores its permanent token
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>token.existingSecret</code>, <code>token.value</code>
              </th>
              <td>A pre-provisioned agent token instead of a join token</td>
            </tr>
            <tr>
              <th scope="row">
                <code>watch.namespaces</code>
              </th>
              <td>Limit what the agent watches. Empty means the whole cluster</td>
            </tr>
            <tr>
              <th scope="row">
                <code>watch.presets</code>
              </th>
              <td>
                <code>karpenter</code> and <code>externalSecrets</code>
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>impersonation.groups</code>
              </th>
              <td>Pin impersonation to an exact list of groups. Include eddy:authenticated</td>
            </tr>
            <tr>
              <th scope="row">
                <code>userRBAC.*</code>
              </th>
              <td>
                Who may read and act in this cluster: <code>viewer.groups</code> (default{" "}
                <code>eddy:authenticated</code>), <code>operator.groups</code>, <code>namespaces</code> and{" "}
                <code>bindings</code>. See <Link to="/docs/access/">Access and RBAC</Link>
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>replicaCount</code>
              </th>
              <td>
                Default 2. Every replica connects, and the hub uses the oldest and keeps the others as hot
                standbys
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>limits.*</code>
              </th>
              <td>
                Control-plane protection for the whole Deployment: <code>qps</code> (20), <code>burst</code>{" "}
                (40), <code>concurrency</code> (16), <code>logStreams</code> (8), <code>sarConcurrency</code>{" "}
                (8). The chart divides them by <code>replicaCount</code>
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>networkPolicy.enabled</code>
              </th>
              <td>
                On by default: egress to DNS, the Kubernetes API and the hub only. Set <code>hubTo</code> to
                the hub endpoint's CIDR
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <h3 id="presets">Presets</h3>
      <p>
        Presets are opt-in groups of extra kinds. Each adds its kinds to the agent's cache, only for kinds the
        cluster serves, and read access on them to the agent's ClusterRole. Unknown preset names fail the
        chart.
      </p>
      <ul>
        <li>
          <code>karpenter</code>: NodePool and NodeClaim (<code>karpenter.sh</code>) and EC2NodeClass (
          <code>karpenter.k8s.aws</code>).
        </li>
        <li>
          <code>externalSecrets</code>: ExternalSecret, ClusterExternalSecret, SecretStore, ClusterSecretStore
          and PushSecret (<code>external-secrets.io</code>). The Secrets they produce are never read.
        </li>
      </ul>
      <p>
        People need matching read RBAC to see these kinds. The example roles in{" "}
        <Link to="/docs/access/">Access and RBAC</Link> already include them.
      </p>

      <h3 id="protected">Protected clusters, environment and colour</h3>
      <p>
        <code>environment</code> (for example Production or Staging) and <code>color</code> label the cluster
        everywhere in the UI, so a production cluster is hard to mistake for staging.{" "}
        <code>protected: true</code> adds a server-checked typed confirmation to every write on that cluster.
        See{" "}
        <Link to="/docs/access/" hash="protected">
          Access and RBAC
        </Link>
        .
      </p>

      <h2 id="network">Network requirements</h2>
      <ul>
        <li>
          <strong>Outbound only.</strong> The agent needs TCP 443 to the hub's agent endpoint, plus DNS and
          the Kubernetes API. It accepts <strong>no inbound connections</strong>, and has no Service.
        </li>
        <li>
          <strong>The hub</strong> must accept TCP 443 on the agent endpoint only, from your agent networks.
          The UI endpoint is a separate listener and hostname.
        </li>
        <li>
          Behind an egress proxy, the agent honours <code>HTTPS_PROXY</code> and <code>NO_PROXY</code> (set
          through <code>extraEnv</code>). Keep the Kubernetes API in <code>NO_PROXY</code>.
        </li>
        <li>
          The connection is one long-lived WebSocket with a ping every 20 seconds, so any load balancer idle
          timeout above that works; 3600 seconds is recommended, as in{" "}
          <Link to="/docs/install/" hash="alb">
            Install
          </Link>
          . Reconnects use jittered backoff.
        </li>
      </ul>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Where the workload cluster is</th>
              <th scope="col">Path to the agent endpoint</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">Same VPC as the hub</th>
              <td>Internal NLB or ALB, security group allowing 443 from node or pod CIDRs</td>
            </tr>
            <tr>
              <th scope="row">Other VPC, same account</th>
              <td>VPC peering or Transit Gateway</td>
            </tr>
            <tr>
              <th scope="row">Other AWS account</th>
              <td>PrivateLink: the agent NLB behind a VPC endpoint service, an interface endpoint per VPC</td>
            </tr>
            <tr>
              <th scope="row">Other cloud, on-prem, a laptop</th>
              <td>
                A public endpoint restricted by source CIDR (TLS plus token), or a tunnel such as Tailscale or
                Cloudflare Tunnel
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <h2 id="verify">Verify</h2>
      <ol>
        <li>
          Within about a minute the cluster shows as <code>Connected</code> in the UI and on the management
          cluster:
          <CodeBlock lines={["$ kubectl --context mgmt get clusters.gitops.eddy.dev"]} />
        </li>
        <li>
          Read the agent log in the workload cluster:
          <CodeBlock
            lines={[
              "$ kubectl --context prod-eu -n eddy-system get pods",
              "$ kubectl --context prod-eu -n eddy-system logs deploy/eddy-agent",
            ]}
          />
        </li>
        <li>
          Check the wizard's checklist or the cluster's Connection panel. Impersonation should read as pinned,
          and the Flux kinds as synced.
        </li>
        <li>
          People see a cluster only through RBAC. The chart's default <code>userRBAC</code> lets every
          signed-in user read it. Decide who may operate in <Link to="/docs/access/">Access and RBAC</Link>.
        </li>
      </ol>

      <h2 id="trouble">Troubleshooting</h2>
      <ul>
        <li>
          <strong>
            The cluster stays <code>Pending</code>.
          </strong>{" "}
          Open its Connection panel. Rejected attempts say why: an expired or used join token (regenerate
          one), a token for another cluster, or a protocol mismatch (upgrade the agent). No attempts at all
          means the agent never reached the hub: check <code>hub.url</code>, DNS, and outbound 443.
        </li>
        <li>
          <strong>
            The cluster stays <code>Disconnected</code>.
          </strong>{" "}
          Read the agent log. Usual causes are a wrong <code>hub.url</code>, a certificate the agent does not
          trust (set <code>hub.caBundle</code>), a token that does not match the{" "}
          <code>eddy-agent-&lt;name&gt;</code> Secret, or an ingress that closes idle WebSockets.
        </li>
        <li>
          <strong>The cluster is connected but the UI is empty.</strong> The user has no RBAC in that cluster,
          for example because <code>userRBAC.viewer.groups</code> was set to an empty list.
        </li>
        <li>
          <strong>A GitOps controller keeps removing the permanent token.</strong> The agent writes it under
          the <code>token</code> key of its own Secret. Tell the controller to ignore that key (for example an
          Argo CD <code>ignoreDifferences</code> on <code>/data/token</code>).
        </li>
      </ul>
      <Pager current="/docs/clusters/" />
    </>
  );
}
