import { createFileRoute, Link } from "@tanstack/react-router";
import { FullRef } from "../../components/FullRef";
import { Kbd } from "../../components/Kbd";
import { type KeyItem, Keymap } from "../../components/Keymap";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/using")({
  head: () =>
    pageHead({
      title: "Using Eddy · Eddy docs",
      description:
        "The keyboard map, the command palette, lists and filters, the dependency graph, logs, YAML and events, Ask AI on a selection and threads.",
      path: "/docs/using/",
    }),
  component: Page,
});

// Transcribed from web/src/lib/keys.ts. The first key of each binding is shown; arrows and Enter also work
// where noted in the text.
const moveKeys: KeyItem[] = [
  { keys: ["j"], label: "Move down" },
  { keys: ["k"], label: "Move up" },
  { keys: ["l"], label: "Open selected" },
  { keys: ["h"], label: "Go back up" },
  { keys: ["g", "g"], label: "Jump to top" },
  { keys: ["G"], label: "Jump to bottom" },
  { keys: ["u"], label: "Jump to owner" },
  { keys: ["["], label: "Previous page in this nav group" },
  { keys: ["]"], label: "Next page in this nav group" },
];
const goKeys: KeyItem[] = [
  { keys: ["g", "f"], label: "Fleet overview" },
  { keys: ["g", "t"], label: "Threads" },
  { keys: ["g", "k"], label: "Access tokens" },
  { keys: ["g", "a"], label: "My audit log" },
];
const findKeys: KeyItem[] = [
  { keys: ["⌘", "K"], label: "Search every cluster" },
  { keys: [":"], label: "Commands only" },
  { keys: ["/"], label: "Filter this list" },
  { keys: ["a"], label: "Ask AI about the selection" },
  { keys: ["d"], label: "Details panel" },
  { keys: ["?"], label: "Keyboard shortcuts" },
];
const clusterKeys: KeyItem[] = [
  { keys: ["1", "…", "9"], label: "Switch to cluster 1 to 9" },
  { keys: ["{"], label: "Previous cluster" },
  { keys: ["}"], label: "Next cluster" },
  { keys: ["n"], label: "Add a cluster (fleet page)" },
];
const actKeys: KeyItem[] = [
  { keys: ["r"], label: "Reconcile" },
  { keys: ["R"], label: "Reconcile with source" },
  { keys: ["s"], label: "Suspend or resume" },
  { keys: ["L"], label: "Logs (pods and workloads)" },
  { keys: ["o"], label: "Overview tab" },
  { keys: ["e"], label: "Events tab" },
  { keys: ["y"], label: "YAML tab" },
  { keys: ["t"], label: "Threads tab" },
  { keys: ["c"], label: "New thread (on the Threads tab)" },
];
const logKeys: KeyItem[] = [{ keys: ["f"], label: "Toggle follow" }];
const graphKeys: KeyItem[] = [
  { keys: ["0"], label: "Fit the graph to the screen" },
  { keys: ["+"], label: "Zoom in" },
  { keys: ["-"], label: "Zoom out" },
  { keys: ["F"], label: "Focus on the selected node and its neighbours (again: whole graph)" },
];

function Page() {
  return (
    <>
      <h1>Using Eddy</h1>
      <p className="lead">
        Eddy is built to be driven from the keyboard, and everything has a mouse path too. This page covers
        the keys, the palette, lists, the dependency graph, logs and threads. Press <Kbd>?</Kbd> in the app at
        any time for the same key list.
      </p>
      <FullRef path="web/src/lib/keys.ts" label="web/src/lib/keys.ts (the key registry)" />

      <h2 id="keys">Keyboard map</h2>
      <p>
        Letters work when no text field has focus. A two-key sequence such as <Kbd>g</Kbd> <Kbd>f</Kbd> means
        press the keys one after the other, within a second. <Kbd>j</Kbd> and <Kbd>k</Kbd> also answer to the
        down and up arrows, <Kbd>l</Kbd> to Enter and the right arrow, and <Kbd>h</Kbd> to Escape and the left
        arrow. On Windows and Linux the palette is <Kbd>Ctrl</Kbd> <Kbd>K</Kbd>.
      </p>
      <h3 id="keys-move">Move around</h3>
      <Keymap items={moveKeys} label="Move around" />
      <h3 id="keys-go">Go to</h3>
      <Keymap items={goKeys} label="Go to" />
      <h3 id="keys-find">Find and ask</h3>
      <Keymap items={findKeys} label="Find and ask" />
      <h3 id="keys-clusters">Clusters</h3>
      <Keymap items={clusterKeys} label="Clusters" />
      <h3 id="keys-act">Act on the selection</h3>
      <Keymap items={actKeys} label="Act on selection" />
      <h3 id="keys-logs">In logs</h3>
      <Keymap items={logKeys} label="In logs" />
      <h3 id="keys-graph">In the graph</h3>
      <Keymap items={graphKeys} label="In the graph" />

      <h2 id="palette">The command palette</h2>
      <p>
        Press <Kbd>⌘</Kbd> <Kbd>K</Kbd> to search resources and commands.
      </p>
      <ul>
        <li>
          <strong>Scope.</strong> Inside a cluster, the palette searches that cluster, and it is instant.
          Press <Kbd>Tab</Kbd> (or use the scope toggle) to widen it to <strong>every cluster</strong>. On the
          fleet page it already searches every cluster. Matches rank by fuzzy name, then failing resources
          first.
        </li>
        <li>
          <strong>Commands.</strong> Type <Kbd>:</Kbd> first to show commands only. They depend on what is
          selected: reconcile, suspend or resume, show logs, jump to the owner, ask AI, new thread, go to a
          page of resources, pin a cluster, toggle the theme and open the key list.
        </li>
        <li>
          <strong>Clusters.</strong> Clusters appear as <q>Switch to …</q> commands. Outside the palette,{" "}
          <Kbd>1</Kbd> to <Kbd>9</Kbd> switch to that cluster directly, and <Kbd>{"{"}</Kbd> and{" "}
          <Kbd>{"}"}</Kbd> go to the previous and next one.
        </li>
        <li>
          <strong>Needs attention.</strong> With an empty query the palette lists what needs attention first.
        </li>
      </ul>

      <h2 id="lists">Lists, filters and status</h2>
      <ul>
        <li>
          The sidebar groups kinds by project (Flux and the presets you enabled). Each list shows a status
          pill per row: <strong>Ready</strong>, <strong>Failed</strong>, <strong>Reconciling</strong>,{" "}
          <strong>Suspended</strong>, <strong>Unknown</strong> or <strong>Completed</strong>. Lists sort what
          needs attention first.
        </li>
        <li>
          Press <Kbd>/</Kbd> to filter the list you are looking at. <Kbd>j</Kbd> and <Kbd>k</Kbd> move,{" "}
          <Kbd>l</Kbd> opens, <Kbd>h</Kbd> goes back.
        </li>
        <li>
          Cluster colours and environments (set by the operator) tint each cluster, so a production cluster is
          hard to mistake for staging. Rows update live, with a short flash when a status changes.
        </li>
        <li>
          Objects a Kustomization applied that Eddy does not watch appear as inventory-only rows: their kind,
          namespace and name, never their content.
        </li>
      </ul>
      <h3 id="actions">Reconcile, suspend and resume</h3>
      <p>
        Select a Flux resource and press <Kbd>r</Kbd> to reconcile, <Kbd>R</Kbd> to reconcile with its source
        first, or <Kbd>s</Kbd> to suspend or resume it. These run as you, so they need <code>patch</code> in
        that cluster (see <Link to="/docs/access/">Access and RBAC</Link>). On a protected cluster you type
        the cluster name to confirm.
      </p>

      <h2 id="graph">The dependency graph</h2>
      <p>
        A Kustomization or HelmRelease has a <strong>Manages</strong> section with three views:
      </p>
      <ul>
        <li>
          <strong>Tree:</strong> the ownership tree of what it created.
        </li>
        <li>
          <strong>Graph:</strong> dependencies, sources and managed objects, live. Edges show{" "}
          <em>first → then</em> ordering, the source, what a resource applies, and what is waiting.
        </li>
        <li>
          <strong>Outline:</strong> the same graph as a list, step by step.
        </li>
      </ul>
      <p>
        Arrow keys, <Kbd>h</Kbd> <Kbd>j</Kbd> <Kbd>k</Kbd> <Kbd>l</Kbd> and Enter move along the edges while
        the graph has focus. Open a group node (double-click or Enter) to <strong>expand</strong> it. Press{" "}
        <Kbd>F</Kbd> to focus on a node and its neighbours, <Kbd>0</Kbd> to fit the graph, and use{" "}
        <Kbd>+</Kbd> and <Kbd>-</Kbd> to zoom. The right-click menu offers reconcile, focus and open.
      </p>

      <h2 id="detail">Logs, YAML and events</h2>
      <ul>
        <li>
          Tabs on a resource: <Kbd>o</Kbd> Overview, <Kbd>y</Kbd> YAML, <Kbd>e</Kbd> Events, <Kbd>t</Kbd>{" "}
          Threads, and <Kbd>L</Kbd> Logs for pods and workloads.
        </li>
        <li>
          YAML and events are read as you, so you see them only if your RBAC allows. YAML is redacted, Secret
          YAML is never shown, and ConfigMap data is stripped.
        </li>
        <li>
          Logs stream only while you are viewing them. A workload log (Deployment, StatefulSet, DaemonSet or
          Job) follows several pods at once. Press <Kbd>f</Kbd> to toggle follow. You need <code>get</code> on{" "}
          <code>pods/log</code>.
        </li>
      </ul>

      <h2 id="ask">Ask AI on a selection</h2>
      <p>
        When an operator has enabled it (see <Link to="/docs/ask-ai/">Ask AI</Link>), select a resource and
        press <Kbd>a</Kbd> to ask about it. Press <Kbd>a</Kbd> again to go back to Details. You can attach
        selected log lines or a YAML excerpt to a question. Answers can be wrong, so check them before acting,
        and commands in them are copy-only.
      </p>

      <h2 id="threads">Threads</h2>
      <p>
        Threads are review conversations on a resource or a cluster. Open the Threads tab (<Kbd>t</Kbd>) and
        press <Kbd>c</Kbd> to start one. Threads from Claude Code carry a <code>via mcp</code> badge. An Ask
        AI answer can be saved as a thread. <Kbd>g</Kbd> <Kbd>t</Kbd> lists all the threads on resources you
        can see, and you can reply or resolve them there.
      </p>
      <Pager current="/docs/using/" />
    </>
  );
}
