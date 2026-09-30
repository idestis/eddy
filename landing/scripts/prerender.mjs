// Prerender every route to dist/<route>/index.html, plus 404.html, sitemap.xml and robots.txt.
// Input: dist/ (client build, whose index.html is the template) and dist-ssr/ (SSR build of src/entry-server.tsx).
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const root = resolve(import.meta.dirname, "..");
const dist = join(root, "dist");
const ssr = join(root, "dist-ssr");

const { render, routePaths, sitemapXml, robotsTxt } = await import(
  pathToFileURL(join(ssr, "entry-server.js")).href
);
const template = await readFile(join(dist, "index.html"), "utf8");
if (!template.includes("<!--app-head-->") || !template.includes("<!--app-html-->")) {
  throw new Error("dist/index.html lost its <!--app-head--> / <!--app-html--> placeholders");
}

const page = ({ head, html }) =>
  template.replace("<!--app-head-->", () => head).replace("<!--app-html-->", () => html);

const paths = routePaths();
for (const path of paths) {
  const result = await render(path);
  if (result.notFound) throw new Error(`route ${path} rendered as not found`);
  const file = join(dist, path, "index.html");
  await mkdir(dirname(file), { recursive: true });
  await writeFile(file, page(result));
  console.log(`prerendered ${path}`);
}

// GitHub Pages serves 404.html for any unknown URL; render a path that matches no route.
const missing = await render("/__not-found__/");
if (!missing.notFound) throw new Error("expected /__not-found__/ to render as not found");
await writeFile(join(dist, "404.html"), page(missing));
console.log("prerendered 404.html");

await writeFile(join(dist, "sitemap.xml"), sitemapXml(paths));
await writeFile(join(dist, "robots.txt"), robotsTxt());
// The template has served its purpose; index.html was overwritten by the "/" route above.
await rm(ssr, { recursive: true, force: true });
