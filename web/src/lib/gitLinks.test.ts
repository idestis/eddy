import { describe, expect, it } from "vitest";
import { commitUrl, parseRevision, pathUrl, refUrl, repoOfUrl, withoutCredentials } from "./gitLinks";

const SHA = "fb9e25d0123456789abcdef0123456789abcdef0";

describe("repoOfUrl", () => {
  it.each([
    ["https://github.com/Carmoola/fluxcd.git", "https://github.com/Carmoola/fluxcd", "github"],
    ["https://github.com/Carmoola/fluxcd", "https://github.com/Carmoola/fluxcd", "github"],
    ["ssh://git@github.com/org/repo.git", "https://github.com/org/repo", "github"],
    ["ssh://git@github.com:22/org/repo", "https://github.com/org/repo", "github"],
    ["git@github.com:org/repo.git", "https://github.com/org/repo", "github"],
    ["https://gitlab.com/group/sub/deep/repo.git", "https://gitlab.com/group/sub/deep/repo", "gitlab"],
    ["git@gitlab.com:group/sub/repo.git", "https://gitlab.com/group/sub/repo", "gitlab"],
    ["https://bitbucket.org/team/repo.git", "https://bitbucket.org/team/repo", "bitbucket"],
    ["git@bitbucket.org:team/repo.git", "https://bitbucket.org/team/repo", "bitbucket"],
    ["https://dev.azure.com/org/proj/_git/repo", "https://dev.azure.com/org/proj/_git/repo", "azure"],
    ["git@ssh.dev.azure.com:v3/org/proj/repo", "https://dev.azure.com/org/proj/_git/repo", "azure"],
    ["https://codeberg.org/me/repo.git", "https://codeberg.org/me/repo", "gitea"],
    ["https://git.example.com/me/repo.git", "https://git.example.com/me/repo", "generic"],
    ["https://github.mycorp.com/me/repo/", "https://github.mycorp.com/me/repo", "github"],
  ])("%s", (raw, web, forge) => {
    expect(repoOfUrl(raw)).toMatchObject({ web, forge });
  });

  it("strips credentials", () => {
    const r = repoOfUrl("https://user:token@github.com/org/repo.git");
    expect(r?.web).toBe("https://github.com/org/repo");
    expect(r?.display).toBe("github.com/org/repo");
    expect(withoutCredentials("https://user:token@host.example/x")).toBe("https://host.example/x");
    expect(withoutCredentials("oci://ghcr.io/org/chart")).toBe("oci://ghcr.io/org/chart");
  });

  it.each([
    "oci://ghcr.io/org/charts/podinfo",
    "s3://bucket/key",
    "file:///tmp/x",
    "javascript:alert(1)",
    "",
    "nothing",
  ])("does not link %s", (raw) => {
    expect(repoOfUrl(raw)).toBeUndefined();
  });
});

describe("parseRevision", () => {
  it.each([
    [`main@sha1:${SHA}`, { ref: "main", refKind: "branch", sha: SHA }],
    [`refs/tags/v1.2.3@sha1:${SHA}`, { ref: "v1.2.3", refKind: "tag", sha: SHA }],
    [`refs/heads/feature/x@sha1:${SHA}`, { ref: "feature/x", refKind: "branch", sha: SHA }],
    [`sha1:${SHA}`, { sha: SHA }],
    [SHA, { sha: SHA }],
    [`main/${SHA}`, { ref: "main", sha: SHA }],
    ["sha256:" + "ab".repeat(32), { digest: "ab".repeat(32) }],
    ["1.2.3@sha256:" + "cd".repeat(32), { ref: "1.2.3", digest: "cd".repeat(32) }],
    ["6.7.2", { raw: "6.7.2" }],
  ])("%s", (raw, want) => {
    expect(parseRevision(raw)).toMatchObject(want);
  });

  it("gives digests no commit", () => {
    expect(parseRevision("sha256:" + "ab".repeat(32))?.sha).toBeUndefined();
    expect(parseRevision("1.2.3@sha256:" + "cd".repeat(32))?.sha).toBeUndefined();
  });
});

describe("forge URLs", () => {
  const cases = [
    ["github", "https://github.com/o/r", "/tree/main", "/commit/S", "/tree/S/apps/x"],
    ["gitlab", "https://gitlab.com/g/s/r", "/-/tree/main", "/-/commit/S", "/-/tree/S/apps/x"],
    ["bitbucket", "https://bitbucket.org/t/r", "/src/main", "/commits/S", "/src/S/apps/x"],
    ["gitea", "https://codeberg.org/o/r", "/src/branch/main", "/commit/S", "/src/commit/S/apps/x"],
    ["generic", "https://git.example.com/o/r", "/tree/main", "/commit/S", "/tree/S/apps/x"],
  ] as const;
  it.each(cases)("%s", (_, web, tree, commit, path) => {
    const repo = repoOfUrl(web);
    expect(repo).toBeDefined();
    if (!repo) return;
    expect(refUrl(repo, "main")).toBe(web + tree);
    expect(commitUrl(repo, "S")).toBe(web + commit);
    expect(pathUrl(repo, "S", "./apps/x/")).toBe(web + path);
  });

  it("azure", () => {
    const repo = repoOfUrl("https://dev.azure.com/org/proj/_git/repo");
    if (!repo) throw new Error("no repo");
    expect(refUrl(repo, "main")).toBe("https://dev.azure.com/org/proj/_git/repo?version=GBmain");
    expect(refUrl(repo, "v1", "tag")).toBe("https://dev.azure.com/org/proj/_git/repo?version=GTv1");
    expect(commitUrl(repo, "S")).toBe("https://dev.azure.com/org/proj/_git/repo/commit/S");
    expect(pathUrl(repo, "S", "apps")).toBe(
      "https://dev.azure.com/org/proj/_git/repo?path=/apps&version=GCS",
    );
  });

  it("keeps slashes in branch names and encodes the rest", () => {
    const repo = repoOfUrl("https://github.com/o/r");
    if (!repo) throw new Error("no repo");
    expect(refUrl(repo, "feature/a b")).toBe("https://github.com/o/r/tree/feature/a%20b");
  });
});
