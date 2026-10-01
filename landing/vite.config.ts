import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The site is served from a sub-path on GitHub Pages (https://idestis.github.io/eddy/).
// Moving to a custom domain later is one line: LANDING_BASE=/ (and LANDING_SITE=https://example.com).
const normalizeBase = (value: string): string =>
  `/${value.replace(/^\/+|\/+$/g, "")}/`.replace(/^\/\/$/, "/");
const base = normalizeBase(process.env.LANDING_BASE ?? "/eddy/");
const site = (process.env.LANDING_SITE ?? "https://idestis.github.io").replace(/\/+$/, "");

export default defineConfig(({ isPreview }) => ({
  base,
  plugins: [
    // The router plugin must run before the React plugin.
    tanstackRouter({
      target: "react",
      // Each route's component is its own chunk. Pages are prerendered and the router loads the
      // matched route's chunk before hydrating, so there is no waterfall on first paint.
      autoCodeSplitting: true,
      routesDirectory: "./src/routes",
      generatedRouteTree: "./src/routeTree.gen.ts",
      quoteStyle: "double",
      semicolons: true,
    }),
    react(),
    tailwindcss(),
  ],
  define: { __SITE_URL__: JSON.stringify(site) },
  server: { port: 5174, strictPort: true },
  preview: { port: 4174, strictPort: true },
  // Preview serves the prerendered pages as a multi-page site; dev falls back to index.html.
  appType: isPreview ? "mpa" : "spa",
  build: {
    target: "es2023",
    sourcemap: false,
    // Fonts are self-hosted files; never inline them.
    assetsInlineLimit: (file) => (/\.(woff2?|ttf|otf)$/.test(file) ? false : undefined),
  },
}));
