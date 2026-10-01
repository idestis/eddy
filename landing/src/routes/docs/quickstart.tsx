import { createFileRoute, Link } from "@tanstack/react-router";
import { CodeBlock } from "../../components/CodeBlock";
import { FullRef } from "../../components/FullRef";
import { Kbd } from "../../components/Kbd";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/quickstart")({
  head: () =>
    pageHead({
      title: "Quickstart (kind) · Eddy docs",
      description:
        "Run the Eddy hub, an agent, Flux and a demo app on a local kind cluster, sign in, look around and tear it down.",
      path: "/docs/quickstart/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Quickstart (kind)</h1>
      <p className="lead">
        This runs the hub, an agent, Flux and a demo app on one local kind cluster, in about five minutes.
        Nothing leaves your machine.
      </p>
      <FullRef path="docs/install.md#quickstart-on-kind-5-minutes" label="docs/install.md, quickstart" />
      <h2 id="req">Before you start</h2>
      <p>
        You need <code>docker</code>, <a href="https://kind.sigs.k8s.io">kind</a>, <code>kubectl</code>,{" "}
        <code>helm</code> and <code>openssl</code>. The <a href="https://taskfile.dev">Task</a> runner is
        optional, and so is the <code>flux</code> CLI (the script falls back to the Flux install manifest).
      </p>
      <h2 id="run">Steps</h2>
      <ol>
        <li>
          <strong>Clone the repository and start the cluster.</strong> The script builds the images from
          source, so run it from a checkout.
          <CodeBlock lines={["$ git clone https://github.com/idestis/eddy && cd eddy", "$ task kind:up"]} />
          Without Task, run the script that <code>task kind:up</code> calls. It is safe to re-run:
          <CodeBlock lines={["$ ./deploy/kind/up.sh"]} />
        </li>
        <li>
          <strong>Wait for it to finish.</strong> The script does this, in order:
          <ul>
            <li>
              creates a kind cluster named <code>eddy</code> (context <code>kind-eddy</code>) and installs
              Flux.
            </li>
            <li>adds a demo podinfo GitRepository and Kustomization, so there is something to look at;</li>
            <li>builds the hub and agent images and loads them into kind;</li>
            <li>
              installs the CloudNativePG operator and a one-instance PostgreSQL <code>Cluster</code> named{" "}
              <code>eddy-db</code> in the <code>eddy</code> namespace.
            </li>
            <li>
              installs two hub replicas with local users, the <code>eddy-db-app</code> Secret as the database
              connection, one registered cluster called <code>kind</code>, and the <code>platform</code> group
              allowed to use the Add cluster wizard (<code>onboarding.admins</code>).
            </li>
            <li>
              installs two agent replicas in <code>eddy-system</code>, talking to the hub over TLS with a
              self-signed certificate that it hands to the agent as <code>hub.caBundle</code>.
            </li>
            <li>
              grants the example user roles, so the <code>dev</code> user can read and operate (see{" "}
              <Link to="/docs/access/">Access and RBAC</Link>).
            </li>
          </ul>
        </li>
        <li>
          <strong>Open a port-forward to the hub.</strong> The script prints this command at the end:
          <CodeBlock lines={["$ kubectl --context kind-eddy -n eddy port-forward svc/eddy-hub 8080:80"]} />
        </li>
        <li>
          <strong>Sign in.</strong> Open <code>http://localhost:8080</code> and sign in as <code>dev</code>{" "}
          with the password <code>eddy-dev-password</code>. That user is in the <code>platform</code> group,
          which the example RBAC binds to <code>eddy-operator</code>, so it can also reconcile and suspend.
          The password is for local demos only.
        </li>
      </ol>
      <h2 id="verify">Verify</h2>
      <ul>
        <li>
          The fleet page shows one cluster, <code>kind</code>, as connected. From the terminal:
          <CodeBlock
            lines={[
              "$ kubectl --context kind-eddy get clusters.gitops.eddy.dev",
              "$ kubectl --context kind-eddy -n eddy get pods",
            ]}
          />
        </li>
        <li>
          Press <Kbd>⌘K</Kbd> (<Kbd>Ctrl</Kbd>+<Kbd>K</Kbd> elsewhere) and search for <code>podinfo</code>.
        </li>
        <li>
          Use <Kbd>j</Kbd> and <Kbd>k</Kbd> to move, <Kbd>l</Kbd> to open, <Kbd>r</Kbd> to reconcile.{" "}
          <Kbd>?</Kbd> lists every key. See <Link to="/docs/using/">Using Eddy</Link>.
        </li>
      </ul>
      <h2 id="add">Add another cluster</h2>
      <p>
        The kind setup registers its own cluster in <code>clusters[]</code>. The <code>dev</code> user can
        also try the <strong>Add cluster</strong> wizard (<Kbd>n</Kbd> on the fleet page), because the script
        sets <code>onboarding.admins.groups: [eddy:platform]</code> (see{" "}
        <Link to="/docs/clusters/" hash="wizard">
          Add clusters
        </Link>
        ). The wizard prints a <code>helm install</code> command that dials the hub's agent endpoint, which on
        kind is only an in-cluster address. A second cluster that can reach it needs a real endpoint, so
        follow <Link to="/docs/install/">Install</Link> for that.
      </p>
      <h2 id="clean">Tear down</h2>
      <CodeBlock lines={["$ task kind:down"]} />
      <p>
        This deletes the kind cluster <code>eddy</code> and everything in it. Without Task:
      </p>
      <CodeBlock lines={["$ kind delete cluster --name eddy"]} />
      <h2 id="trouble">Troubleshooting</h2>
      <ul>
        <li>
          <strong>
            A pod is <code>ImagePullBackOff</code>.
          </strong>{" "}
          The images are built locally and loaded into kind with <code>pullPolicy: Never</code>. Re-run{" "}
          <code>task kind:up</code>.
        </li>
        <li>
          <strong>Sign-in fails with an Origin error.</strong> Use <code>http://localhost:8080</code> exactly:
          the hub's <code>publicURL</code> is set to that address, and it must match the browser.
        </li>
        <li>
          <strong>The cluster stays disconnected.</strong> Read the agent log:{" "}
          <code>kubectl --context kind-eddy -n eddy-system logs deploy/eddy-agent</code>.
        </li>
      </ul>
      <p>
        The kind setup uses local users so it works offline. For a real deployment, sign in with GitHub or
        OIDC. See <Link to="/docs/sign-in/">Sign-in</Link>.
      </p>
      <Pager current="/docs/quickstart/" />
    </>
  );
}
