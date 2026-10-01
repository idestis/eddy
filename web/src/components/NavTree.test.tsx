import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import type { NavNode } from "../lib/kinds";
import { branchClass, NavTree } from "./NavTree";

const leaf = (id: string): NavNode => ({ id, label: id, icon: "box", kinds: [id] });
const TREE: NavNode[] = [
  {
    id: "flux",
    label: "Flux",
    icon: "flux",
    kinds: [],
    children: [
      leaf("Kustomization"),
      leaf("HelmRelease"),
      {
        id: "sources",
        label: "Sources",
        icon: "git",
        kinds: [],
        children: [leaf("GitRepository"), leaf("OCIRepository"), leaf("Bucket")],
      },
    ],
  },
  leaf("other"),
];

function Harness({ initial = {} }: { initial?: Record<string, boolean> }) {
  const [open, setOpen] = useState<Record<string, boolean>>(initial);
  return (
    <NavTree
      nodes={TREE}
      isOpen={(n) => open[n.id] ?? false}
      setOpen={(id, on) => setOpen((o) => ({ ...o, [id]: on }))}
      renderLink={(n, props) => (
        <a
          href={`#${n.id}`}
          {...props}
          onClick={(e) => {
            e.preventDefault();
            props.onClick();
          }}
        >
          {n.label}
        </a>
      )}
    />
  );
}

const row = (id: string) => document.querySelector(`[data-nav-row="${id}"]`);

describe("NavTree", () => {
  it("gives every level the same ├─/└─ connectors", () => {
    render(<Harness initial={{ flux: true, sources: true }} />);
    // Root rows have no connector.
    expect(row("flux")?.className).not.toMatch(/tree-(mid|last)/);
    // Level 2: first and middle are ├─, last is └─.
    expect(row("Kustomization")).toHaveClass("tree-mid");
    expect(row("HelmRelease")).toHaveClass("tree-mid");
    expect(row("sources")).toHaveClass("tree-last");
    // Level 3, nested inside Sources' own connector column.
    expect(row("GitRepository")).toHaveClass("tree-mid");
    expect(row("OCIRepository")).toHaveClass("tree-mid");
    expect(row("Bucket")).toHaveClass("tree-last");
    expect(row("Bucket")?.closest("ul")).toHaveClass("tree-branch");
    expect(row("Bucket")?.closest("ul")?.parentElement?.closest("ul")).toHaveClass("tree-branch");
    expect(branchClass(0, 1)).toBe("tree-last");
  });

  it("expands a group when its row is clicked, and only toggles from the chevron", () => {
    render(<Harness />);
    const kids = () => row("flux")?.querySelector("[data-open]");
    expect(kids()).toHaveAttribute("data-open", "false");
    fireEvent.click(screen.getByText("Flux"));
    expect(kids()).toHaveAttribute("data-open", "true");
    // Clicking the row again does not collapse it; the chevron does.
    fireEvent.click(screen.getByText("Flux"));
    expect(kids()).toHaveAttribute("data-open", "true");
    fireEvent.click(screen.getByRole("button", { name: "Collapse Flux" }));
    expect(kids()).toHaveAttribute("data-open", "false");
  });

  it("uses → and ← to expand, enter, collapse and go back up", () => {
    render(<Harness />);
    const flux = screen.getByText("Flux");
    flux.focus();
    fireEvent.keyDown(flux, { key: "ArrowRight" });
    expect(screen.getByRole("button", { name: "Collapse Flux" })).toBeInTheDocument();
    fireEvent.keyDown(flux, { key: "ArrowRight" });
    expect(document.activeElement).toBe(screen.getByText("Kustomization"));
    fireEvent.keyDown(document.activeElement as Element, { key: "ArrowLeft" });
    expect(document.activeElement).toBe(flux);
    fireEvent.keyDown(flux, { key: "ArrowLeft" });
    expect(screen.getByRole("button", { name: "Expand Flux" })).toBeInTheDocument();
  });
});
