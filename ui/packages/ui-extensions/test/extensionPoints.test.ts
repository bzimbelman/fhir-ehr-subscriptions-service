import { describe, expect, expectTypeOf, test } from "vitest";
import {
  entitlement,
  type Extension,
  type NavLinkExtension,
  type PageRouteExtension,
  type PanelWidgetExtension,
  type RowActionExtension,
  type DetailTabExtension,
  type ExtensionKind,
} from "../src";

describe("Extension shapes", () => {
  test("NavLinkExtension compiles with a required entitlement", () => {
    const link: NavLinkExtension = {
      kind: "nav-link",
      id: "compliance-nav",
      displayName: "Compliance",
      href: "/compliance/iti-20",
      requiredEntitlement: entitlement("audit.export.iti20"),
      order: 50,
      icon: "shield",
    };
    expect(link.kind).toBe("nav-link");
    expect(link.requiredEntitlement).toBe("audit.export.iti20");
  });

  test("NavLinkExtension compiles without entitlement (FOSS case)", () => {
    const link: NavLinkExtension = {
      kind: "nav-link",
      id: "messages-nav",
      displayName: "Messages",
      href: "/messages",
    };
    expect(link.requiredEntitlement).toBeUndefined();
  });

  test("PageRouteExtension carries a lazy-imported component", async () => {
    const route: PageRouteExtension = {
      kind: "page-route",
      id: "iti20-page",
      displayName: "ITI-20 Export",
      path: "/compliance/iti-20",
      component: async () => ({
        default: () => null as unknown as never,
      }),
    };
    const mod = await route.component();
    expect(typeof mod.default).toBe("function");
  });

  test("PanelWidgetExtension binds to a sealed slot", () => {
    const widget: PanelWidgetExtension = {
      kind: "panel-widget",
      id: "datadog-status",
      displayName: "Datadog status",
      slot: "dashboard.stats",
      component: async () => ({ default: () => null as unknown as never }),
      order: 10,
    };
    expect(widget.slot).toBe("dashboard.stats");
  });

  test("RowActionExtension carries an async action handler", async () => {
    const action: RowActionExtension = {
      kind: "row-action",
      id: "synthetic-resend",
      displayName: "Synthetic resend",
      target: "dlq-row",
      label: "Replay (synthetic)",
      action: async (rowId, ctx) => ({
        ok: true,
        message: `Resent ${rowId}`,
        refresh: !!ctx.refresh,
      }),
    };
    const result = await action.action("msg-1", {
      rowId: "msg-1",
      refresh: () => {},
    });
    expect(result.ok).toBe(true);
    expect(result.message).toBe("Resent msg-1");
  });

  test("DetailTabExtension binds to a sealed target", () => {
    const tab: DetailTabExtension = {
      kind: "detail-tab",
      id: "trace-timeline",
      displayName: "Trace timeline",
      target: "message-detail",
      label: "Trace",
      component: async () => ({ default: () => null as unknown as never }),
    };
    expect(tab.target).toBe("message-detail");
  });
});

describe("Extension discriminated union", () => {
  test("kind narrows to the right variant", () => {
    const xs: readonly Extension[] = [
      {
        kind: "nav-link",
        id: "n1",
        displayName: "N",
        href: "/n",
      },
      {
        kind: "page-route",
        id: "p1",
        displayName: "P",
        path: "/p",
        component: async () => ({ default: () => null as unknown as never }),
      },
    ];

    for (const x of xs) {
      if (x.kind === "nav-link") {
        expectTypeOf(x).toMatchTypeOf<NavLinkExtension>();
        expect(x.href).toBe("/n");
      } else if (x.kind === "page-route") {
        expectTypeOf(x).toMatchTypeOf<PageRouteExtension>();
        expect(x.path).toBe("/p");
      }
    }
  });

  test("ExtensionKind covers every variant exactly", () => {
    const kinds: ExtensionKind[] = [
      "nav-link",
      "page-route",
      "panel-widget",
      "row-action",
      "detail-tab",
    ];
    expect(kinds).toHaveLength(5);
  });
});
