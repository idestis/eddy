import { createFileRoute, Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { ArchDiagram } from "../components/ArchDiagram";
import { ClusterList } from "../components/ClusterList";
import { CodeBlock } from "../components/CodeBlock";
import { HeroMock } from "../components/HeroMock";
import { GithubIcon } from "../components/icons";
import { Kbd } from "../components/Kbd";
import { Keymap } from "../components/Keymap";
import { Section, SectionHead } from "../components/Section";
import { pageHead, SITE_TITLE } from "../lib/head";
import { absoluteUrl, REPO_URL } from "../lib/site";

const DESCRIPTION =
  "Eddy is an open-source, multi-cluster web UI for Flux. Command palette, vim-style keys, Kubernetes RBAC through impersonation, and MCP for Claude Code.";

export const Route = createFileRoute("/")({
  head: () =>
    pageHead({
      title: SITE_TITLE,
      description: DESCRIPTION,
      path: "/",
      ogDescription:
        "Every Flux cluster in one place. Agents dial out, Kubernetes RBAC decides, and Claude Code can help through MCP. Open source, Apache-2.0.",
      twitterDescription:
        "Open-source multi-cluster Flux UI with a command palette, Kubernetes RBAC and MCP for Claude Code.",
      jsonLd: {
        "@context": "https://schema.org",
        "@type": "SoftwareApplication",
        name: "Eddy",
        description: "A fast, keyboard-first, multi-cluster UI for Flux.",
        url: absoluteUrl("/"),
        applicationCategory: "DeveloperApplication",
        operatingSystem: "Kubernetes",
        isAccessibleForFree: true,
        license: "https://www.apache.org/licenses/LICENSE-2.0",
        codeRepository: REPO_URL,
        offers: { "@type": "Offer", price: "0", priceCurrency: "USD" },
      },
    }),
  component: Home,
});

const who = [
  {
    title: "Platform and SRE teams",
    text: "One view of every cluster, without handing out kubeconfigs or exposing API servers.",
  },
  {
    title: "People who live in the terminal",
    text: "A UI that keeps up with you. Search, reconcile, suspend and read logs from the keyboard.",
  },
  {
    title: "Anyone who wants it open",
    text: "Multi-cluster is in the open-source edition, under Apache-2.0. Read it, run it, change it.",
  },
];

const steps = [
  {
    title: "Agents connect out.",
    text: "Each agent replica opens a WebSocket to the hub's agent endpoint with a token bound to one cluster name.",
  },
  {
    title: "Only summaries travel.",
    text: "Agents send resource summaries. Secret and ConfigMap data are never read.",
  },
  {
    title: "Actions run as you.",
    text: "The agent impersonates the signed-in user, so the cluster's own RBAC and audit log decide and record.",
  },
];

const ready: ReactNode[] = [
  "Hub and agents over outbound WebSocket, with 1..N agent replicas per cluster",
  "Add cluster wizard with one-time join tokens and a live connection checklist",
  "Kustomizations, HelmReleases and sources, plus Karpenter and External Secrets presets, core kinds and inventory, with reconcile, suspend and resume",
  "Live dependency graph",
  <>
    <Kbd>⌘K</Kbd> palette with fleet-wide server-side search
  </>,
  "YAML, events and workload logs with structured JSON, and Ask AI on a selection",
  "GitHub and OIDC sign-in (Google, Okta, Entra, Dex), local users and trusted reverse-proxy sign-in",
  "MCP for Claude Code with personal access tokens and threads",
  <>
    Ask AI with your choice of model: <a href="#ai">see below</a>
  </>,
  "PostgreSQL (bring your own, for example CloudNativePG) with active/active hub replicas",
  "Measured at 100 clusters × 15k resources: in our single-user benchmarks, fleet counts take about 10 ms, a 15k-row list about 100 ms and a fleet search about 35 ms",
];
const next = [
  "mTLS for agents (planned)",
  "MCP OAuth (planned)",
  "Hub-managed access: assign users to groups, with break-glass expiry (planned)",
];
const future = ["SAML 2.0", "Image automation", "Notification alerts", "flux diff"];

function Home() {
  return (
    <>
      <div className="hero-bg">
        <section aria-labelledby="hero-title" className="wrap pt-10 pb-14 text-center sm:pt-20 sm:pb-20">
          <p className="mb-0">
            <span className="inline-flex items-center gap-2 rounded-full border border-line-strong bg-surface px-3 py-1.5 font-mono text-[0.8rem] leading-none font-medium text-ink-2">
              <i className="size-2 rounded-full bg-linear-to-br from-prod-2 to-stage-2" />
              v1.0 · early release
            </span>
          </p>
          <h1
            id="hero-title"
            className="mx-auto mt-6 mb-5 max-w-[15em] text-[clamp(2.2rem,6.2vw,3.9rem)] font-bold tracking-[-0.035em]"
          >
            A fast, keyboard-first, <span className="grad-text whitespace-nowrap">multi-cluster</span> UI for
            Flux
          </h1>
          <p className="mx-auto mb-8 max-w-[38em] text-[clamp(1.05rem,2.2vw,1.25rem)] text-ink-2">
            See every Flux cluster you run in one place. Jump anywhere with <Kbd>⌘K</Kbd>, reconcile or
            suspend with a single key, and read logs, events and YAML without leaving the keyboard.
          </p>
          <p className="mx-auto -mt-4 mb-8 max-w-[38em] text-ink-2">
            Watch changes ripple through a live dependency graph, and let Claude Code investigate for you over
            MCP.
          </p>
          <div className="flex flex-wrap justify-center gap-3">
            <Link to="/docs/quickstart/" className="btn btn-primary">
              Try it on kind
            </Link>
            <a href={REPO_URL} className="btn btn-secondary">
              <GithubIcon />
              View on GitHub
            </a>
          </div>
          <p className="fine mt-4 mb-0">
            Open source · Apache-2.0 · Built for our own platform work, shared in case it helps yours
          </p>
          <HeroMock />
        </section>
      </div>

      <Section labelledBy="who-title">
        <SectionHead
          id="who-title"
          eyebrow="Who it's for"
          title="For teams that run Flux on more than one cluster"
        />
        <ul className="m-0 grid list-none gap-3 p-0 md:grid-cols-3">
          {who.map((w) => (
            <li
              key={w.title}
              className="rounded-r-xl border border-l-[3px] border-line border-l-accent bg-surface px-5 py-4 text-ink-2"
            >
              <b className="block text-ink">{w.title}</b>
              {w.text}
            </li>
          ))}
        </ul>
      </Section>

      <Section id="features" labelledBy="feat-title" alt>
        <SectionHead id="feat-title" eyebrow="Features" title="Fast to use, hard to misuse" />
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          <article className="card">
            <h3 className="mb-2 text-[1.1rem] font-semibold">Multi-cluster, open source</h3>
            <p>
              A hub runs in your management cluster and a light agent runs in each workload cluster. Agents
              dial out to the hub, so no workload API server is exposed and the hub holds no cluster
              credentials.
            </p>
            <ClusterList />
            <p className="text-[0.9rem]">
              Environment colours match the app: teal for development, violet for staging, orange for
              production. Protected clusters carry a solid badge and need a typed confirmation for writes.
            </p>
          </article>

          <article className="card">
            <h3 className="mb-2 text-[1.1rem] font-semibold">Kubernetes RBAC is the permission model</h3>
            <p>
              Every read and action runs as the signed-in user, impersonated by the agent in each cluster.
              Each cluster decides what they may do. Eddy invents no roles, and it cannot do more than the
              user could with <code>kubectl</code>. Sign in with GitHub or OIDC (Google, Okta, Entra, Dex),
              and the agent's ServiceAccount stays read-only.
            </p>
            <p>
              <a href="https://github.com/idestis/eddy/blob/HEAD/docs/auth.md">Sign-in guide</a>
            </p>
          </article>

          <article className="card">
            <h3 className="mb-2 text-[1.1rem] font-semibold">Live dependency graph</h3>
            <p>
              See sources, then each <code>dependsOn</code> layer, then what depends on them. When a reconcile
              starts, the change ripples through the graph live, so you can see what is blocked and why.
            </p>
          </article>

          <article className="card">
            <h3 className="mb-2 text-[1.1rem] font-semibold">Add a cluster in a minute</h3>
            <p>
              The Add cluster wizard shows the install command with a one-time join token, then a live
              checklist ticks as the agent connects and syncs. No kubeconfig leaves your hands.
            </p>
          </article>

          <article className="card md:col-span-2 lg:col-span-1">
            <h3 className="mb-2 text-[1.1rem] font-semibold">Fast</h3>
            <p>
              Agents stream summaries through informers and batched deltas, and the UI watches only the
              clusters on screen. Lists are virtualised and fleet search runs on the hub, so a big fleet stays
              responsive: we measure at 100 clusters × 15k resources.
            </p>
          </article>

          <article className="card md:col-span-2 lg:col-span-3">
            <h3 className="mb-2 text-[1.1rem] font-semibold">Command palette and vim-style keys</h3>
            <p>
              <Kbd>⌘K</Kbd> searches every resource in every cluster. The rest of the keymap stays out of your
              way.
            </p>
            <Keymap />
            <p className="fine mt-4">
              Keys shown are from the design prototype. Press <Kbd>?</Kbd> in the app for the current list.
            </p>
          </article>

          <article className="card md:col-span-2 lg:col-span-3">
            <h3 className="mb-2 text-[1.1rem] font-semibold">Threads and MCP for Claude Code</h3>
            <p>
              The hub serves an MCP endpoint. Claude Code, or any MCP client, can list clusters, find
              unhealthy resources across the fleet, read events and leave review threads that show up in the
              UI, marked <code>via mcp</code>. It acts with your identity, so it sees only what your RBAC
              allows. Create a token under <strong>Settings → Access tokens</strong>, then:
            </p>
            <CodeBlock
              lines={[
                "$ claude mcp add --transport http eddy https://<your-hub>/mcp \\",
                '    --header "Authorization: Bearer eddy_pat_…"',
              ]}
              className="my-4"
            />
            <p>
              Then ask:{" "}
              <em>
                "Review every failing HelmRelease across the fleet and leave a thread on each one with the
                likely cause."
              </em>{" "}
              <Link to="/docs/mcp/">MCP and Claude Code docs</Link>
            </p>
          </article>
        </div>
      </Section>

      <Section id="ai" labelledBy="ai-title">
        <SectionHead id="ai-title" eyebrow="Ask AI" title="Ask AI: bring your model">
          <p className="m-0">
            Ask about a resource or a selection and get an answer grounded in what you can see. It is off by
            default.
          </p>
        </SectionHead>
        <div className="grid gap-4 md:grid-cols-2">
          <article className="card">
            <span className="mb-3 inline-block rounded-full border border-ok/40 bg-ok/10 px-2.5 py-1.5 font-mono text-[0.72rem] leading-none font-semibold text-ink-2">
              v1.0 · ready
            </span>
            <ul className="m-0 list-disc pl-5 text-ink-2 marker:text-ink-3">
              <li className="my-1.5">The Anthropic API.</li>
              <li className="my-1.5">
                AWS Bedrock through the Converse API, which is model-agnostic. Set any Bedrock model or
                inference profile that supports tool use as <code>ai.bedrock.modelId</code>: Claude, Amazon
                Nova (Lite, Pro, Premier), Meta Llama, Mistral and others.
              </li>
              <li className="my-1.5">
                Claude is the default and the model we test with. Others should work, and we would like to
                hear how it goes.
              </li>
              <li className="my-1.5">
                With Bedrock, data stays in your AWS account, with IAM through IRSA or Pod Identity.
              </li>
            </ul>
          </article>
          <article className="card">
            <span className="mb-3 inline-block rounded-full border border-line-strong bg-surface-sunken px-2.5 py-1.5 font-mono text-[0.72rem] leading-none font-semibold text-ink-2">
              Future · planned
            </span>
            <ul className="m-0 list-disc pl-5 text-ink-2 marker:text-ink-3">
              <li className="my-1.5">
                OpenAI-compatible endpoints: self-hosted Ollama or vLLM, Azure OpenAI and others.
              </li>
            </ul>
            <h3 className="mt-5 mb-2 text-[1.05rem] font-semibold">Guardrails, whichever model you use</h3>
            <ul className="m-0 list-disc pl-5 text-ink-2 marker:text-ink-3">
              <li className="my-1.5">Read-only tools only.</li>
              <li className="my-1.5">Answers are scoped to what the signed-in user can see (RBAC).</li>
              <li className="my-1.5">Secrets are redacted before anything reaches the model.</li>
              <li className="my-1.5">It can be switched off at runtime.</li>
            </ul>
            <p className="mt-4 mb-0">
              <Link to="/docs/security/" className="inline-flex min-h-10 items-center">
                Security model
              </Link>
            </p>
          </article>
        </div>
      </Section>

      <Section id="how" labelledBy="how-title">
        <SectionHead
          id="how-title"
          eyebrow="How it works"
          title="One hub, any number of clusters, all connections outbound"
        />
        <ArchDiagram />
        <ol className="m-0 mt-6 grid list-none gap-x-6 gap-y-4 p-0 [counter-reset:s] md:grid-cols-3">
          {steps.map((s) => (
            <li
              key={s.title}
              className="relative pl-10 text-ink-2 [counter-increment:s] before:absolute before:top-0 before:left-0 before:grid before:size-7 before:place-items-center before:rounded-full before:bg-linear-to-br before:from-accent before:to-accent-2 before:font-mono before:text-[0.8rem] before:font-bold before:text-on-accent before:content-[counter(s)]"
            >
              <b className="text-ink">{s.title}</b> {s.text}
            </li>
          ))}
        </ol>
      </Section>

      <Section id="roadmap" labelledBy="road-title" alt>
        <SectionHead
          id="road-title"
          eyebrow="Status"
          title="v1.0 is ready to try. v1.1 is about agent security and access."
        >
          <p className="m-0">Eddy is at v1.0, an early release. Plans for what comes next can change.</p>
        </SectionHead>
        <div className="grid gap-4 md:grid-cols-2">
          <div className="card">
            <span className="mb-3 inline-block rounded-full border border-ok/40 bg-ok/10 px-2.5 py-1.5 font-mono text-[0.72rem] leading-none font-semibold text-ink-2">
              v1.0 · ready
            </span>
            <ul className="m-0 list-disc pl-5 text-ink-2 marker:text-ink-3">
              {ready.map((r, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: static list
                <li key={i} className="my-1.5">
                  {r}
                </li>
              ))}
            </ul>
          </div>
          <div className="card">
            <span className="mb-3 inline-block rounded-full border border-line-strong bg-surface-sunken px-2.5 py-1.5 font-mono text-[0.72rem] leading-none font-semibold text-ink-2">
              v1.1 · next
            </span>
            <ul className="m-0 list-disc pl-5 text-ink-2 marker:text-ink-3">
              {next.map((r) => (
                <li key={r} className="my-1.5">
                  {r}
                </li>
              ))}
            </ul>
            <p className="mt-4 mb-0">
              <b className="text-ink">Future</b>
            </p>
            <ul className="m-0 list-disc pl-5 text-ink-2 marker:text-ink-3">
              {future.map((r) => (
                <li key={r} className="my-1.5">
                  {r}
                </li>
              ))}
            </ul>
            <p className="mt-4 mb-0">
              <Link to="/docs/roadmap-faq/" className="inline-flex min-h-10 items-center">
                Full roadmap
              </Link>
            </p>
          </div>
        </div>
      </Section>

      <Section labelledBy="cta-title">
        <div className="text-center">
          <h2 id="cta-title" className="mb-3 text-[clamp(1.55rem,3.6vw,2.15rem)] font-semibold">
            Try it in about five minutes
          </h2>
          <p className="mx-auto mb-6 max-w-[38em] text-[1.15rem] text-ink-2">
            Everything runs on a local kind cluster.
          </p>
          <CodeBlock lines={["$ task kind:up"]} className="mx-auto mb-6 max-w-[520px] text-left" />
          <div className="flex flex-wrap justify-center gap-3">
            <Link to="/docs/quickstart/" className="btn btn-primary">
              Quickstart
            </Link>
            <Link to="/docs/" className="btn btn-secondary">
              Read the docs
            </Link>
          </div>
        </div>
      </Section>
    </>
  );
}
