import { createRouter, type RouterHistory } from "@tanstack/react-router";
import { ROUTER_BASEPATH } from "./lib/site";
import { routeTree } from "./routeTree.gen";

export function createAppRouter(history?: RouterHistory, { prerender = false } = {}) {
  return createRouter({
    routeTree,
    history,
    basepath: ROUTER_BASEPATH,
    // GitHub Pages serves dist/docs/mcp/index.html at /docs/mcp/, so keep the slash everywhere.
    trailingSlash: "always",
    // The server would inject a restoration <script> that the client tree does not have.
    scrollRestoration: !prerender,
    defaultPreload: "intent",
    defaultPreloadStaleTime: 0,
  });
}

export type AppRouter = ReturnType<typeof createAppRouter>;

declare module "@tanstack/react-router" {
  interface Register {
    router: AppRouter;
  }
}
