import { Link } from "@tanstack/react-router";
import { docsNav } from "../lib/docs";

/** Previous / next links, derived from the docs navigation order. */
export function Pager({ current }: { current: (typeof docsNav)[number]["to"] }) {
  const i = docsNav.findIndex((d) => d.to === current);
  const prev = docsNav[i - 1];
  const next = docsNav[i + 1];
  return (
    <div className="pager">
      {prev ? (
        <Link to={prev.to} rel="prev">
          ← {prev.title}
        </Link>
      ) : (
        <span />
      )}
      {next && (
        <Link to={next.to} rel="next">
          {next.title} →
        </Link>
      )}
    </div>
  );
}
