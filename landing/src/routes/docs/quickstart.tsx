import { createFileRoute } from "@tanstack/react-router";
import { CodeBlock } from "../../components/CodeBlock";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/quickstart")({
  head: () =>
    pageHead({
      title: "Quickstart (kind) · Eddy docs",
      description: "Run Eddy on a local kind cluster in about five minutes.",
      path: "/docs/quickstart/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Quickstart (kind)</h1>
      <p className="lead">
        This runs the hub, an agent, Flux and a demo app on a local kind cluster, in about five minutes.
      </p>
      <h2 id="req">Requirements</h2>
      <p>
        <code>docker</code>, <a href="https://kind.sigs.k8s.io">kind</a>, <code>kubectl</code>,{" "}
        <code>helm</code>, <code>openssl</code> and <a href="https://taskfile.dev">Task</a>. The{" "}
        <code>flux</code> CLI is optional.
      </p>
      <h2 id="run">Run it</h2>
      <CodeBlock lines={["$ git clone https://github.com/idestis/eddy && cd eddy", "$ task kind:up"]} />
      <p>
        <code>task kind:up</code> runs <code>deploy/kind/up.sh</code>, which is safe to re-run. It creates a
        kind cluster named <code>eddy</code>, installs Flux, adds a demo podinfo app, builds and loads the hub
        and agent images, installs the hub with local users and one registered cluster called{" "}
        <code>kind</code>, installs the agent, and applies example user RBAC.
      </p>
      <p>Then start the port-forward the script prints. It looks like this:</p>
      <CodeBlock lines={["$ kubectl --context kind-eddy -n eddy port-forward svc/eddy-hub 8080:80"]} />
      <p>
        Open <code>http://localhost:8080</code> and sign in as <code>dev</code> with password{" "}
        <code>eddy-dev-password</code>. That user is in the <code>eddy:platform</code> group, so it can also
        reconcile and suspend. The password is for local demos only.
      </p>
      <div className="callout">
        <p>
          The exact namespace and command are printed by the script. If they differ from the above, trust the
          script, and see{" "}
          <a href="https://github.com/idestis/eddy/blob/HEAD/docs/install.md#quickstart-on-kind-5-minutes">
            install.md
          </a>
          .
        </p>
      </div>
      <h2 id="try">Try it</h2>
      <ul>
        <li>
          Press <kbd>⌘K</kbd> and search for <code>podinfo</code>.
        </li>
        <li>
          Use <kbd>j</kbd> and <kbd>k</kbd> to move, <kbd>l</kbd> to open, <kbd>r</kbd> to reconcile.{" "}
          <kbd>?</kbd> lists all keys.
        </li>
      </ul>
      <h2 id="clean">Clean up</h2>
      <CodeBlock lines={["$ task kind:down"]} />
      <Pager current="/docs/quickstart/" />
    </>
  );
}
