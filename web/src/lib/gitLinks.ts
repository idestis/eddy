// Web links for Flux sources and revisions. A GitRepository's URL becomes its page on the
// forge; a revision such as `main@sha1:fb9e25d…` becomes a branch/tag link and a commit link.
// Only http(s) links are ever produced. Credentials in a URL are dropped before it is shown
// or linked. OCI and Helm sources have no page to link to, so they stay plain text.

export type Forge = "github" | "gitlab" | "bitbucket" | "azure" | "gitea" | "generic";

export interface Repo {
  /** https://host/org/repo, no credentials, no `.git`. */
  web: string;
  /** host/org/repo, for display. */
  display: string;
  forge: Forge;
}

// user@host:path, or a dotted host (so `javascript:alert(1)` is not one).
const SCP = /^(?:[\w.-]+@([\w.-]+)|([\w-]+(?:\.[\w-]+)+)):(?!\/)([\w./~-]+)$/;

function forgeOf(host: string): Forge {
  const h = host.toLowerCase();
  if (h.includes("github")) return "github";
  if (h.includes("gitlab")) return "gitlab";
  if (h.includes("bitbucket")) return "bitbucket";
  if (h === "dev.azure.com" || h.endsWith(".visualstudio.com") || h === "ssh.dev.azure.com") return "azure";
  if (/gitea|forgejo|codeberg/.test(h)) return "gitea";
  return "generic";
}

function clean(path: string): string {
  return path
    .replace(/^\/+|\/+$/g, "")
    .replace(/\.git$/, "")
    .replace(/\/+$/, "");
}

function repoOf(scheme: "http" | "https", host: string, path: string): Repo | undefined {
  let p = clean(path);
  if (!host || !p) return undefined;
  let h = host;
  if (h.toLowerCase() === "ssh.dev.azure.com") {
    // ssh.dev.azure.com:v3/<org>/<project>/<repo>
    const m = /^v3\/([^/]+)\/([^/]+)\/(.+)$/.exec(p);
    if (!m) return undefined;
    h = "dev.azure.com";
    p = `${m[1]}/${m[2]}/_git/${m[3]}`;
  }
  return { web: `${scheme}://${h}/${p}`, display: `${h}/${p}`, forge: forgeOf(h) };
}

/** The forge page of a Git URL, or undefined for anything that is not a web-reachable Git URL. */
export function repoOfUrl(raw: string | undefined): Repo | undefined {
  const url = raw?.trim();
  if (!url) return undefined;
  if (!url.includes("://")) {
    const m = SCP.exec(url);
    return m ? repoOf("https", m[1] ?? m[2] ?? "", m[3] ?? "") : undefined;
  }
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return undefined;
  }
  const scheme = u.protocol.replace(/:$/, "");
  if (scheme === "http" || scheme === "https") return repoOf(scheme, u.host, u.pathname);
  if (scheme === "ssh" || scheme === "git" || scheme === "git+ssh")
    return repoOf("https", u.hostname, u.pathname);
  return undefined;
}

/** The URL with credentials removed, for showing a source that has no web page. */
export function withoutCredentials(raw: string): string {
  try {
    const u = new URL(raw);
    if (!u.username && !u.password) return raw;
    u.username = "";
    u.password = "";
    return u.toString();
  } catch {
    return raw.replace(/^([a-z][a-z0-9+.-]*:\/\/)[^/@]*@/i, "$1");
  }
}

export interface Revision {
  raw: string;
  /** Branch or tag name. */
  ref?: string;
  refKind?: "branch" | "tag";
  /** A Git commit. Only set for sha1 revisions, never for OCI/Helm digests. */
  sha?: string;
  /** An OCI or Helm digest: shown, never linked. */
  digest?: string;
}

const SHA = /^[0-9a-f]{7,40}$/i;

/** Splits the revision formats Flux reports. Unknown formats come back with only `raw`. */
export function parseRevision(raw: string | undefined): Revision | undefined {
  const rev = raw?.trim();
  if (!rev) return undefined;
  const git = /^(.*?)@?sha1:([0-9a-f]{7,40})$/i.exec(rev);
  if (git) return { raw: rev, sha: git[2], ...refOf(git[1] ?? "") };
  const oci = /^(.*?)@?sha256:([0-9a-f]{16,})$/i.exec(rev);
  if (oci) return { raw: rev, digest: oci[2], ...(oci[1] ? { ref: oci[1] } : {}) };
  if (SHA.test(rev)) return { raw: rev, sha: rev };
  // The older `<branch>/<40 hex>` form.
  const old = /^(.+)\/([0-9a-f]{40})$/i.exec(rev);
  if (old) return { raw: rev, sha: old[2], ...refOf(old[1] ?? "") };
  return { raw: rev };
}

function refOf(s: string): Pick<Revision, "ref" | "refKind"> {
  if (!s) return {};
  if (s.startsWith("refs/tags/")) return { ref: s.slice("refs/tags/".length), refKind: "tag" };
  if (s.startsWith("refs/heads/")) return { ref: s.slice("refs/heads/".length), refKind: "branch" };
  return { ref: s, refKind: "branch" };
}

const enc = (p: string) => p.split("/").map(encodeURIComponent).join("/");

/** The branch or tag page. */
export function refUrl(repo: Repo, ref: string, kind: "branch" | "tag" = "branch"): string {
  const r = enc(ref);
  switch (repo.forge) {
    case "gitlab":
      return `${repo.web}/-/tree/${r}`;
    case "bitbucket":
      return `${repo.web}/src/${r}`;
    case "azure":
      return `${repo.web}?version=${kind === "tag" ? "GT" : "GB"}${encodeURIComponent(ref)}`;
    case "gitea":
      return `${repo.web}/src/${kind}/${r}`;
    default:
      return `${repo.web}/tree/${r}`;
  }
}

export function commitUrl(repo: Repo, sha: string): string {
  switch (repo.forge) {
    case "gitlab":
      return `${repo.web}/-/commit/${sha}`;
    case "bitbucket":
      return `${repo.web}/commits/${sha}`;
    default:
      return `${repo.web}/commit/${sha}`;
  }
}

/** A folder or file of the repository at a commit. */
export function pathUrl(repo: Repo, sha: string, path: string): string {
  const p = enc(path.replace(/^\.?\/+|\/+$/g, ""));
  if (!p) return `${repo.web}/${repo.forge === "gitlab" ? "-/" : ""}tree/${sha}`;
  switch (repo.forge) {
    case "gitlab":
      return `${repo.web}/-/tree/${sha}/${p}`;
    case "bitbucket":
      return `${repo.web}/src/${sha}/${p}`;
    case "azure":
      return `${repo.web}?path=/${p}&version=GC${sha}`;
    case "gitea":
      return `${repo.web}/src/commit/${sha}/${p}`;
    default:
      return `${repo.web}/tree/${sha}/${p}`;
  }
}

export const shortSha = (sha: string) => sha.slice(0, 7);
