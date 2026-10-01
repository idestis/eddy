import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { Ref, Resource } from "../../api/types";
import { blockage, buildGraph } from "../../lib/graph";
import { layoutGraph } from "../../lib/graphLayout";
import { kindInfo } from "../../lib/kinds";
import { NO_MOTION } from "../../lib/liveMotion";
import { GraphCanvas } from "./GraphCanvas";

const res = (kind: string, name: string, extra: Partial<Resource> = {}): Resource => {
  const group = kindInfo(kind).group;
  return {
    group,
    kind,
    namespace: "flux-system",
    name,
    id: `${group}/${kind}/flux-system/${name}`,
    version: "v1",
    status: "ready",
    resourceVersion: "1",
    ...extra,
  };
};
const ref = (r: Resource): Ref => ({ group: r.group, kind: r.kind, namespace: r.namespace, name: r.name });

const git = res("GitRepository", "repo");
const infra = res("Kustomization", "infra", { source: ref(git) });
const apps = res("Kustomization", "apps", { source: ref(git), dependsOn: [ref(infra)], status: "failed" });
const layout = layoutGraph(buildGraph([git, infra, apps], { kinds: "flux" }));

function Harness({ onOpen, onEscape }: { onOpen: (id: string) => void; onEscape: () => boolean }) {
  const [selected, setSelected] = useState<string | undefined>(infra.id);
  return (
    <GraphCanvas
      layout={layout}
      label="Flux dependency graph"
      selectedId={selected}
      onSelect={setSelected}
      onOpen={onOpen}
      onEscape={onEscape}
      motion={NO_MOTION}
      requested={new Map()}
      marching={new Set()}
      blockage={blockage(layout.index)}
    />
  );
}

describe("GraphCanvas", () => {
  it("labels nodes for screen readers and walks the graph with the keyboard", () => {
    const onOpen = vi.fn();
    const onEscape = vi.fn(() => false);
    render(<Harness onOpen={onOpen} onEscape={onEscape} />);
    expect(screen.getByRole("region", { name: "Flux dependency graph" })).toBeInTheDocument();
    const appsNode = screen.getByRole("button", {
      name: "Kustomization apps, failed, depends on infra, source repo",
    });
    const infraNode = screen.getByRole("button", { name: "Kustomization infra, ready, source repo" });
    expect(infraNode).toHaveAttribute("aria-pressed", "true");
    // One tab stop: the selected node.
    expect(infraNode).toHaveAttribute("tabindex", "0");
    expect(appsNode).toHaveAttribute("tabindex", "-1");

    fireEvent.keyDown(infraNode, { key: "ArrowRight" });
    expect(appsNode).toHaveAttribute("aria-pressed", "true");
    fireEvent.keyDown(appsNode, { key: "h" });
    expect(infraNode).toHaveAttribute("aria-pressed", "true");
    fireEvent.keyDown(infraNode, { key: "ArrowLeft" });
    expect(screen.getByRole("button", { name: "GitRepository repo, ready" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );

    fireEvent.keyDown(screen.getByRole("button", { name: "GitRepository repo, ready" }), { key: "Enter" });
    expect(onOpen).toHaveBeenCalledWith(git.id);
    fireEvent.keyDown(appsNode, { key: "Escape" });
    expect(onEscape).toHaveBeenCalled();
  });

  it("dependsOn edges point from what goes first to what goes second", () => {
    const { container } = render(<Harness onOpen={() => {}} onEscape={() => false} />);
    const deps = container.querySelectorAll('path.gedge[data-type="dependsOn"]');
    expect(deps).toHaveLength(1);
    const infraX = layout.nodes.get(infra.id)?.x ?? 0;
    const appsX = layout.nodes.get(apps.id)?.x ?? 0;
    expect(appsX).toBeGreaterThan(infraX);
    expect(container.querySelectorAll('path.gedge[data-type="source"]')).toHaveLength(2);
  });
});
