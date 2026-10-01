import { REPO_BLOB } from "../lib/site";

/** The "Full reference" pointer at the top of a docs page, linking to its file in the repository. */
export function FullRef({ path, label }: { path: string; label?: string }) {
  return (
    <p className="callout">
      <strong>Full reference:</strong> <a href={`${REPO_BLOB}/${path}`}>{label ?? path}</a> in the repository.
    </p>
  );
}
