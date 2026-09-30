// Run hack/check-links.py against dist/. That script resolves absolute links ("/eddy/...") from the
// site root, so mirror dist/ under the base path in a temp dir first.
import { spawnSync } from "node:child_process";
import { cpSync, existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const dist = join(root, "dist");
if (!existsSync(dist)) {
  console.error("dist/ not found. Run `pnpm build` first.");
  process.exit(1);
}
const base = `/${(process.env.LANDING_BASE ?? "/eddy/").replace(/^\/+|\/+$/g, "")}`.replace(/^\/$/, "");

const tmp = mkdtempSync(join(tmpdir(), "eddy-links-"));
try {
  cpSync(dist, join(tmp, base), { recursive: true });
  const py = spawnSync("python3", [resolve(root, "../hack/check-links.py"), tmp], { stdio: "inherit" });
  process.exitCode = py.status ?? 1;
} finally {
  rmSync(tmp, { recursive: true, force: true });
}
