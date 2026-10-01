import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { renderToString } from "react-dom/server";
import { mergeHead, renderHead } from "./lib/head";
import { absoluteUrl, ROUTER_BASEPATH } from "./lib/site";
import { createAppRouter } from "./router";

/** Render one URL (a path below the base, e.g. "/docs/mcp/") to HTML plus its <head> tags. */
export async function render(path: string) {
  const router = createAppRouter(
    createMemoryHistory({ initialEntries: [ROUTER_BASEPATH === "/" ? path : `${ROUTER_BASEPATH}${path}`] }),
    { prerender: true },
  );
  await router.load();
  const html = renderToString(<RouterProvider router={router} />);
  const head = renderHead(
    mergeHead(router.state.matches.map((m) => ({ meta: m.meta, links: m.links }) as never)),
  );
  // Only the root route matched: nothing else claimed this URL.
  const notFound = router.state.matches.every((m) => m.routeId === "__root__");
  return { html, head, notFound };
}

/** Every concrete page of the site as a route path, for prerendering and the sitemap. */
export function routePaths(): string[] {
  const router = createAppRouter();
  return Object.keys(router.routesByPath)
    .filter((p) => !p.includes("$") && !p.includes("*"))
    .map((p) => (p.endsWith("/") ? p : `${p}/`))
    .sort();
}

export function sitemapXml(paths: string[]): string {
  const lastmod = new Date().toISOString().slice(0, 10);
  const urls = paths
    .map((p) => `  <url><loc>${absoluteUrl(p)}</loc><lastmod>${lastmod}</lastmod></url>`)
    .join("\n");
  return `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${urls}\n</urlset>\n`;
}

export function robotsTxt(): string {
  return `User-agent: *\nAllow: /\n\nSitemap: ${absoluteUrl("sitemap.xml")}\n`;
}
