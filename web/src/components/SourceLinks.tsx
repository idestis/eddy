// A source URL and a revision as links to the forge, with a copy button each. The links are
// built by lib/gitLinks.ts (http(s) only). A Kustomization or HelmRelease has no URL of its
// own, so its commit link uses the URL of the GitRepository it points to.

import { useQueryClient } from "@tanstack/react-query";
import { keys } from "../api/queries";
import type { Resource, ResourceSnapshot } from "../api/types";
import {
  commitUrl,
  parseRevision,
  pathUrl,
  type Repo,
  refUrl,
  repoOfUrl,
  shortSha,
  withoutCredentials,
} from "../lib/gitLinks";
import { Icon } from "./Icon";
import { useToast } from "./Toasts";

const LINK = "inline-flex min-w-0 items-center gap-1 text-ink no-underline hover:underline";
const ICON_BTN =
  "inline-flex size-5 shrink-0 items-center justify-center rounded-md text-ink-3 hover:bg-surface-sunken hover:text-ink";

function ExternalLink({
  href,
  title,
  children,
}: {
  href: string;
  title?: string;
  children: React.ReactNode;
}) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" title={title} className={LINK}>
      {children}
      <Icon name="external" className="size-3 shrink-0 text-ink-3" />
    </a>
  );
}

function Copy({ text, label }: { text: string; label: string }) {
  const toast = useToast();
  return (
    <button
      type="button"
      className={ICON_BTN}
      aria-label={label}
      title={label}
      onClick={() =>
        navigator.clipboard.writeText(text).then(
          () => toast("Copied to the clipboard", "ok"),
          () => toast("Couldn't copy. Select the text and copy it by hand.", "bad"),
        )
      }
    >
      <Icon name="copy" className="size-3" />
    </button>
  );
}

/** The source URL: `host/org/repo` linking to the forge, or plain text for OCI, S3 and the like. */
export function SourceUrl({ url }: { url: string }) {
  const repo = repoOfUrl(url);
  const shown = withoutCredentials(url);
  return (
    <span className="inline-flex max-w-full items-center gap-1.5">
      {repo ? (
        <ExternalLink href={repo.web} title={shown}>
          <span className="truncate">{repo.display}</span>
        </ExternalLink>
      ) : (
        <span className="break-all" title={shown}>
          {shown}
        </span>
      )}
      <Copy text={shown} label="Copy the source URL" />
    </span>
  );
}

/** The repository a revision belongs to: the resource's own URL, or its source's. */
export function useRepoOf(cluster: string, r: Resource): Repo | undefined {
  const qc = useQueryClient();
  if (r.url) return repoOfUrl(r.url);
  if (!r.source) return undefined;
  const src = r.source;
  const items = qc.getQueryData<ResourceSnapshot>(keys.resources(cluster))?.items;
  const found = items?.find(
    (x) => x.kind === src.kind && x.namespace === src.namespace && x.name === src.name,
  );
  return repoOfUrl(found?.url);
}

/** `main` (branch or tag link) and `fb9e25d` (commit link), or the plain revision. */
export function RevisionValue({ cluster, r }: { cluster: string; r: Resource }) {
  const repo = useRepoOf(cluster, r);
  const rev = parseRevision(r.revision);
  if (!rev) return null;
  const path = r.details?.find((d) => d.label === "Path")?.value;
  if (!rev.sha) {
    return (
      <span className="inline-flex max-w-full items-center gap-1.5" title={rev.raw}>
        <span className="break-all">{rev.raw}</span>
        <Copy text={rev.raw} label="Copy the revision" />
      </span>
    );
  }
  const sha = rev.sha;
  return (
    <span className="inline-flex max-w-full flex-wrap items-center gap-x-2 gap-y-1" title={rev.raw}>
      {rev.ref &&
        (repo ? (
          <ExternalLink href={refUrl(repo, rev.ref, rev.refKind)}>{rev.ref}</ExternalLink>
        ) : (
          <span>{rev.ref}</span>
        ))}
      {repo ? (
        <ExternalLink href={commitUrl(repo, sha)}>{shortSha(sha)}</ExternalLink>
      ) : (
        <span>{shortSha(sha)}</span>
      )}
      {repo && path && (
        <ExternalLink href={pathUrl(repo, sha, path)} title={`${path} at ${shortSha(sha)}`}>
          {path}
        </ExternalLink>
      )}
      <Copy text={sha} label="Copy the commit SHA" />
    </span>
  );
}
