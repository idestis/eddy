import "@fontsource-variable/geist";
import "@fontsource-variable/geist-mono";
import "./styles/app.css";

import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { initTheme } from "./lib/theme";
import { createAppRouter, createQueryClient } from "./router";

async function start() {
  // Statically false in production builds, so the mock module is dropped.
  if (import.meta.env.DEV && import.meta.env.VITE_MOCK === "1") {
    const { installMock } = await import("./mock");
    installMock();
  }
  initTheme();

  const queryClient = createQueryClient();
  const router = createAppRouter(queryClient);
  const root = document.getElementById("root");
  if (!root) throw new Error("missing #root");
  createRoot(root).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>,
  );
}

void start();
