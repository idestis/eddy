import { render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import type { Finding } from "../api/types";
import { FindingCallout } from "./Findings";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, search }: { children: ReactNode; search: Record<string, string> }) => (
    <a href={`/?${new URLSearchParams(search)}`}>{children}</a>
  ),
}));

const finding: Finding = {
  id: "job-buildup/prefect",
  kind: "job-buildup",
  severity: "warning",
  namespace: "prefect",
  message: "15,168 finished Jobs in prefect (15,153 hidden)",
  recommendation: "set ttlSecondsAfterFinished on the Job template",
  jobs: {
    hidden: 15153,
    finished: 15168,
    succeeded: 15083,
    failed: 85,
    withoutTTL: 15135,
    standaloneFailed: 85,
    oldest: new Date(Date.now() - 41 * 86_400_000).toISOString(),
    groups: [
      { by: "label", name: "prefect.io/deployment-name=etl", label: "etl-hourly", count: 12000 },
      { by: "label", name: "prefect.io/deployment-name=crm", label: "sync-crm", count: 3168, failed: 85 },
    ],
    threshold: 200,
  },
};

describe("FindingCallout", () => {
  it("shows the message, the recommendation, key counts and top groups", () => {
    render(<FindingCallout cluster="prod-eu" finding={finding} link />);
    const box = screen.getByRole("region", { name: "Finding in prefect" });
    expect(within(box).getByText(finding.message)).toBeInTheDocument();
    expect(within(box).getByText(/ttlSecondsAfterFinished on the Job template/)).toBeInTheDocument();
    expect(within(box).getByText("15,153")).toBeInTheDocument();
    expect(within(box).getByText("15,135")).toBeInTheDocument();
    expect(within(box).getByText("41d ago")).toBeInTheDocument();
    const groups = within(box).getByRole("list", { name: "Largest groups" });
    expect(
      within(groups)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual(["etl-hourly: 12,000", "sync-crm: 3,168"]);
    expect(within(box).getByRole("link", { name: "Jobs in prefect" })).toHaveAttribute(
      "href",
      "/?kind=Job&namespace=prefect",
    );
  });

  it("omits the link when already on that namespace", () => {
    render(<FindingCallout cluster="prod-eu" finding={finding} />);
    expect(screen.queryByRole("link")).toBeNull();
  });
});
