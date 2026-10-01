import { createFileRoute, Link } from "@tanstack/react-router";
import { CodeBlock } from "../../components/CodeBlock";
import { FullRef } from "../../components/FullRef";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/access")({
  head: () =>
    pageHead({
      title: "Access and RBAC · Eddy docs",
      description:
        "How Eddy uses Kubernetes impersonation, the eddy-viewer and eddy-operator roles, binding GitHub teams and OIDC groups, and typed confirmation on protected clusters.",
      path: "/docs/access/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Access and RBAC</h1>
      <p className="lead">
        Eddy has no permission model of its own: ordinary Kubernetes RBAC in each workload cluster decides. By
        default the agent chart lets every signed-in user read; you decide who may operate. This page explains
        how identities become Kubernetes subjects and which roles to grant.
      </p>
      <FullRef
        path="deploy/rbac/eddy-user-rbac.yaml"
        label="deploy/rbac/eddy-user-rbac.yaml and the eddy-agent chart values"
      />

      <h2 id="kinds">Two clusters, two kinds of roles</h2>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Where</th>
              <th scope="col">Set with</th>
              <th scope="col">Decides</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">Management cluster</th>
              <td>
                <code>onboarding.admins</code> in the hub chart
              </td>
              <td>
                Who may add, edit and delete clusters in the Add cluster wizard (
                <Link to="/docs/clusters/" hash="wizard">
                  Add clusters
                </Link>
                )
              </td>
            </tr>
            <tr>
              <th scope="row">Each workload cluster</th>
              <td>
                <code>userRBAC</code> in the agent chart
              </td>
              <td>Who may read and act on Flux and workloads there (this page)</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        Ask AI and MCP need no roles of their own: they run as the asking user. The AWS IAM role for Bedrock
        in{" "}
        <Link to="/docs/ask-ai/" hash="bedrock">
          Ask AI
        </Link>{" "}
        is an AWS credential for the hub pod, not Kubernetes RBAC.
      </p>

      <h2 id="how">How impersonation works</h2>
      <ol>
        <li>
          A person signs in to the hub. The hub turns the identity into a Kubernetes user and groups (see{" "}
          <Link to="/docs/sign-in/" hash="mapping">
            group mapping
          </Link>
          ): for example the user <code>local:alice</code> and the groups <code>eddy:platform</code> and{" "}
          <code>eddy:authenticated</code>.
        </li>
        <li>
          For every direct read (YAML, events, logs) and every write, the hub asks the agent to call the
          Kubernetes API with <strong>impersonation headers</strong> for that user and those groups. The API
          server's RBAC decides, and the cluster's own audit log records the real user.
        </li>
        <li>
          Lists and counts come from the agent's shared cache. The hub filters them per user with
          SubjectAccessReviews (cached for 45 seconds) before they leave the hub. This also covers thread
          lists, Ask AI and MCP results.
        </li>
        <li>
          The agent refuses <code>system:*</code> users and groups, users matching{" "}
          <code>denyUserPrefixes</code>, and any group outside <code>allowedGroupPrefixes</code> (default{" "}
          <code>eddy:</code>). The hub checks the same rules first.
        </li>
      </ol>
      <p>
        So Eddy can never do more than the user could do with <code>kubectl</code>, and the agent's own
        ServiceAccount cannot write. A permission change takes effect within about 90 seconds.
      </p>

      <h2 id="roles">1. Grant access with the agent chart</h2>
      <p>
        The <code>eddy-agent</code> chart creates two ClusterRoles, <code>eddy-viewer</code> and{" "}
        <code>eddy-operator</code>, and binds them to the groups you name. Set the groups in the agent values
        for each cluster:
      </p>
      <CodeBlock
        code={`
userRBAC:
  create: true                              # the default
  viewer:
    groups: ["eddy:authenticated"]          # the default: every signed-in user can read
  operator:
    groups: ["eddy:github:acme/platform"]   # who may reconcile, suspend and resume
`}
      />
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Role</th>
              <th scope="col">Bound to (default)</th>
              <th scope="col">What it allows</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">
                <code>eddy-viewer</code>
              </th>
              <td>
                <code>userRBAC.viewer.groups</code>: <code>eddy:authenticated</code>, every signed-in user
              </td>
              <td>
                <code>get</code>, <code>list</code> and <code>watch</code> on every kind the agent watches
                (preset kinds only when enabled), plus <code>get</code> on <code>pods/log</code>. Never
                Secrets or ConfigMaps
              </td>
            </tr>
            <tr>
              <th scope="row">
                <code>eddy-operator</code>
              </th>
              <td>
                <code>userRBAC.operator.groups</code>: none until you set them
              </td>
              <td>
                Everything a viewer can do, plus <code>patch</code> on the Flux kinds, which is what
                reconcile, suspend and resume need
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        By default every signed-in user can read summaries in that cluster. Set{" "}
        <code>userRBAC.viewer.groups: []</code> to opt out and bind viewers yourself.
      </p>
      <ul>
        <li>
          <strong>Limit to namespaces.</strong> A list in <code>userRBAC.namespaces</code> creates
          RoleBindings in those namespaces instead of cluster-wide bindings.
        </li>
        <li>
          <strong>Extra bindings.</strong> <code>userRBAC.bindings</code> takes entries with a{" "}
          <code>name</code>, a <code>role</code> (<code>viewer</code> or <code>operator</code>),{" "}
          <code>groups</code>, <code>users</code> and optional <code>namespaces</code>. For example, an
          operator group for one cluster only:
          <CodeBlock
            code={`
userRBAC:
  bindings:
    - name: prod-eu-operators
      role: operator
      groups: ["eddy:operators:prod-eu"]
`}
          />
        </li>
        <li>
          <code>userRBAC.roleNames</code> renames the roles and <code>userRBAC.extraRules</code> adds rules to
          them.
        </li>
        <li>
          <strong>Logs</strong> are readable only with <code>get</code> on <code>pods/log</code>. To keep logs
          out of Eddy, set <code>userRBAC.create: false</code> and apply your own copy of{" "}
          <code>deploy/rbac/eddy-user-rbac.yaml</code> without the <code>pods/log</code> rule.
        </li>
        <li>
          <strong>Secrets and ConfigMaps</strong> are not in the roles. Eddy never shows Secret YAML and
          strips <code>data</code> from ConfigMaps. Objects a Kustomization applied that Eddy does not watch
          appear as inventory-only rows (kind, namespace and name). A user sees one only if they can list its
          Kustomization and that kind in the namespace.
        </li>
        <li>
          The Karpenter and External Secrets rules only matter when the agent enables the matching presets.
        </li>
      </ul>
      <h3 id="raw">Without Helm: the example manifest</h3>
      <p>
        If you manage RBAC yourself, <code>deploy/rbac/eddy-user-rbac.yaml</code> in the repository holds the
        same two ClusterRoles and bindings (to <code>eddy:authenticated</code> and <code>eddy:platform</code>
        ). Set <code>userRBAC.create: false</code> on the agent, apply the file in every workload cluster, and
        edit the subjects:
      </p>
      <CodeBlock
        lines={[
          "$ curl -fsSLO https://raw.githubusercontent.com/idestis/eddy/main/deploy/rbac/eddy-user-rbac.yaml",
          "$ kubectl --context prod-eu apply -f eddy-user-rbac.yaml",
        ]}
      />

      <h2 id="bind">2. Know which group to bind</h2>
      <p>
        Bind <strong>groups</strong>, not individual users. A group name is the <code>eddy:</code> prefix plus
        what the identity provider sends. The same names work in <code>userRBAC.*.groups</code> and in your
        own RoleBindings, for example for a GitHub team:
      </p>
      <CodeBlock
        code={`
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: eddy-operator-platform
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: eddy-operator
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: eddy:github:acme/platform
`}
      />
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Identity</th>
              <th scope="col">Kubernetes group to bind</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">
                Local user with <code>groups: [platform]</code>
              </th>
              <td>
                <code>eddy:platform</code> (the user is <code>local:&lt;name&gt;</code>)
              </td>
            </tr>
            <tr>
              <th scope="row">GitHub organization</th>
              <td>
                <code>eddy:github:acme</code>
              </td>
            </tr>
            <tr>
              <th scope="row">GitHub team</th>
              <td>
                <code>eddy:github:acme/platform</code>
              </td>
            </tr>
            <tr>
              <th scope="row">
                Okta or Dex group <code>eddy-sre</code>
              </th>
              <td>
                <code>eddy:eddy-sre</code>
              </td>
            </tr>
            <tr>
              <th scope="row">Entra ID group</th>
              <td>
                <code>eddy:&lt;group object id&gt;</code>
              </td>
            </tr>
            <tr>
              <th scope="row">Every signed-in user</th>
              <td>
                <code>eddy:authenticated</code>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        Use <code>userRBAC.namespaces</code>, or a <code>RoleBinding</code> of your own, to limit a team to
        some namespaces. For Google or other providers that send no groups, map users to groups with{" "}
        <code>config.auth.groups.static</code> in the hub values.
      </p>

      <h2 id="pin">3. Pin the agent's impersonation (recommended)</h2>
      <p>
        The agent may impersonate groups. To limit a compromised agent pod to the groups you actually use,
        list them in the agent chart. The chart then pins the <code>impersonate</code> rule on groups with{" "}
        <code>resourceNames</code>:
      </p>
      <CodeBlock
        code={`
impersonation:
  groups: [eddy:authenticated, eddy:platform, eddy:github:acme/platform]
`}
      />
      <p>
        List every group you bind, or those users lose access. Users cannot be pinned the same way, so bind
        humans to groups, never to individual users with powerful roles.
      </p>

      <h2 id="protected">Protected clusters: typed confirmation</h2>
      <p>
        A cluster marked <code>protected: true</code> (usually production) asks for a typed confirmation on
        every write: the UI asks the user to type the cluster name, and the hub checks it on the server. Over
        MCP the same check is the <code>confirm_cluster</code> argument. Operators can deny MCP writes to
        protected clusters entirely with <code>config.mcp.protectedClusters: deny</code>. The typed name is a
        speed bump: the real controls are RBAC and the audit log.
      </p>

      <h2 id="can">What each role can do</h2>
      <div className="tbl">
        <table>
          <thead>
            <tr>
              <th scope="col">Action</th>
              <th scope="col">Viewer</th>
              <th scope="col">Operator</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <th scope="row">See clusters, lists, counts, graph, YAML, events</th>
              <td>Yes</td>
              <td>Yes</td>
            </tr>
            <tr>
              <th scope="row">Read pod and workload logs</th>
              <td>
                Yes (needs <code>pods/log</code>)
              </td>
              <td>Yes</td>
            </tr>
            <tr>
              <th scope="row">Reconcile, reconcile with source</th>
              <td>No</td>
              <td>Yes</td>
            </tr>
            <tr>
              <th scope="row">Suspend and resume</th>
              <td>No</td>
              <td>Yes</td>
            </tr>
            <tr>
              <th scope="row">Add, edit or delete a cluster in Eddy</th>
              <td colSpan={2}>
                Needs <code>onboarding.admins</code> in the hub chart (RBAC on{" "}
                <code>clusters.gitops.eddy.dev</code> in the management cluster), see{" "}
                <Link to="/docs/clusters/" hash="wizard">
                  Add clusters
                </Link>
              </td>
            </tr>
            <tr>
              <th scope="row">Threads, Ask AI, access tokens</th>
              <td colSpan={2}>Any signed-in user, on the resources they can already see</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>
        The audit log is yours alone unless your group is in <code>config.auth.auditViewerGroups</code> (use
        the prefixed name, for example <code>eddy:platform</code>).
      </p>

      <h2 id="verify">Verify</h2>
      <ol>
        <li>
          Impersonate as a user yourself and check:
          <CodeBlock
            lines={[
              "$ kubectl --context prod-eu auth can-i patch kustomizations.kustomize.toolkit.fluxcd.io \\",
              "    -n flux-system --as local:alice --as-group eddy:platform --as-group eddy:authenticated",
            ]}
          />
        </li>
        <li>
          Sign in as a user from a bound group. The cluster should list its resources. A user with no binding
          sees an empty list.
        </li>
        <li>
          Open <code>/api/v1/me</code> to see the groups the hub resolved for you.
        </li>
      </ol>
      <h2 id="trouble">Troubleshooting</h2>
      <ul>
        <li>
          <strong>The UI is empty for a user.</strong> They have no RBAC in that cluster, or the group name in
          the binding does not match. Compare it with the groups in <code>/api/v1/me</code>.
        </li>
        <li>
          <strong>Reconcile returns Forbidden.</strong> The user lacks <code>patch</code> on that kind. Bind
          them to <code>eddy-operator</code>.
        </li>
        <li>
          <strong>A user lost access after you pinned groups.</strong> Their group is not in{" "}
          <code>impersonation.groups</code>. Add it and upgrade the agent.
        </li>
        <li>
          <strong>A team change did not apply.</strong> Groups are captured at sign-in. The user signs out and
          in again.
        </li>
      </ul>
      <Pager current="/docs/access/" />
    </>
  );
}
