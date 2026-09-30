import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot, hydrateRoot } from "react-dom/client";
import { applyHead, mergeHead } from "./lib/head";
import { createAppRouter } from "./router";
import "./styles/app.css";

async function start() {
  const container = document.getElementById("root");
  if (!container) throw new Error("missing #root");

  const router = createAppRouter();
  // Resolve the first route before rendering so hydration sees the same tree as the prerendered HTML.
  await router.load();

  const syncHead = () =>
    applyHead(mergeHead(router.state.matches.map((m) => ({ meta: m.meta, links: m.links }) as never)));
  router.subscribe("onResolved", syncHead);

  const app = (
    <StrictMode>
      <RouterProvider router={router} />
    </StrictMode>
  );
  // Prerendered pages hydrate; in `vite dev` the container is empty and the app renders from scratch.
  if (container.hasChildNodes()) {
    // Tells the router the tree came from the server, so it skips the Suspense wrapper the server did not render.
    router.ssr = { manifest: undefined };
    hydrateRoot(container, app);
  } else {
    syncHead();
    createRoot(container).render(app);
  }
}

void start();
