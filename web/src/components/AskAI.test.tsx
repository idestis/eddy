import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect } from "react";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { ClusterInfo, Message, Thread } from "../api/types";
import { AppStateProvider, useAppState } from "../lib/appState";
import { resource } from "../test/fixtures";
import { AskAIPanel, AskAIProvider, turnsFromMessages } from "./AskAI";

const api = vi.hoisted(() => ({ askAI: vi.fn(), getThread: vi.fn(), listThreads: vi.fn() }));
vi.mock("../api/endpoints", async (orig) => ({
  ...(await orig<typeof import("../api/endpoints")>()),
  ...api,
}));
vi.mock("../api/queries", async (orig) => ({
  ...(await orig<typeof import("../api/queries")>()),
  useMe: () => ({ data: { features: { ai: true, aiProvider: "anthropic" } } }),
}));

const cluster: ClusterInfo = {
  name: "staging",
  displayName: "staging",
  protected: false,
  order: 1,
  connected: true,
};
const apps = resource("apps");
const author = { type: "human", subject: "u", display: "dana", via: "askai" } as const;
const ai = { type: "ai", subject: "ai", display: "AI", via: "askai", client: "claude" } as const;
const msg = (id: string, a: Message["author"], body: string, meta?: unknown): Message => ({
  id,
  threadId: "th_1",
  author: a,
  body,
  meta,
  createdAt: "2026-01-01T00:00:00Z",
});
const thread = (id: string, title: string, name = "apps"): Thread => ({
  id,
  ref: { cluster: "staging", group: apps.group, kind: "Kustomization", namespace: "flux-system", name },
  type: "ask",
  visibility: "private",
  title,
  status: "open",
  createdBy: author,
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
  messageCount: 2,
});

beforeAll(() => {
  Element.prototype.scrollTo ??= () => {};
});
beforeEach(() => {
  api.askAI.mockReset().mockResolvedValue({
    threadId: "th_new",
    message: msg("m_ai", ai, "All good."),
    steps: [],
  });
  api.getThread.mockReset();
  api.listThreads.mockReset().mockResolvedValue({ items: [] });
});

function Probe() {
  const { pane, startNewChat, ask } = useAppState();
  // biome-ignore lint/correctness/useExhaustiveDependencies: open Ask AI once, like the tab or `a`
  useEffect(() => ask(), []);
  return (
    <>
      <output aria-label="pane">{pane}</output>
      <button type="button" onClick={startNewChat}>
        palette new chat
      </button>
    </>
  );
}

function setup() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <AppStateProvider>
        <AskAIProvider>
          <Probe />
          <AskAIPanel cluster={cluster} resource={apps} />
        </AskAIProvider>
      </AppStateProvider>
    </QueryClientProvider>,
  );
  return userEvent.setup();
}

const box = () => screen.getByRole("textbox", { name: "Ask AI" });

describe("turnsFromMessages", () => {
  it("maps stored messages to turns and restores tool steps", () => {
    const steps = [{ tool: "get_events", args: {}, bytes: 10 }];
    const turns = turnsFromMessages([msg("1", author, "why?"), msg("2", ai, "because", { steps })]);
    expect(turns[0]).toEqual({ kind: "user", text: "why?" });
    expect(turns[1]).toMatchObject({ kind: "ai", steps });
  });
});

describe("Ask AI panel", () => {
  it("starts a new chat without sending the old thread id", async () => {
    const user = setup();
    expect(screen.getByRole("button", { name: /New chat/ })).toBeDisabled();
    await user.type(box(), "first{Enter}");
    await screen.findByText("All good.");
    expect(api.askAI).toHaveBeenLastCalledWith(expect.not.objectContaining({ threadId: expect.anything() }));
    await user.type(box(), "second{Enter}");
    await waitFor(() => expect(api.askAI).toHaveBeenCalledTimes(2));
    expect(api.askAI.mock.calls[1]?.[0]).toMatchObject({ threadId: "th_new" });

    await user.click(screen.getByRole("button", { name: /New chat/ }));
    expect(screen.queryByText("All good.")).toBeNull();
    await user.type(box(), "third{Enter}");
    await waitFor(() => expect(api.askAI).toHaveBeenCalledTimes(3));
    expect(api.askAI.mock.calls[2]?.[0].threadId).toBeUndefined();
  });

  it("clears the conversation from the palette's New Ask AI chat signal", async () => {
    const user = setup();
    await user.type(box(), "first{Enter}");
    await screen.findByText("All good.");
    await user.click(screen.getByRole("button", { name: "palette new chat" }));
    await waitFor(() => expect(screen.queryByText("All good.")).toBeNull());
  });

  it("lists earlier chats for this resource, newest first, and reopens one", async () => {
    api.listThreads.mockResolvedValue({
      items: [thread("th_1", "Why is apps failing?"), thread("th_2", "Other resource", "other")],
    });
    api.getThread.mockResolvedValue({
      thread: thread("th_1", "Why is apps failing?"),
      messages: [msg("1", author, "Why is apps failing?"), msg("2", ai, "A bad path.")],
    });
    const user = setup();
    await user.click(screen.getByRole("button", { name: "History" }));
    expect(api.listThreads).toHaveBeenCalledWith(
      expect.objectContaining({ type: "ask", cluster: "staging", kind: "Kustomization", name: "apps" }),
    );
    const item = await screen.findByRole("menuitem", { name: /Why is apps failing/ });
    expect(screen.queryByRole("menuitem", { name: /Other resource/ })).toBeNull();
    await user.click(item);
    await screen.findByText("A bad path.");
    await user.type(box(), "and now?{Enter}");
    await waitFor(() => expect(api.askAI).toHaveBeenCalled());
    expect(api.askAI.mock.calls[0]?.[0]).toMatchObject({ threadId: "th_1", question: "and now?" });
  });

  it("Esc clears a draft first, then returns to Details", async () => {
    const user = setup();
    await user.type(box(), "half a question");
    await user.keyboard("{Escape}");
    expect(box()).toHaveValue("");
    expect(screen.getByLabelText("pane")).toHaveTextContent("ai");
    await user.keyboard("{Escape}");
    expect(screen.getByLabelText("pane")).toHaveTextContent("details");
  });

  it("Esc on a control inside the panel returns to Details, and closes the history menu first", async () => {
    const user = setup();
    await user.click(screen.getByRole("button", { name: "History" }));
    await screen.findByRole("menu");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    screen.getByRole("button", { name: "History" }).focus();
    await user.keyboard("{Escape}");
    expect(screen.getByLabelText("pane")).toHaveTextContent("details");
  });
});
