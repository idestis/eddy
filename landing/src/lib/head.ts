import { absoluteUrl, OG_IMAGE } from "./site";

export type MetaTag = Record<string, unknown>;
export interface LinkTag {
  rel: string;
  href: string;
}
export interface HeadData {
  meta: MetaTag[];
  links: LinkTag[];
}

const OG_ALT =
  "Eddy: a fast, keyboard-first, multi-cluster UI for Flux, with status rows and environment colour chips.";

export const SITE_TITLE = "Eddy: a fast, keyboard-first, multi-cluster UI for Flux";

interface PageHeadInput {
  title: string;
  description: string;
  /** Route path, e.g. "/docs/mcp/". Omitted for pages that must not be indexed. */
  path?: string;
  ogTitle?: string;
  ogDescription?: string;
  twitterDescription?: string;
  noindex?: boolean;
  jsonLd?: unknown;
}

/** Route `head` for one page: title, description, canonical, Open Graph and Twitter tags. */
export function pageHead(input: PageHeadInput): HeadData {
  const ogTitle = input.ogTitle ?? input.title;
  const ogDescription = input.ogDescription ?? input.description;
  const meta: MetaTag[] = [
    { title: input.title },
    { name: "description", content: input.description },
    { name: "robots", content: input.noindex ? "noindex" : "index, follow" },
  ];
  const links: LinkTag[] = [];
  if (input.path) {
    const url = absoluteUrl(input.path);
    links.push({ rel: "canonical", href: url });
    meta.push({ property: "og:url", content: url });
  }
  meta.push(
    { property: "og:type", content: input.path?.startsWith("/docs/") ? "article" : "website" },
    { property: "og:locale", content: "en_US" },
    { property: "og:site_name", content: "Eddy" },
    { property: "og:title", content: ogTitle },
    { property: "og:description", content: ogDescription },
    { property: "og:image", content: OG_IMAGE },
    { property: "og:image:width", content: "1200" },
    { property: "og:image:height", content: "630" },
    { property: "og:image:type", content: "image/png" },
    { property: "og:image:alt", content: OG_ALT },
    { name: "twitter:card", content: "summary_large_image" },
    { name: "twitter:title", content: ogTitle },
    { name: "twitter:description", content: input.twitterDescription ?? ogDescription },
    { name: "twitter:image", content: OG_IMAGE },
    { name: "twitter:image:alt", content: OG_ALT },
  );
  if (input.jsonLd) meta.push({ "script:ld+json": input.jsonLd });
  return { meta, links };
}

const keyOf = (m: MetaTag): string =>
  "title" in m ? "title" : "script:ld+json" in m ? "ld" : String(m.name ?? m.property ?? JSON.stringify(m));

/** Merge the head data of the matched routes; deeper routes win per tag. */
export function mergeHead(parts: Array<Partial<HeadData> | undefined>): HeadData {
  const meta = new Map<string, MetaTag>();
  const links = new Map<string, LinkTag>();
  for (const part of parts) {
    for (const m of part?.meta ?? []) meta.set(keyOf(m), m);
    for (const l of part?.links ?? []) links.set(l.rel, l);
  }
  return { meta: [...meta.values()], links: [...links.values()] };
}

const esc = (v: unknown): string =>
  String(v).replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

/** Server side: head tags as an HTML string for the prerendered file. */
export function renderHead(head: HeadData): string {
  const out: string[] = [];
  for (const m of head.meta) {
    if ("title" in m) out.push(`<title>${esc(m.title)}</title>`);
    else if ("script:ld+json" in m) {
      const json = JSON.stringify(m["script:ld+json"]).replace(/</g, "\\u003c");
      out.push(`<script type="application/ld+json">${json}</script>`);
    } else {
      out.push(
        `<meta ${Object.entries(m)
          .map(([k, v]) => `${k}="${esc(v)}"`)
          .join(" ")}>`,
      );
    }
  }
  for (const l of head.links) out.push(`<link rel="${esc(l.rel)}" href="${esc(l.href)}">`);
  return out.join("\n");
}

/** Client side: keep <head> in step with the current route after client navigations. */
export function applyHead(head: HeadData): void {
  for (const m of head.meta) {
    if ("title" in m) {
      document.title = String(m.title);
    } else if ("name" in m || "property" in m) {
      const attr = "name" in m ? "name" : "property";
      const key = String(m[attr]);
      let el = document.head.querySelector<HTMLMetaElement>(`meta[${attr}="${key}"]`);
      if (!el) {
        el = document.createElement("meta");
        el.setAttribute(attr, key);
        document.head.append(el);
      }
      el.setAttribute("content", String(m.content));
    }
  }
  // JSON-LD only describes the home page.
  const hasLd = head.meta.some((m) => "script:ld+json" in m);
  if (!hasLd) document.head.querySelector('script[type="application/ld+json"]')?.remove();
  for (const l of head.links) {
    let el = document.head.querySelector<HTMLLinkElement>(`link[rel="${l.rel}"]`);
    if (!el) {
      el = document.createElement("link");
      el.rel = l.rel;
      document.head.append(el);
    }
    el.href = l.href;
  }
  if (!head.links.some((l) => l.rel === "canonical")) {
    document.head.querySelector('link[rel="canonical"]')?.remove();
  }
}
