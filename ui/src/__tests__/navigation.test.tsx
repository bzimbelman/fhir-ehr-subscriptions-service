import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";

// next/link renders <a> in tests; mock it so we don't drag the App Router in.
vi.mock("next/link", () => ({
  __esModule: true,
  default: ({
    href,
    children,
    ...rest
  }: {
    href: string;
    children: React.ReactNode;
  }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

import { Navigation, NAV_LINKS } from "@/components/Navigation";

describe("Navigation", () => {
  it("renders the eight expected nav links", () => {
    render(<Navigation />);

    const expectedLabels = [
      "Dashboard",
      "Interfaces",
      "Messages",
      "DLQ",
      "Subscriptions",
      "Matchbox",
      "Settings",
      "Audit",
    ];
    for (const label of expectedLabels) {
      expect(screen.getByRole("link", { name: label })).toBeInTheDocument();
    }
  });

  it("exposes ticket metadata for each placeholder route", () => {
    expect(NAV_LINKS).toHaveLength(8);
    for (const link of NAV_LINKS) {
      expect(link.ticket).toMatch(/^#\d+$/);
      expect(link.href).toMatch(/^\//);
    }
  });

  it("links DLQ to /dlq for ticket #403", () => {
    const dlq = NAV_LINKS.find((l) => l.label === "DLQ");
    expect(dlq).toBeDefined();
    expect(dlq?.href).toBe("/dlq");
    expect(dlq?.ticket).toBe("#403");
  });
});
