/// <reference types="vitest/config" />
import { writeFileSync } from "node:fs";
import { resolve } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin, type ProxyOptions } from "vite";

const outDir = resolve(import.meta.dirname, "../internal/ui/dist");

// emptyOutDir wipes internal/ui/dist, including the placeholder that keeps
// the directory in Git so `go:embed` compiles on a fresh checkout.
function keepGitkeep(): Plugin {
  return {
    name: "eddy:keep-gitkeep",
    apply: "build",
    closeBundle() {
      writeFileSync(resolve(outDir, ".gitkeep"), "");
    },
  };
}

// The hub streams SSE on /api/v1/stream and on pod logs, so the proxy must
// not time out long-lived responses.
const hub: ProxyOptions = {
  target: "http://127.0.0.1:8080",
  changeOrigin: false,
  timeout: 0,
  proxyTimeout: 0,
};

export default defineConfig({
  plugins: [
    // The router plugin must run before the React plugin.
    tanstackRouter({
      target: "react",
      autoCodeSplitting: true,
      routesDirectory: "./src/routes",
      generatedRouteTree: "./src/routeTree.gen.ts",
      quoteStyle: "double",
      semicolons: true,
    }),
    react(),
    // Tailwind compiles to one static stylesheet at build time (no runtime <style>), so the
    // strict CSP (style-src 'self') holds.
    tailwindcss(),
    keepGitkeep(),
  ],
  server: {
    port: 5173,
    strictPort: true,
    proxy: { "/api": hub, "/auth": hub, "/mcp": hub },
  },
  build: {
    outDir,
    emptyOutDir: true,
    target: "es2023",
    sourcemap: false,
    // Fonts are self-hosted files; only small images may be inlined (CSP img-src allows data:).
    assetsInlineLimit: (file) => (/\.(woff2?|ttf|otf)$/.test(file) ? false : undefined),
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    css: false,
  },
});
