import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import { ClusterForm } from "./AddClusterDialog";

const setup = (error: Error | null = null) => {
  const onSubmit = vi.fn();
  render(<ClusterForm error={error} onSubmit={onSubmit} onCancel={() => {}} />);
  return { onSubmit, name: screen.getByRole("textbox", { name: /^Name/ }) };
};

describe("ClusterForm", () => {
  it("validates the name live as a lower-case DNS label", async () => {
    const { name, onSubmit } = setup();
    await userEvent.type(name, "Prod");
    expect(screen.getByText("Use lower-case letters only.")).toBeInTheDocument();
    expect(name).toHaveAttribute("aria-invalid", "true");
    await userEvent.clear(name);
    await userEvent.type(name, "prod_us");
    expect(screen.getByText(/hyphens only/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Add and get install command/ })).toBeDisabled();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("submits the fields, the chosen colour and a 1h join token", async () => {
    const { name, onSubmit } = setup();
    await userEvent.type(name, "prod-us");
    await userEvent.type(screen.getByRole("textbox", { name: "Display name" }), "Prod US");
    await userEvent.click(screen.getByRole("radio", { name: "Production" }));
    const teal = screen.getByRole("button", { name: "Teal" });
    expect(teal).toHaveAttribute("aria-pressed", "false");
    await userEvent.click(teal);
    expect(teal).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: /^Auto/ })).toHaveAttribute("aria-pressed", "false");
    await userEvent.click(screen.getByRole("switch"));
    await userEvent.click(screen.getByRole("button", { name: /Add and get install command/ }));
    expect(onSubmit).toHaveBeenCalledWith({
      name: "prod-us",
      displayName: "Prod US",
      environment: "Production",
      region: undefined,
      color: "#0f766e",
      protected: true,
      ttl: "1h",
    });
  });

  it("does not submit an empty name", async () => {
    const { onSubmit } = setup();
    await userEvent.click(screen.getByRole("button", { name: /Add and get install command/ }));
    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText("A name is required.")).toBeInTheDocument();
  });

  it("shows a name conflict under the name and other hub errors as an alert", () => {
    setup(new ApiError(409, "conflict", "Cluster prod-us already exists."));
    expect(screen.getByText("Cluster prod-us already exists.")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows other hub errors inline", () => {
    setup(new ApiError(403, "forbidden", "RBAC says no."));
    expect(screen.getByRole("alert")).toHaveTextContent("You may not add clusters here. RBAC says no.");
  });
});
