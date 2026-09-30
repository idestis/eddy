import { Link } from "@tanstack/react-router";
import { REPO_BLOB, REPO_URL } from "../lib/site";

const link = "inline-flex min-h-10 items-center text-ink-2";

export function Footer() {
  return (
    <footer className="border-t border-line py-8 text-[0.9rem] text-ink-3">
      <div className="wrap flex flex-wrap items-center justify-between gap-x-8 gap-y-3">
        <p className="m-0">
          Eddy is open source under the{" "}
          <a href={`${REPO_BLOB}/LICENSE`} className="text-ink-2">
            Apache License 2.0
          </a>
          . v1.0, early release.
        </p>
        <ul className="m-0 flex list-none flex-wrap gap-x-5 p-0">
          <li>
            <Link to="/" className={link}>
              Home
            </Link>
          </li>
          <li>
            <Link to="/docs/" className={link}>
              Docs
            </Link>
          </li>
          <li>
            <a href={REPO_URL} className={link}>
              GitHub
            </a>
          </li>
          <li>
            <a href={`${REPO_BLOB}/CONTRIBUTING.md`} className={link}>
              Contributing
            </a>
          </li>
          <li>
            <a href={`${REPO_BLOB}/SECURITY.md`} className={link}>
              Report a vulnerability
            </a>
          </li>
        </ul>
      </div>
    </footer>
  );
}
