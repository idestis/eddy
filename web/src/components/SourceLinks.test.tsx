import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { keys } from "../api/queries";
import { resource } from "../test/fixtures";
import { RevisionValue, SourceUrl } from "./SourceLinks";

const SHA = "fb9e25d0123456789abcdef0123456789abcdef0";

function wrap(qc: QueryClient, node: React.ReactNode) {
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>;
}

describe("SourceUrl", () => {
  it("links a Git URL to the forge in a new tab, without credentials", () => {
    render(<SourceUrl url="https://bot:secret@github.com/Carmoola/fluxcd.git" />);
    const a = screen.getByRole("link");
    expect(a).toHaveAttribute("href", "https://github.com/Carmoola/fluxcd");
    expect(a).toHaveAttribute("target", "_blank");
    expect(a).toHaveAttribute("rel", "noopener noreferrer");
    expect(a).toHaveTextContent("github.com/Carmoola/fluxcd");
    expect(document.body.innerHTML).not.toContain("secret");
  });

  it("shows OCI as text with a copy button and no link", () => {
    render(<SourceUrl url="oci://ghcr.io/org/manifests" />);
    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByText("oci://ghcr.io/org/manifests")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy the source URL" })).toBeInTheDocument();
  });
});

describe("RevisionValue", () => {
  const qc = new QueryClient();
  qc.setQueryData(keys.resources("dev"), {
    resourceVersion: "1",
    items: [
      resource("fluxcd", {
        group: "source.toolkit.fluxcd.io",
        kind: "GitRepository",
        namespace: "flux-system",
        url: "ssh://git@gitlab.com/g/s/fluxcd.git",
      }),
    ],
  });

  it("links the branch and the commit of the source repository", () => {
    const ks = resource("apps", {
      kind: "Kustomization",
      namespace: "flux-system",
      revision: `main@sha1:${SHA}`,
      source: {
        group: "source.toolkit.fluxcd.io",
        kind: "GitRepository",
        namespace: "flux-system",
        name: "fluxcd",
      },
      details: [{ label: "Path", value: "./clusters/prod" }],
    });
    render(wrap(qc, <RevisionValue cluster="dev" r={ks} />));
    const hrefs = screen.getAllByRole("link").map((a) => a.getAttribute("href"));
    expect(hrefs).toEqual([
      "https://gitlab.com/g/s/fluxcd/-/tree/main",
      `https://gitlab.com/g/s/fluxcd/-/commit/${SHA}`,
      `https://gitlab.com/g/s/fluxcd/-/tree/${SHA}/clusters/prod`,
    ]);
    expect(screen.getByText("fb9e25d")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy the commit SHA" })).toBeInTheDocument();
  });

  it("does not link a digest", () => {
    const oci = resource("chart", { kind: "HelmChart", revision: `1.2.3@sha256:${"ab".repeat(32)}` });
    render(wrap(qc, <RevisionValue cluster="dev" r={oci} />));
    expect(screen.queryByRole("link")).toBeNull();
  });
});
