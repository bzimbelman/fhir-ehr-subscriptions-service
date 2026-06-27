import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { MatchboxView } from "@/components/MatchboxView";
import { SAMPLE_ADT_A04, type ApiResult } from "@/lib/matchboxClient";
import type {
  MatchboxHealth,
  StructureMapsEnvelope,
  TransformRequest,
  TransformResponse,
} from "@/lib/matchboxTypes";

const REF_NOW = new Date("2026-06-26T12:00:00Z");

function healthyHealth(over: Partial<MatchboxHealth> = {}): MatchboxHealth {
  return {
    reachable: true,
    version: "v3.9.13",
    base_url: "http://matchbox:8080/matchboxv3/fhir",
    checked_at: "2026-06-26T11:59:55Z",
    response_time_ms: 42,
    error: null,
    ...over,
  };
}

function unreachableHealth(over: Partial<MatchboxHealth> = {}): MatchboxHealth {
  return {
    reachable: false,
    version: null,
    base_url: "http://matchbox:8080/matchboxv3/fhir",
    checked_at: "2026-06-26T11:59:55Z",
    response_time_ms: 3001,
    error: "connection refused",
    ...over,
  };
}

function ok<T>(data: T): () => Promise<ApiResult<T>> {
  return vi.fn(async () => ({ data, error: null }));
}

beforeEach(() => {
  Object.defineProperty(document, "visibilityState", {
    value: "visible",
    writable: true,
    configurable: true,
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("MatchboxView (ticket #405)", () => {
  it("fetches /api/admin/matchbox/health on mount", async () => {
    const fetchHealth = ok(healthyHealth());
    const fetchMaps = ok<StructureMapsEnvelope>({ total: 0, items: [], error: null });
    await act(async () => {
      render(
        <MatchboxView
          fetchHealth={fetchHealth}
          fetchMaps={fetchMaps}
          runTransform={vi.fn()}
          nowProvider={() => REF_NOW}
        />,
      );
    });
    expect(fetchHealth).toHaveBeenCalledTimes(1);
    expect(fetchMaps).toHaveBeenCalledTimes(1);
  });

  it("renders the healthy pill and version when Matchbox is reachable", async () => {
    const fetchHealth = ok(healthyHealth({ version: "v3.9.13" }));
    const fetchMaps = ok<StructureMapsEnvelope>({ total: 0, items: [], error: null });
    await act(async () => {
      render(
        <MatchboxView
          fetchHealth={fetchHealth}
          fetchMaps={fetchMaps}
          runTransform={vi.fn()}
          nowProvider={() => REF_NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(
        screen.getByTestId("matchbox-health-pill-healthy"),
      ).toBeInTheDocument();
    });
    expect(screen.getByTestId("matchbox-health-version").textContent).toBe(
      "v3.9.13",
    );
  });

  it("renders unreachable pill and StructureMaps empty-state message when Matchbox is down", async () => {
    const fetchHealth = ok(unreachableHealth({ error: "connection refused" }));
    const fetchMaps = ok<StructureMapsEnvelope>({
      total: 0,
      items: [],
      error: "connection refused",
    });
    await act(async () => {
      render(
        <MatchboxView
          fetchHealth={fetchHealth}
          fetchMaps={fetchMaps}
          runTransform={vi.fn()}
          nowProvider={() => REF_NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(
        screen.getByTestId("matchbox-health-pill-unreachable"),
      ).toBeInTheDocument();
    });
    const unreachable = screen.getByTestId("matchbox-structuremaps-unreachable");
    expect(unreachable).toBeInTheDocument();
    expect(unreachable.textContent).toContain("Matchbox unreachable");
    expect(unreachable.textContent).toContain("connection refused");
  });

  it("Refresh button re-fetches /health", async () => {
    let calls = 0;
    const fetchHealth = vi.fn(async (): Promise<ApiResult<MatchboxHealth>> => {
      calls += 1;
      return { data: healthyHealth({ version: `v${calls}` }), error: null };
    });
    const fetchMaps = ok<StructureMapsEnvelope>({ total: 0, items: [], error: null });
    await act(async () => {
      render(
        <MatchboxView
          fetchHealth={fetchHealth}
          fetchMaps={fetchMaps}
          runTransform={vi.fn()}
          nowProvider={() => REF_NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("matchbox-health-version").textContent).toBe("v1");
    });
    await act(async () => {
      fireEvent.click(screen.getByTestId("matchbox-health-refresh"));
    });
    await waitFor(() => {
      expect(screen.getByTestId("matchbox-health-version").textContent).toBe("v2");
    });
    expect(fetchHealth).toHaveBeenCalledTimes(2);
  });

  it("StructureMaps filter narrows the rendered rows", async () => {
    const fetchHealth = ok(healthyHealth());
    const fetchMaps = ok<StructureMapsEnvelope>({
      total: 3,
      items: [
        {
          id: "ADT-A01",
          url: "http://hl7.org/fhir/uv/v2mappings/StructureMap/ADT_A01",
          name: "ADT_A01",
          title: "Admission",
          status: "active",
          version: "1.0",
        },
        {
          id: "ADT-A04",
          url: "http://hl7.org/fhir/uv/v2mappings/StructureMap/ADT_A04",
          name: "ADT_A04",
          title: "Register",
          status: "active",
          version: "1.0",
        },
        {
          id: "ORU-R01",
          url: "http://hl7.org/fhir/uv/v2mappings/StructureMap/ORU_R01",
          name: "ORU_R01",
          title: "Observation Result",
          status: "draft",
          version: "0.9",
        },
      ],
      error: null,
    });
    await act(async () => {
      render(
        <MatchboxView
          fetchHealth={fetchHealth}
          fetchMaps={fetchMaps}
          runTransform={vi.fn()}
          nowProvider={() => REF_NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("matchbox-sm-row-ADT-A01")).toBeInTheDocument();
    });
    expect(screen.getByTestId("matchbox-sm-row-ORU-R01")).toBeInTheDocument();

    const filter = screen.getByTestId("matchbox-structuremaps-filter");
    await act(async () => {
      fireEvent.change(filter, { target: { value: "ORU" } });
    });
    await waitFor(() => {
      expect(screen.queryByTestId("matchbox-sm-row-ADT-A01")).toBeNull();
    });
    expect(screen.queryByTestId("matchbox-sm-row-ADT-A04")).toBeNull();
    expect(screen.getByTestId("matchbox-sm-row-ORU-R01")).toBeInTheDocument();
  });

  it("Transform button POSTs to /api/admin/matchbox/transform", async () => {
    const fetchHealth = ok(healthyHealth());
    const fetchMaps = ok<StructureMapsEnvelope>({ total: 0, items: [], error: null });
    const runTransform = vi.fn(
      async (body: TransformRequest): Promise<ApiResult<TransformResponse>> => {
        void body;
        return {
          data: {
            success: true,
            bundle: { resourceType: "Bundle", type: "transaction", entry: [] },
            error: null,
          },
          error: null,
        };
      },
    );
    await act(async () => {
      render(
        <MatchboxView
          fetchHealth={fetchHealth}
          fetchMaps={fetchMaps}
          runTransform={runTransform}
          nowProvider={() => REF_NOW}
        />,
      );
    });
    const textarea = screen.getByTestId("matchbox-transform-input") as HTMLTextAreaElement;
    await act(async () => {
      fireEvent.change(textarea, {
        target: { value: "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626120000||ADT^A04|405-T1|P|2.5" },
      });
    });
    await act(async () => {
      fireEvent.change(screen.getByTestId("matchbox-transform-map-url"), {
        target: { value: "http://example/StructureMap/ADT_A04" },
      });
    });
    await act(async () => {
      fireEvent.click(screen.getByTestId("matchbox-transform-run"));
    });
    await waitFor(() => {
      expect(runTransform).toHaveBeenCalledTimes(1);
    });
    const firstCall = runTransform.mock.calls[0];
    expect(firstCall).toBeDefined();
    const call = firstCall![0];
    expect(call.source_format).toBe("hl7v2");
    expect(call.map_url).toBe("http://example/StructureMap/ADT_A04");
    expect(call.raw_message).toContain("ADT^A04");
  });

  it("Transform success renders the pretty-printed Bundle", async () => {
    const fetchHealth = ok(healthyHealth());
    const fetchMaps = ok<StructureMapsEnvelope>({ total: 0, items: [], error: null });
    const runTransform = vi.fn(
      async (): Promise<ApiResult<TransformResponse>> => ({
        data: {
          success: true,
          bundle: { resourceType: "Bundle", type: "transaction", entry: [{ x: 1 }] },
          error: null,
        },
        error: null,
      }),
    );
    await act(async () => {
      render(
        <MatchboxView
          fetchHealth={fetchHealth}
          fetchMaps={fetchMaps}
          runTransform={runTransform}
          nowProvider={() => REF_NOW}
        />,
      );
    });
    const textarea = screen.getByTestId("matchbox-transform-input");
    await act(async () => {
      fireEvent.change(textarea, { target: { value: "MSH|^~\\&|X" } });
    });
    await act(async () => {
      fireEvent.click(screen.getByTestId("matchbox-transform-run"));
    });
    await waitFor(() => {
      expect(screen.getByTestId("matchbox-transform-output")).toBeInTheDocument();
    });
    const output = screen.getByTestId("matchbox-transform-output");
    // Pretty-printed JSON: each top-level key on its own line.
    expect(output.textContent).toContain('"resourceType": "Bundle"');
    expect(output.textContent).toContain('"entry"');
  });

  it("Transform failure renders the error message", async () => {
    const fetchHealth = ok(healthyHealth());
    const fetchMaps = ok<StructureMapsEnvelope>({ total: 0, items: [], error: null });
    const runTransform = vi.fn(
      async (): Promise<ApiResult<TransformResponse>> => ({
        data: {
          success: false,
          bundle: null,
          error: "matchbox: unknown source ADT_A99",
        },
        error: null,
      }),
    );
    await act(async () => {
      render(
        <MatchboxView
          fetchHealth={fetchHealth}
          fetchMaps={fetchMaps}
          runTransform={runTransform}
          nowProvider={() => REF_NOW}
        />,
      );
    });
    const textarea = screen.getByTestId("matchbox-transform-input");
    await act(async () => {
      fireEvent.change(textarea, { target: { value: "MSH|^~\\&|X" } });
    });
    await act(async () => {
      fireEvent.click(screen.getByTestId("matchbox-transform-run"));
    });
    await waitFor(() => {
      expect(screen.getByTestId("matchbox-transform-error")).toBeInTheDocument();
    });
    const err = screen.getByTestId("matchbox-transform-error");
    expect(err.textContent).toContain("matchbox: unknown source ADT_A99");
  });

  it("Try sample loads the obvious-synthetic ADT^A04 fixture into the textarea", async () => {
    const fetchHealth = ok(healthyHealth());
    const fetchMaps = ok<StructureMapsEnvelope>({ total: 0, items: [], error: null });
    await act(async () => {
      render(
        <MatchboxView
          fetchHealth={fetchHealth}
          fetchMaps={fetchMaps}
          runTransform={vi.fn()}
          nowProvider={() => REF_NOW}
        />,
      );
    });
    await act(async () => {
      fireEvent.click(screen.getByTestId("matchbox-transform-sample"));
    });
    const textarea = screen.getByTestId("matchbox-transform-input") as HTMLTextAreaElement;
    expect(textarea.value).toBe(SAMPLE_ADT_A04);
    // Sanity: the sample is the expected ADT^A04 synthetic message.
    expect(textarea.value).toContain("ADT^A04");
    expect(textarea.value).toContain("Test^Patient");
  });
});

// Suppress unused-import lint on `within` until it's actually used.
void within;
