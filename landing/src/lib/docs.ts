/** Docs navigation, in reading order. Used by the sidebar and the previous/next pager. */
export const docsNav = [
  { group: "Start", to: "/docs/", title: "Overview" },
  { group: "Start", to: "/docs/quickstart/", title: "Quickstart (kind)" },
  { group: "Set up", to: "/docs/install/", title: "Install the hub" },
  { group: "Set up", to: "/docs/sign-in/", title: "Sign-in" },
  { group: "Set up", to: "/docs/clusters/", title: "Add clusters" },
  { group: "Set up", to: "/docs/access/", title: "Access and RBAC" },
  { group: "Use", to: "/docs/using/", title: "Using Eddy" },
  { group: "Use", to: "/docs/ask-ai/", title: "Ask AI" },
  { group: "Use", to: "/docs/mcp/", title: "MCP & Claude Code" },
  { group: "Run", to: "/docs/operations/", title: "Operations" },
  { group: "Run", to: "/docs/security/", title: "Security model" },
  { group: "Run", to: "/docs/roadmap-faq/", title: "Roadmap & FAQ" },
] as const;

export type DocsPath = (typeof docsNav)[number]["to"];
