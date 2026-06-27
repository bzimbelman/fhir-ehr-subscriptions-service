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
  it("renders the six expected nav links", () => {
    render(<Navigation />);

    const expectedLabels = [
      "Dashboard",
      "Messages",
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
    expect(NAV_LINKS).toHaveLength(6);
    for (const link of NAV_LINKS) {
      expect(link.ticket).toMatch(/^#\d+$/);
      expect(link.href).toMatch(/^\//);
    }
  });
});
