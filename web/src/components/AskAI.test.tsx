import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type {
  AskRequest,
  AskResponse,
  Chat,
  ClusterInfo,
  Message,
  Resource,
  ResourceRef,
  SearchResponse,
} from "../api/types";
import { AppStateProvider, useAppState } from "../lib/appState";
import { ChatProvider } from "../lib/chatState";
import { resource } from "../test/fixtures";
import { AskAIPanel } from "./AskAI";

const api = vi.hoisted(() => ({
  askAI: vi.fn(),
  getChat: vi.fn(),
  listChats: vi.fn(),
  updateChat: vi.fn(),
  deleteChat: vi.fn(),
  createThread: vi.fn(),
  listTokens: vi.fn(),
  search: vi.fn(),
  getClusters: vi.fn(),
}));
vi.mock("../api/endpoints", async (orig) => ({
  ...(await orig<typeof import("../api/endpoints")>()),
  ...api,
}));
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
  Link: ({
    children,
    title,
    className,
    onClick,
    ...rest
  }: {
    children: ReactNode;
    title?: string;
    className?: string;
    onClick?: () => void;
    "aria-label"?: string;
    "data-hidden"?: boolean;
  }) => (
    <a
      href="/"
      title={title}
      className={className}
      aria-label={rest["aria-label"]}
      data-hidden={rest["data-hidden"]}
      onClick={(e) => {
        e.preventDefault();
        onClick?.();
      }}
    >
      {children}
    </a>
  ),
}));
const features = vi.hoisted(() => ({ ai: true, aiProvider: "anthropic" }));
vi.mock("../api/queries", async (orig) => ({
  ...(await orig<typeof import("../api/queries")>()),
  useMe: () => ({ data: { user: "local:dana", features } }),
}));

const staging: ClusterInfo = {
  name: "staging",
  displayName: "staging",
  protected: false,
  order: 1,
  connected: true,
};
const prod: ClusterInfo = {
  name: "prod-eu",
  displayName: "prod-eu",
  protected: false,
  order: 2,
  connected: true,
};
const apps = resource("apps");
const podinfo = resource("podinfo", {
  kind: "HelmRelease",
  group: "helm.toolkit.fluxcd.io",
  namespace: "apps",
});
const redis = resource("redis", { kind: "HelmRelease", group: "helm.toolkit.fluxcd.io", namespace: "cache" });
const ref = (cluster: string, r: Resource): ResourceRef => ({
  cluster,
  group: r.group,
  kind: r.kind,
  namespace: r.namespace,
  name: r.name,
});
const appsRef = ref("staging", apps);
const podinfoRef = ref("staging", podinfo);

const ai = { type: "ai", subject: "ai", display: "AI", via: "askai", client: "claude" } as const;
const human = { type: "human", subject: "local:dana", display: "dana", via: "askai" } as const;
let ids = 0;
const msg = (chat: string, a: Message["author"], body: string): Message => ({
  id: `m${++ids}`,
  threadId: chat,
  author: a,
  body,
  createdAt: "2026-09-30T10:00:00Z",
});

/** The hub's chats, as the fake endpoints see them. */
let db: Map<string, { chat: Chat; messages: Message[] }>;
let hidden: Set<string>;

function chatOf(id: string, title: string, context: ResourceRef[], updatedAt = "2026-09-30T10:00:00Z"): Chat {
  return { id, owner: "local:dana", title, context, createdAt: updatedAt, updatedAt, messageCount: 0 };
}

beforeAll(() => {
  Element.prototype.scrollTo ??= () => {};
  Element.prototype.scrollIntoView ??= () => {};
});

beforeEach(() => {
  sessionStorage.clear();
  features.ai = true;
  ids = 0;
  db = new Map();
  hidden = new Set();
  let n = 0;
  api.askAI.mockReset().mockImplementation(async (req: AskRequest): Promise<AskResponse> => {
    let entry = req.chatId ? db.get(req.chatId) : undefined;
    if (req.chatId && !entry) throw new ApiError(404, "not_found", "Chat not found.");
    if (!entry) {
      n += 1;
      entry = { chat: chatOf(`ch_${n}`, req.question, req.context ?? []), messages: [] };
      db.set(entry.chat.id, entry);
    } else if (req.context) {
      entry.chat = { ...entry.chat, context: req.context };
    }
    const answer = msg(entry.chat.id, ai, `Answer to ${req.question}`);
    entry.messages.push(msg(entry.chat.id, human, req.question), answer);
    entry.chat = { ...entry.chat, messageCount: entry.messages.length };
    return {
      chat: entry.chat,
      message: answer,
      steps: [],
      contextStatus: entry.chat.context.map((r) => (hidden.has(r.name) ? "hidden" : "ok")),
    };
  });
  api.getChat.mockReset().mockImplementation(async (id: string) => {
    const e = db.get(id);
    if (!e) throw new ApiError(404, "not_found", "Chat not found.");
    return { chat: e.chat, messages: e.messages };
  });
  api.listChats.mockReset().mockImplementation(async () => ({ items: [...db.values()].map((e) => e.chat) }));
  api.updateChat.mockReset().mockImplementation(async (id: string, body: { title?: string }) => {
    const e = db.get(id);
    if (!e) throw new ApiError(404, "not_found", "Chat not found.");
    e.chat = { ...e.chat, ...body };
    return e.chat;
  });
  api.deleteChat.mockReset().mockImplementation(async (id: string) => {
    db.delete(id);
  });
  api.createThread.mockReset().mockResolvedValue({ thread: { id: "th_saved" }, message: {} });
  api.listTokens.mockReset().mockResolvedValue({ items: [] });
  api.getClusters.mockReset().mockResolvedValue({ items: [staging, prod] });
  api.search.mockReset().mockImplementation(
    async (q: { q: string }): Promise<SearchResponse> => ({
      items: [
        { cluster: "staging", resource: podinfo },
        { cluster: "prod-eu", resource: redis },
      ]
        .filter((h) => h.resource.name.includes(q.q))
        .map((h) => ({ ...h, match: { score: 1, primary: [], secondary: [] } })),
    }),
  );
});

function Probe() {
  const { pane, askWithLogs } = useAppState();
  return (
    <>
      <output aria-label="pane">{pane}</output>
      <button
        type="button"
        onClick={() =>
          askWithLogs({
            attachment: { kind: "logs", source: "flux-system/apps-7d9f/manager", lines: ["error: boom"] },
            total: 1,
            question: "What's wrong in these log lines?",
            link: { cluster: "staging", ref: { kind: "Pod", namespace: "flux-system", name: "apps-7d9f" } },
          })
        }
      >
        ask about lines
      </button>
    </>
  );
}

interface Screen {
  /** The page key: a new key remounts the panel, like a route change. */
  page?: string;
  cluster?: ClusterInfo;
  resource?: Resource;
}

function setup(initial: Screen = { resource: apps }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const ui = ({ page = "a", cluster = staging, resource: r }: Screen) => (
    <QueryClientProvider client={qc}>
      <AppStateProvider>
        <ChatProvider>
          <Probe />
          <AskAIPanel key={page} cluster={cluster} resource={r} />
        </ChatProvider>
      </AppStateProvider>
    </QueryClientProvider>
  );
  const view = render(ui(initial));
  return { user: userEvent.setup(), go: (s: Screen) => view.rerender(ui(s)), qc };
}

const box = () => screen.getByRole("combobox", { name: "Ask AI" });
const chips = () =>
  screen
    .queryAllByRole("link")
    .map((a) => a.getAttribute("aria-label") ?? "")
    .filter((l) => l.startsWith("Open") || l.includes("no longer"));
const lastAsk = (): AskRequest => api.askAI.mock.lastCall?.[0];

describe("Ask AI chats", () => {
  it("keeps the chat across a route change and a selection change", async () => {
    const { user, go } = setup({ page: "apps", resource: apps });
    await user.type(box(), "why is it slow?{Enter}");
    await screen.findByText("Answer to why is it slow?");
    expect(lastAsk()).toEqual({ question: "why is it slow?", context: [appsRef] });

    // Another page, another resource: the same chat, the same chips.
    go({ page: "podinfo", resource: podinfo });
    expect(await screen.findByText("Answer to why is it slow?")).toBeInTheDocument();
    expect(screen.getByText("why is it slow?", { selector: "div" })).toBeInTheDocument();
    expect(chips()).toEqual(["Open Kustomization flux-system/apps on staging"]);
    // Another cluster: the chip now names its cluster.
    go({ page: "prod", cluster: prod, resource: redis });
    expect(await screen.findByText("Answer to why is it slow?")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Open Kustomization flux-system/apps on staging" }),
    ).toHaveTextContent("staging");

    await user.type(box(), "and now?{Enter}");
    await screen.findByText("Answer to and now?");
    // The open chat, and no context: the chips did not change.
    expect(lastAsk()).toEqual({ chatId: "ch_1", question: "and now?" });
  });

  it("reopens the chat after a reload of the tab", async () => {
    const { user } = setup();
    await user.type(box(), "first{Enter}");
    await screen.findByText("Answer to first");
    expect(JSON.parse(sessionStorage.getItem("eddy.ai.chat") ?? "{}")).toMatchObject({ chatId: "ch_1" });
  });

  it("starts a new chat with the selection, or the cluster when nothing is selected", async () => {
    const { user, go } = setup({ resource: apps });
    expect(chips()).toEqual(["Open Kustomization flux-system/apps on staging"]);
    await user.type(box(), "first{Enter}");
    await screen.findByText("Answer to first");

    go({ resource: undefined });
    await user.click(screen.getByRole("button", { name: "New chat" }));
    expect(screen.queryByText("Answer to first")).toBeNull();
    expect(chips()).toEqual(["Open the whole cluster staging"]);
    await user.type(box(), "second{Enter}");
    await waitFor(() => expect(api.askAI).toHaveBeenCalledTimes(2));
    expect(lastAsk()).toEqual({
      question: "second",
      context: [{ cluster: "staging", group: "", kind: "", namespace: "", name: "" }],
    });
  });

  it("suggests the selection when it is not in the chat, and never adds it by itself", async () => {
    const { user, go } = setup({ resource: apps });
    await user.type(box(), "first{Enter}");
    await screen.findByText("Answer to first");
    expect(screen.queryByTitle(/to this chat$/)).toBeNull();

    go({ resource: podinfo });
    const add = await screen.findByRole("button", { name: "Add HR podinfo" });
    expect(chips()).toHaveLength(1);
    await user.click(add);
    expect(chips()).toEqual([
      "Open Kustomization flux-system/apps on staging",
      "Open HelmRelease apps/podinfo on staging",
    ]);
    await user.type(box(), "both?{Enter}");
    await waitFor(() => expect(api.askAI).toHaveBeenCalledTimes(2));
    // The chips changed, so the ask replaces the context.
    expect(lastAsk()).toEqual({ chatId: "ch_1", question: "both?", context: [appsRef, podinfoRef] });
  });

  it("adds a chip from the + picker", async () => {
    const { user } = setup({ resource: apps });
    await user.click(screen.getByRole("button", { name: "Add a resource or cluster" }));
    const search = screen.getByRole("combobox", { name: "Add to the chat" });
    await user.type(search, "redis");
    const option = await screen.findByRole("option", { name: /redis/ });
    expect(option).toHaveTextContent("prod-eu");
    await user.keyboard("{Enter}");
    expect(screen.queryByRole("combobox", { name: "Add to the chat" })).toBeNull();
    expect(chips()).toEqual([
      "Open Kustomization flux-system/apps on staging",
      "Open HelmRelease cache/redis on prod-eu",
    ]);
  });

  it("removes a chip with × without touching the question", async () => {
    const { user } = setup({ resource: apps });
    await user.type(box(), "keep this text");
    await user.click(
      screen.getByRole("button", { name: "Remove Kustomization flux-system/apps on staging from the chat" }),
    );
    expect(chips()).toEqual([]);
    expect(box()).toHaveValue("keep this text");
    await user.click(box());
    await user.keyboard("{Enter}");
    await waitFor(() => expect(api.askAI).toHaveBeenCalledOnce());
    expect(lastAsk()).toEqual({ question: "keep this text" });
  });

  describe("@mentions", () => {
    it("inserts @namespace/name and adds the chip", async () => {
      const { user } = setup({ resource: apps });
      await user.type(box(), "compare with @podi");
      const list = await screen.findByRole("listbox", { name: "Mention" });
      expect(box()).toHaveAttribute("aria-expanded", "true");
      expect(box()).toHaveAttribute("aria-controls", list.id);
      await within(list).findByRole("option", { name: /podinfo/ });
      await user.keyboard("{Enter}");
      expect(box()).toHaveValue("compare with @apps/podinfo ");
      expect(screen.queryByRole("listbox", { name: "Mention" })).toBeNull();
      expect(chips()).toContain("Open HelmRelease apps/podinfo on staging");
      // Removing the chip leaves the text alone.
      await user.click(
        screen.getByRole("button", { name: "Remove HelmRelease apps/podinfo on staging from the chat" }),
      );
      expect(box()).toHaveValue("compare with @apps/podinfo ");
    });

    it("moves with ↑ ↓, wraps, and closes on Esc without leaving the panel", async () => {
      api.search.mockImplementation(async () => ({
        items: [podinfo, redis].map((r, i) => ({
          cluster: i ? "prod-eu" : "staging",
          resource: r,
          match: { score: 1, primary: [], secondary: [] },
        })),
      }));
      const { user } = setup({ resource: apps });
      await user.type(box(), "@x");
      const list = await screen.findByRole("listbox", { name: "Mention" });
      await waitFor(() => expect(within(list).getAllByRole("option")).toHaveLength(2));
      const [first, second] = within(list).getAllByRole("option");
      expect(box()).toHaveAttribute("aria-activedescendant", first?.id);
      expect(first).toHaveAttribute("aria-selected", "true");
      await user.keyboard("{ArrowDown}");
      expect(box()).toHaveAttribute("aria-activedescendant", second?.id);
      await user.keyboard("{ArrowDown}");
      expect(box()).toHaveAttribute("aria-activedescendant", first?.id);
      await user.keyboard("{ArrowUp}");
      expect(second).toHaveAttribute("aria-selected", "true");
      await user.keyboard("{Escape}");
      expect(screen.queryByRole("listbox", { name: "Mention" })).toBeNull();
      expect(box()).toHaveAttribute("aria-expanded", "false");
      expect(box()).toHaveValue("@x");
      expect(screen.getByLabelText("pane")).not.toHaveTextContent("details");
      // Enter now sends instead of picking.
      await user.keyboard("{Enter}");
      await waitFor(() => expect(api.askAI).toHaveBeenCalledOnce());
    });
  });

  describe("history", () => {
    beforeEach(() => {
      const a = chatOf("ch_a", "Why is podinfo failing?", [podinfoRef], "2026-09-30T12:00:00Z");
      const b = chatOf("ch_b", "Anything unhealthy?", [], "2026-09-29T12:00:00Z");
      db.set("ch_a", {
        chat: { ...a, messageCount: 2 },
        messages: [msg("ch_a", human, "Why?"), msg("ch_a", ai, "A bad tag.")],
      });
      db.set("ch_b", { chat: b, messages: [] });
    });

    it("opens a chat from the list", async () => {
      const { user } = setup({ resource: apps });
      await user.click(screen.getByRole("button", { name: "History" }));
      const list = await screen.findByRole("list", { name: "Your chats" });
      expect(screen.getByText("Chats are kept for 30 days. Only you can see them.")).toBeInTheDocument();
      const rows = within(list).getAllByRole("listitem");
      expect(rows[0]).toHaveTextContent("Why is podinfo failing?");
      expect(rows[0]).toHaveTextContent("2 messages");
      expect(rows[0]).toHaveTextContent("podinfo");
      await user.click(within(list).getByRole("button", { name: /^Why is podinfo failing\?/ }));
      expect(await screen.findByText("A bad tag.")).toBeInTheDocument();
      expect(chips()).toEqual(["Open HelmRelease apps/podinfo on staging"]);
      await user.type(box(), "and?{Enter}");
      await waitFor(() => expect(api.askAI).toHaveBeenCalled());
      expect(lastAsk()).toEqual({ chatId: "ch_a", question: "and?" });
    });

    it("renames a chat inline", async () => {
      const { user } = setup();
      await user.click(screen.getByRole("button", { name: "History" }));
      await user.click(await screen.findByRole("button", { name: "Rename Anything unhealthy?" }));
      const input = screen.getByRole("textbox", { name: "Chat title" });
      await user.clear(input);
      await user.type(input, "Prod health{Enter}");
      await waitFor(() => expect(api.updateChat).toHaveBeenCalledWith("ch_b", { title: "Prod health" }));
      expect(await screen.findByText("Prod health")).toBeInTheDocument();
    });

    it("deletes a chat after confirming", async () => {
      const { user } = setup();
      await user.click(screen.getByRole("button", { name: "History" }));
      await user.click(await screen.findByRole("button", { name: "Delete Anything unhealthy?" }));
      expect(api.deleteChat).not.toHaveBeenCalled();
      await user.click(within(screen.getByRole("alert")).getByRole("button", { name: "Delete" }));
      await waitFor(() => expect(api.deleteChat).toHaveBeenCalledWith("ch_b"));
      await waitFor(() => expect(screen.queryByText("Anything unhealthy?")).toBeNull());
      expect(screen.getByText("Why is podinfo failing?")).toBeInTheDocument();
    });
  });

  it("strikes through context the user can no longer see", async () => {
    hidden.add("apps");
    const { user } = setup({ resource: apps });
    await user.type(box(), "first{Enter}");
    await screen.findByText("Answer to first");
    const chip = screen.getByRole("link", { name: /you no longer have access/ });
    expect(chip).toHaveAttribute("title", "You no longer have access");
    expect(chip).toHaveAttribute("data-hidden", "true");
    expect(chip.className).toContain("line-through");
    // No thread can go on a reference the user cannot see.
    expect(screen.queryByRole("button", { name: "Save as thread" })).toBeNull();
  });

  describe("errors", () => {
    it("turns read-only on the kill switch, keeping the history", async () => {
      api.askAI.mockRejectedValue(new ApiError(503, "disabled", "Ask AI is turned off."));
      db.set("ch_a", { chat: chatOf("ch_a", "Old chat", [appsRef]), messages: [] });
      const { user } = setup();
      await user.type(box(), "hello{Enter}");
      expect(await screen.findByRole("alert")).toHaveTextContent("Ask AI is turned off on this hub");
      expect(
        screen.getByText("Ask AI is turned off on this hub", { selector: "strong" }),
      ).toBeInTheDocument();
      expect(box()).toBeDisabled();
      expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
      await user.click(screen.getByRole("button", { name: "History" }));
      expect(await screen.findByRole("list", { name: "Your chats" })).toBeInTheDocument();
    });

    it("shows a stored chat read-only when Ask AI is off", async () => {
      features.ai = false;
      db.set("ch_a", {
        chat: chatOf("ch_a", "Old chat", [appsRef]),
        messages: [msg("ch_a", human, "Why?"), msg("ch_a", ai, "A bad tag.")],
      });
      sessionStorage.setItem("eddy.ai.chat", JSON.stringify({ chatId: "ch_a", edited: null }));
      setup();
      expect(await screen.findByText("A bad tag.")).toBeInTheDocument();
      expect(box()).toBeDisabled();
      expect(screen.queryByRole("button", { name: /Remove/ })).toBeNull();
    });

    it("keeps the question and offers a retry on 429", async () => {
      api.askAI.mockRejectedValueOnce(new ApiError(429, "rate_limited", "slow down"));
      const { user } = setup();
      await user.type(box(), "hello{Enter}");
      expect(await screen.findByRole("alert")).toHaveTextContent("Wait a moment");
      expect(screen.getByText("hello", { selector: "div" })).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Try again" }));
      expect(await screen.findByText("Answer to hello")).toBeInTheDocument();
      expect(screen.queryByRole("alert")).toBeNull();
    });

    it("explains a 400", async () => {
      api.askAI.mockRejectedValueOnce(new ApiError(400, "bad_request", "question is at most 8 KiB."));
      const { user } = setup();
      await user.type(box(), "hello{Enter}");
      expect(await screen.findByRole("alert")).toHaveTextContent("question is at most 8 KiB.");
    });
  });

  it("puts log lines into the open chat and sends them as an attachment", async () => {
    const { user } = setup();
    await user.type(box(), "first{Enter}");
    await screen.findByText("Answer to first");
    await user.click(screen.getByRole("button", { name: "ask about lines" }));
    // The same chat, with the lines in the composer and the question selected.
    expect(screen.getByText("Answer to first")).toBeInTheDocument();
    expect(screen.getByText("1 line · flux-system/apps-7d9f/manager")).toBeInTheDocument();
    await waitFor(() => expect(document.activeElement).toBe(box()));
    const el = box() as HTMLTextAreaElement;
    expect(el.selectionEnd - el.selectionStart).toBe(el.value.length);
    await user.keyboard("{Enter}");
    await waitFor(() => expect(api.askAI).toHaveBeenCalledTimes(2));
    expect(lastAsk()).toMatchObject({
      chatId: "ch_1",
      attachments: [{ kind: "logs", lines: ["error: boom"] }],
    });
    expect(await screen.findByText("attached")).toBeInTheDocument();
  });

  it("Esc clears a draft first, then returns to Details", async () => {
    const { user } = setup();
    act(() => screen.getByRole("button", { name: "History" }).focus());
    await user.type(box(), "half a question");
    await user.keyboard("{Escape}");
    expect(box()).toHaveValue("");
    await user.keyboard("{Escape}");
    expect(screen.getByLabelText("pane")).toHaveTextContent("details");
  });

  describe("answer actions", () => {
    it("saves the answer as a thread on the first context entry", async () => {
      const { user } = setup({ resource: apps });
      await user.type(box(), "why is it slow?{Enter}");
      await screen.findByText("Answer to why is it slow?");
      await user.click(screen.getByRole("button", { name: "Save as thread" }));
      await waitFor(() => expect(api.createThread).toHaveBeenCalledOnce());
      const arg = api.createThread.mock.calls[0]?.[0];
      expect(arg.ref).toEqual(appsRef);
      expect(arg.title).toBe("why is it slow?");
      expect(arg.body).toContain("**AI answer · claude via anthropic**");
      expect(await screen.findByRole("button", { name: "Saved" })).toBeDisabled();
    });

    it("lets the user pick the target when the chat has several", async () => {
      const { user } = setup({ resource: podinfo });
      await user.click(screen.getByRole("button", { name: "Add a resource or cluster" }));
      await user.type(screen.getByRole("combobox", { name: "Add to the chat" }), "redis");
      await screen.findByRole("option", { name: /redis/ });
      await user.keyboard("{Enter}");
      expect(chips()).toHaveLength(2);
      await user.type(box(), "q{Enter}");
      await screen.findByText("Answer to q");
      await user.click(screen.getByRole("button", { name: "Save the thread on" }));
      await user.click(await screen.findByRole("option", { name: /redis/ }));
      await user.click(screen.getByRole("button", { name: "Save as thread" }));
      await waitFor(() => expect(api.createThread).toHaveBeenCalledOnce());
      expect(api.createThread.mock.calls[0]?.[0].ref).toEqual(ref("prod-eu", redis));
    });

    it("never puts log lines in the thread", async () => {
      const { user } = setup();
      await user.click(screen.getByRole("button", { name: "ask about lines" }));
      await screen.findByText("1 line · flux-system/apps-7d9f/manager");
      await user.click(box());
      await user.keyboard("{Enter}");
      await screen.findByText(/^Answer to/);
      await user.click(screen.getByRole("button", { name: "Save as thread" }));
      await waitFor(() => expect(api.createThread).toHaveBeenCalledOnce());
      const body = api.createThread.mock.calls[0]?.[0].body as string;
      expect(body).toContain("(not copied)");
      expect(body).not.toContain("error: boom");
    });
  });
});
