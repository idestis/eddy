import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ListRow } from "../lib/resourceRows";
import { resource } from "../test/fixtures";
import { ResourceList, type SortControl } from "./ResourceList";

vi.stubGlobal(
  "ResizeObserver",
  class {
    observe() {}
    unobserve() {}
    disconnect() {}
  },
);

const rows: ListRow[] = [
  { type: "resource", key: "a", resource: resource("a", { replicas: "1/2", message: "m" }) },
];

describe("ResourceList headers", () => {
  it("renders sortable headers with aria-sort and calls back", () => {
    const onSort = vi.fn();
    const sorting: SortControl = {
      sort: { key: "name", order: "desc" },
      onSort,
      disabled: (k) => k === "ready",
    };
    render(
      <ResourceList
        rows={rows}
        grouped={false}
        selectedId={undefined}
        onSelect={() => {}}
        onOpen={() => {}}
        label="x"
        sorting={sorting}
      />,
    );
    expect(screen.getByRole("columnheader", { name: /Name/ }).getAttribute("aria-sort")).toBe("descending");
    expect(screen.getByRole("columnheader", { name: /Status/ }).getAttribute("aria-sort")).toBe("none");
    fireEvent.click(screen.getByRole("button", { name: /Status/ }));
    expect(onSort).toHaveBeenCalledWith("status");
    const message = screen.getByRole("button", { name: /Replicas/ }) as HTMLButtonElement;
    expect(message.disabled).toBe(true);
    expect(message.title).toBe("Available on smaller clusters");
  });

  it("shows plain labels when grouped", () => {
    render(
      <ResourceList
        rows={rows}
        grouped
        selectedId={undefined}
        onSelect={() => {}}
        onOpen={() => {}}
        label="x"
        sorting={{ sort: undefined, onSort: () => {} }}
      />,
    );
    expect(screen.queryByRole("button", { name: /Status/ })).toBeNull();
  });
});
