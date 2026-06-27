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

import { Navigation } from "@/components/Navigation";
import { LicenseProvider } from "@/extensions/LicenseProvider";
import { builtinNavLinks } from "@/extensions/builtinManifests";
import { seedDefaultRegistryWithBuiltins } from "@/extensions/defaultRegistrySetup";

// The default registry is process-wide; seed the builtins so the
// hook inside <Navigation /> has something to return.
seedDefaultRegistryWithBuiltins();

describe("Navigation", () => {
  it("renders the eight expected nav links via the registry", () => {
    render(
      <LicenseProvider>
        <Navigation />
      </LicenseProvider>,
    );

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

  it("registry exposes 8 builtin nav links, each at an absolute path", () => {
    expect(builtinNavLinks).toHaveLength(8);
    for (const link of builtinNavLinks) {
      expect(link.href).toMatch(/^\//);
      expect(link.kind).toBe("nav-link");
    }
  });

  it("DLQ nav link points at /dlq", () => {
    const dlq = builtinNavLinks.find((l) => l.displayName === "DLQ");
    expect(dlq).toBeDefined();
    expect(dlq?.href).toBe("/dlq");
  });
});
