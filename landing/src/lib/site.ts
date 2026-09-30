/** Build-time site constants. Base path and origin come from LANDING_BASE / LANDING_SITE (vite.config.ts). */
export const BASE = import.meta.env.BASE_URL; // "/eddy/" or "/"
export const SITE_URL = __SITE_URL__;

export const REPO_URL = "https://github.com/idestis/eddy";
export const REPO_BLOB = `${REPO_URL}/blob/HEAD`;
export const REPO_TREE = `${REPO_URL}/tree/HEAD`;

/** Router basepath: no trailing slash, "/" when served from the root. */
export const ROUTER_BASEPATH = BASE === "/" ? "/" : BASE.replace(/\/$/, "");

/** Absolute URL for a route path such as "/docs/mcp/" (or an asset such as "assets/og.png"). */
export function absoluteUrl(path: string): string {
  return `${SITE_URL}${BASE}${path.replace(/^\/+/, "")}`;
}

export const OG_IMAGE = absoluteUrl("assets/og.png");
