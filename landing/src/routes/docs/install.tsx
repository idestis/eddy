import { createFileRoute, Link } from "@tanstack/react-router";
import { CodeBlock } from "../../components/CodeBlock";
import { FullRef } from "../../components/FullRef";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/install")({
  head: () =>
    pageHead({
      title: "Install the hub · Eddy docs",
      description:
        "Install the Eddy hub with Helm: PostgreSQL, secrets, a minimal values file, internal ingress for nginx and AWS ALB, and how to check it.",
      path: "/docs/install/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Install the hub</h1>
      <p className="lead">
        This page installs the hub in your management cluster with Helm. It ends with a hub that is running
        and reachable. Sign-in, clusters and access come on the next pages.
      </p>
      <FullRef path="docs/install.md" />

      <h2 id="prereq">Prerequisites</h2>
      <ul>
        <li>Kubernetes 1.27 or later in every cluster, with Flux installed in the workload clusters.</li>
        <li>
          Helm 3.12 or later (Helm 4 works), and <code>kubectl</code> access to the management cluster.
        </li>
        <li>PostgreSQL 14 or later for the hub (next section). It needs no extensions.</li>
        <li>
          An <strong>internal</strong> ingress (nginx or an AWS ALB) and a TLS certificate for the UI, and a
          second internal hostname for agents. Both are covered below.
        </li>
        <li>
          A DNS name for each, for example <code>eddy.internal.example.com</code> and{" "}
          <code>eddy-agents.internal.example.com</code>.
        </li>
      </ul>

      <h2 id="namespace">1. Namespace</h2>
      <p>The hub, its database Secret and its credentials all live in one namespace:</p>
      <CodeBlock lines={["$ kubectl create namespace eddy"]} />

      <h2 id="postgres">2. PostgreSQL</h2>
      <p>
        The hub keeps its own data in PostgreSQL: sessions, personal access tokens, threads, audit events and
        preferences, plus the state replicas share. Cluster state is never stored there. The hub applies its
        migrations when it starts, so an empty database is enough. Pick one option.
      </p>
      <h3 id="cnpg">Option A: CloudNativePG</h3>
      <p>
        Install the <a href="https://cloudnative-pg.io">CloudNativePG operator</a> if the cluster does not
        have it yet:
      </p>
      <CodeBlock
        code={`
$ kubectl apply --server-side -f \\
    https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v1.30.1/cnpg-1.30.1.yaml
$ kubectl -n cnpg-system rollout status deploy/cnpg-controller-manager
`}
      />
      <p>
        Then create a <code>Cluster</code> in the hub's namespace. One instance is enough to start, and two
        give you a standby.
      </p>
      <CodeBlock
        code={`
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: eddy-db
  namespace: eddy
spec:
  instances: 2
  imageName: ghcr.io/cloudnative-pg/postgresql:17
  storage:
    size: 5Gi
  bootstrap:
    initdb:
      database: eddy
      owner: eddy
`}
      />
      <p>
        CloudNativePG creates a Secret named <code>eddy-db-app</code>. Its <code>uri</code> key is a complete
        connection string, so the hub values in step 4 point at it and you do not create a DSN Secret
        yourself.
      </p>
      <h3 id="rds">Option B: RDS, Aurora, Cloud SQL or any PostgreSQL</h3>
      <ol>
        <li>
          Create a database and a role that owns it:
          <CodeBlock
            code={`
CREATE ROLE eddy LOGIN PASSWORD 'change-me';
CREATE DATABASE eddy OWNER eddy;
`}
          />
        </li>
        <li>
          Put the connection string in a Secret in the hub's namespace. Use <code>sslmode=verify-full</code>{" "}
          outside the cluster:
          <CodeBlock
            code={`
$ kubectl -n eddy create secret generic eddy-db \\
    --from-literal=dsn='postgres://eddy:change-me@eddy.abc123.eu-central-1.rds.amazonaws.com:5432/eddy?sslmode=verify-full'
`}
          />
        </li>
        <li>
          In the values file you will write below, set the Secret name and key:
          <CodeBlock code={`store:\n  postgres:\n    dsnSecret: {name: eddy-db, key: dsn}`} />
        </li>
      </ol>
      <p>
        Each hub replica opens up to <code>store.postgres.maxOpenConns</code> connections (default 10) plus
        one to listen for events. Allow for that on the database side.
      </p>
      <div className="callout">
        <p>
          <code>store.driver: memory</code> needs no database, but it runs one replica and loses every
          session, token and thread on restart. It is for a quick evaluation only.
        </p>
      </div>

      <h2 id="secrets">3. Secrets</h2>
      <p>
        One credentials Secret holds everything secret that the hub reads from its environment. The chart
        loads it with <code>credentialsSecret</code>, so no secret ever goes into Helm values.
      </p>
      <ol>
        <li>
          Create the credentials Secret, empty for now:
          <CodeBlock lines={["$ kubectl -n eddy create secret generic eddy-credentials"]} />
          The later pages add keys to it as you need them. The key names are fixed by the config:{" "}
          <code>GITHUB_CLIENT_SECRET</code> for <Link to="/docs/sign-in/">GitHub sign-in</Link>,{" "}
          <code>OIDC_&lt;ID&gt;_CLIENT_SECRET</code> for each OIDC provider, <code>EDDY_PROXY_SECRET</code>{" "}
          for the trusted proxy and <code>ANTHROPIC_API_KEY</code> for <Link to="/docs/ask-ai/">Ask AI</Link>.
        </li>
        <li id="add-key">
          <strong>Add a key to eddy-credentials.</strong> Every page uses this command. It adds or replaces
          one key and keeps the others, and <code>read -rs</code> keeps the value out of your shell history.
          Change the key name:
          <CodeBlock
            code={`
$ read -rs VALUE
$ kubectl -n eddy patch secret eddy-credentials --type merge \\
    -p "{\\"stringData\\":{\\"GITHUB_CLIENT_SECRET\\":\\"$VALUE\\"}}"
$ unset VALUE
`}
          />
          The hub reads the Secret when it starts. If it is already running, restart it afterwards:{" "}
          <code>kubectl -n eddy rollout restart deploy/eddy-hub</code>.
        </li>
        <li>
          <strong>Session key (optional).</strong> The chart generates the signing key on first install and
          keeps it across upgrades. With <code>helm template</code> or Argo CD it cannot, so create the Secret
          yourself and name it in <code>sessionKeySecret</code>. The key is called <code>key</code> and must
          hold at least 32 random bytes:
          <CodeBlock
            code={`
$ kubectl -n eddy create secret generic eddy-session-key \\
    --from-literal=key="$(openssl rand -base64 48)"
`}
          />
        </li>
        <li>
          <strong>Local users (optional).</strong> Hash a password for each user as shown in{" "}
          <Link to="/docs/sign-in/" hash="local">
            Sign-in, local users
          </Link>
          , and paste the hash into <code>users.list</code> below.
        </li>
      </ol>

      <h2 id="values">4. Write hub-values.yaml</h2>
      <p>
        This is a complete minimal file: PostgreSQL from CloudNativePG, one local user, and both internal
        ingresses on nginx. Change the hostnames and the hash. Add real sign-in in{" "}
        <Link to="/docs/sign-in/">Sign-in</Link>.
      </p>
      <CodeBlock
        code={`
publicURL: https://eddy.internal.example.com
agentsPublicURL: https://eddy-agents.internal.example.com

credentialsSecret: eddy-credentials

users:
  list:
    - username: alice
      passwordHash: "$argon2id$v=19$m=65536,t=3,p=4$...$..."
      groups: [platform]        # becomes the Kubernetes group eddy:platform

store:
  postgres:
    dsnSecret: {name: eddy-db-app, key: uri}   # use {name: eddy-db, key: dsn} for option B

# onboarding:
#   admins:
#     groups: [eddy:platform]   # who may use the Add cluster wizard (see Add clusters)

ingress:
  ui:
    enabled: true
    className: nginx-internal
    hosts: [eddy.internal.example.com]
    annotations:
      nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
      nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
      nginx.ingress.kubernetes.io/proxy-buffering: "off"
    tls:
      - secretName: eddy-tls
        hosts: [eddy.internal.example.com]
  agents:
    enabled: true
    className: nginx-internal
    hosts: [eddy-agents.internal.example.com]
    annotations:
      nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
      nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
    tls:
      - secretName: eddy-agents-tls
        hosts: [eddy-agents.internal.example.com]
`}
      />
      <ul>
        <li>
          <code>publicURL</code> must be exactly the address people type in the browser, including the scheme.
          It drives the Origin check and the cookie security, and a mismatch breaks sign-in.
        </li>
        <li>
          <code>agentsPublicURL</code> is the address agents dial. The hub shows it in the Add cluster guide
          and in its install notes.
        </li>
        <li>
          Leave <code>credentialsSecret</code> out if you use none of its keys.
        </li>
      </ul>

      <h2 id="ingress">5. Internal ingress</h2>
      <p>
        The hub has two listeners: container port 8080 for the UI, API and MCP, and 8443 for the agent
        endpoint. Two <code>ClusterIP</code> Services front them: <code>eddy-hub</code> on port 80, and{" "}
        <code>eddy-hub-agents</code> on ports 443 and 80, both to 8443. Expose them on separate hostnames. The
        UI uses server-sent events and agents hold long WebSockets, so raise the idle timeouts. The agent
        ingress routes only <code>/agent/v1/connect</code> and skips your sign-in proxy on purpose, because
        agents authenticate with their token.
      </p>
      <h3 id="nginx">nginx</h3>
      <p>
        The values above already use an internal ingress class. Add a source restriction on the UI if you
        like:
      </p>
      <CodeBlock
        code={`
ingress:
  ui:
    annotations:
      nginx.ingress.kubernetes.io/whitelist-source-range: 10.0.0.0/8
`}
      />
      <h3 id="alb">AWS ALB (one internal ALB for both)</h3>
      <p>
        Two ingresses share one internal ALB through the same <code>group.name</code>. Both use the same
        annotations: the certificate has to cover both hostnames (or use a wildcard), and the health check
        path <code>/healthz</code> is served by the UI listener and by the agent listener, so both target
        groups pass.
      </p>
      <CodeBlock
        code={`
ingress:
  ui:
    enabled: true
    className: alb
    hosts: [eddy.internal.example.com]
    annotations:
      alb.ingress.kubernetes.io/group.name: eddy
      alb.ingress.kubernetes.io/scheme: internal
      alb.ingress.kubernetes.io/target-type: ip
      alb.ingress.kubernetes.io/listen-ports: '[{"HTTPS":443}]'
      alb.ingress.kubernetes.io/certificate-arn: arn:aws:acm:eu-central-1:111122223333:certificate/xxxx
      alb.ingress.kubernetes.io/load-balancer-attributes: idle_timeout.timeout_seconds=3600
      alb.ingress.kubernetes.io/healthcheck-path: /healthz
  agents:
    enabled: true
    className: alb
    hosts: [eddy-agents.internal.example.com]
    annotations:
      alb.ingress.kubernetes.io/group.name: eddy
      alb.ingress.kubernetes.io/scheme: internal
      alb.ingress.kubernetes.io/target-type: ip
      alb.ingress.kubernetes.io/listen-ports: '[{"HTTPS":443}]'
      alb.ingress.kubernetes.io/certificate-arn: arn:aws:acm:eu-central-1:111122223333:certificate/xxxx
      alb.ingress.kubernetes.io/load-balancer-attributes: idle_timeout.timeout_seconds=3600
      alb.ingress.kubernetes.io/healthcheck-path: /healthz
`}
      />
      <p>
        To put both on one hostname instead, set <code>ingress.agents.hosts</code> to the UI host and add{" "}
        <code>alb.ingress.kubernetes.io/group.order: "-1"</code> to the agents annotations. The ALB evaluates
        rules in <code>group.order</code>, then by ingress name, and <code>eddy-hub</code> (the UI,{" "}
        <code>/</code>) sorts before <code>eddy-hub-agents</code>, so without the order the UI rule would
        catch <code>/agent/v1/connect</code>.
      </p>
      <p>The network path, with the ports that matter:</p>
      <ol>
        <li>
          <strong>Browser</strong> to the ALB on 443, then to a hub pod on container port 8080 (UI, API, MCP).
        </li>
        <li>
          <strong>Agent</strong> to the ALB on 443 at{" "}
          <code>wss://eddy-agents.internal.example.com/agent/v1/connect</code>, then to a hub pod on container
          port 8443.
        </li>
        <li>
          <strong>Nothing connects into the workload clusters.</strong> The agent only dials out.
        </li>
      </ol>
      <ul>
        <li>
          8080 and 8443 are container ports only. Agents and browsers use 443. <code>ingress.agents</code>{" "}
          routes only <code>/agent/v1/connect</code>.
        </li>
        <li>
          Raise the idle timeout to 3600 seconds. The ALB default of 60 seconds drops WebSockets and SSE
          streams (agents reconnect on their own, but the UI would flicker).
        </li>
        <li>
          TLS ends at the ALB, so the hub logs one startup warning about serving plain HTTP behind a load
          balancer. That is expected.
        </li>
        <li>
          The ALB's security group must allow 443 from your workload clusters' NAT or VPC ranges (over peering
          or Transit Gateway). Limit the UI to your VPN range with the same group, or put the UI on a second
          ALB if the two need different sources.
        </li>
      </ul>
      <p>
        Agents in other VPCs or accounts reach the endpoint over VPC peering, Transit Gateway or PrivateLink.
        If you prefer an internal NLB with no ingress for agents, set{" "}
        <code>ingress.agents.enabled: false</code> and <code>service.agents.type: LoadBalancer</code>,
        annotate the <code>eddy-hub-agents</code> Service with <code>service.agents.annotations</code>, and
        let the hub terminate TLS itself with <code>agentTLS.secretName</code> (a{" "}
        <code>kubernetes.io/tls</code> Secret).
      </p>
      <div className="callout">
        <p>
          Until mTLS for agents arrives (planned for v1.1), the agent token is the main control on the agent
          endpoint. Keep that endpoint internal, or restrict it by source CIDR. <code>/mcp</code> on the UI
          host must not sit behind your sign-in proxy, because MCP clients send a personal access token.
        </p>
      </div>

      <h2 id="helm">6. Install</h2>
      <CodeBlock
        code={`
$ helm install eddy-hub oci://ghcr.io/idestis/charts/eddy-hub --version 1.0.0 \\
    --namespace eddy -f hub-values.yaml
`}
      />
      <p>
        The chart is published under <code>oci://ghcr.io/idestis/charts</code> and the images are{" "}
        <code>ghcr.io/idestis/eddy-hub</code> and <code>ghcr.io/idestis/eddy-agent</code>. Helm installs the
        Cluster resource definition on first install but never upgrades it; see{" "}
        <Link to="/docs/operations/" hash="upgrade">
          Operations
        </Link>
        .
      </p>

      <h2 id="verify">7. Verify</h2>
      <ol>
        <li>
          Wait for the rollout, then read the install notes the chart prints. They list the callback URLs to
          register, the agent endpoint, and the exact agent command for every cluster in{" "}
          <code>clusters[]</code>:
          <CodeBlock
            lines={["$ kubectl -n eddy rollout status deploy/eddy-hub", "$ helm -n eddy get notes eddy-hub"]}
          />
        </li>
        <li>
          Check readiness on the metrics port, 9090. A hub pod is ready once the cluster registry has synced.
          Database or peer-link trouble shows as <code>ok (degraded: …)</code> but does not take the pod out
          of service:
          <CodeBlock
            lines={[
              "$ kubectl -n eddy get pods",
              "$ kubectl -n eddy port-forward deploy/eddy-hub 9090",
              "$ curl -s localhost:9090/readyz",
            ]}
          />
        </li>
        <li>
          Read the log. It is JSON, one object per line:
          <CodeBlock lines={["$ kubectl -n eddy logs deploy/eddy-hub"]} />
        </li>
        <li>
          Open <code>https://eddy.internal.example.com</code>. You should see the sign-in page. Sign in as the
          local user you defined. There are no clusters yet, and what people can see in each one is decided by
          the <code>userRBAC</code> values of that cluster's agent.
        </li>
      </ol>

      <h2 id="sizing">Sizing and high availability</h2>
      <ul>
        <li>
          The hub runs <strong>two replicas</strong> by default (<code>replicaCount</code>), active/active.
          Each serves the UI, API, MCP and agent endpoint. An agent connects to whichever replica the load
          balancer picks, and the others relay to it over a peer channel on port 8444 that only hub pods can
          reach. Do not run fewer than two in production.
        </li>
        <li>
          A <code>PodDisruptionBudget</code> (<code>maxUnavailable: 1</code>) and a rolling update that never
          removes a replica before its replacement is ready keep the UI up during node drains and upgrades.
          Replicas also spread across zones and nodes by default.
        </li>
        <li>
          Every replica must mount the same session key. The chart does that, and each replica logs a{" "}
          <code>keyFingerprint</code> at start. A replica that fails to authenticate its peers reports{" "}
          <code>degraded</code> in <code>/readyz</code>.
        </li>
        <li>
          Default resources are 100m CPU and 256Mi memory requested, with a 512Mi memory limit. Raise them
          with <code>resources</code> if you watch many clusters.
        </li>
        <li>
          Sessions, rate-limit counters and the agent session registry live in <code>UNLOGGED</code> tables. A
          database crash or a failover to a standby empties them: everyone signs in again and limits reset.
          Nothing else is lost, and agents re-register within about ten seconds.
        </li>
        <li>
          With <code>networkPolicy.enabled: true</code>, set the <code>from</code> sources for the UI, agents
          and metrics ports, and allow egress to PostgreSQL. For CloudNativePG:
          <CodeBlock
            code={`
networkPolicy:
  enabled: true
  egress:
    postgresTo:
      - podSelector:
          matchLabels: {cnpg.io/cluster: eddy-db}
`}
          />
        </li>
      </ul>

      <h2 id="trouble">Troubleshooting</h2>
      <ul>
        <li>
          <strong>A pod stays not ready.</strong> The cluster registry has not synced: check the hub's RBAC on{" "}
          <code>clusters.gitops.eddy.dev</code> and its log.
        </li>
        <li>
          <strong>
            <code>/readyz</code> says degraded.
          </strong>{" "}
          It names the missing peer link or the unreachable store. Check that the network policy allows port
          8444 between hub pods and that every replica logs the same <code>keyFingerprint</code>.
        </li>
        <li>
          <strong>Sign-in answers 503.</strong> The hub cannot reach PostgreSQL. Reads of cluster data keep
          working, but sign-in and writes fail closed until it is back. Check the DSN Secret and the network
          path to the database.
        </li>
        <li>
          <strong>Sign-in loops or fails with an Origin error.</strong> <code>publicURL</code> must equal the
          address in the browser, including the scheme.
        </li>
        <li>
          <strong>The hub refuses to start.</strong> The config is parsed strictly, so an unknown key stops
          it. Run <code>kubectl -n eddy logs deploy/eddy-hub</code> and look for{" "}
          <code>invalid configuration</code>.
        </li>
        <li>
          <strong>Everyone was signed out.</strong> The store is <code>memory</code>, PostgreSQL crashed or
          failed over, or the session key Secret changed.
        </li>
      </ul>
      <p>
        Next: <Link to="/docs/sign-in/">set up sign-in</Link>, then{" "}
        <Link to="/docs/clusters/">add your clusters</Link>.
      </p>
      <Pager current="/docs/install/" />
    </>
  );
}
