import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { ApiError, NetworkError } from "../api/client";
import type { Flow, Span } from "../api/types";
import { droppedFirst, durationText, errorPercent, flowEndText, notAvailableReason, perSecond, trafficLevel, unavailableFor, waterfall } from "../lib/networking";

// Spec 025's console (T011): the Observability tab's requests, connections
// and traces, the topology's live traffic overlay, and the Platform page's
// network layer, each saying the server's reason when it isn't available.

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = (status: number, detail: string) => json(status, { title: status === 409 ? "not available" : "service unavailable", detail, status });

type Perms = Array<{ actions: string[]; notActions?: string[] }>;
const owner: Perms = [{ actions: ["*"] }];
/** Reader's actions: read everything and metrics, but not logs or traces (core/policy). */
const reader: Perms = [{ actions: ["*/read"], notActions: ["serendip/components/logs/read", "serendip/components/traces/read"] }];

let calls: string[];

/** routes answer GETs by exact URL; prefix answers by the URL's path, for the queries with times in them. */
function mockFetch(routes: Record<string, unknown>, prefix: Record<string, (url: string) => Response> = {}, perms: Perms = owner) {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      calls.push(url);
      if (url.startsWith("/v1/authorization/permissions?scope=")) return json(200, { scope: "", permissions: perms });
      const path = url.split("?")[0]!;
      if (path in prefix) return prefix[path]!(url);
      if (url in routes) {
        const v = routes[url];
        return v instanceof Response ? v : json(200, v);
      }
      return json(200, []);
    }),
  );
}

const goTo = (path: string) => window.history.replaceState(null, "", path);

beforeEach(() => {
  goTo("/");
  vi.stubGlobal("scrollTo", () => {});
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const at = "2026-11-23T10:00:00Z";
const meta = (id: string, name: string, kind: string, ownerRefs?: Array<{ kind: string; id: string }>) => ({ id, name, kind, createdAt: at, updatedAt: at, resourceVersion: 1, ...(ownerRefs ? { ownerRefs } : {}) });
const ready = [{ type: "Ready", status: "True", reason: "Deployed", lastTransitionTime: at }];
const project = { metadata: meta("p1", "shop", "Project"), spec: { pipeline: { paths: [] } }, status: {} };
const envs = [{ metadata: meta("e1", "development", "Environment", [{ kind: "Project", id: "p1" }]), spec: {}, status: {} }];
const app = { metadata: meta("app1", "storefront", "Application", [{ kind: "Project", id: "p1" }]), spec: {}, status: { conditions: ready } };
const web = { metadata: meta("c1", "web", "Component", [{ kind: "Application", id: "app1" }]), spec: { image: "nginx:1.27" }, status: { conditions: ready } };
const api = { metadata: meta("c2", "api", "Component", [{ kind: "Application", id: "app1" }]), spec: { image: "api:1" }, status: { conditions: ready } };
const dep = { metadata: meta("d1", "web", "Deployment", [{ kind: "Environment", id: "e1" }]), spec: { component: "c1" }, status: { conditions: ready } };

const base = {
  "/v1/session": { actor: "person:pat", method: "session" },
  "/v1/readiness": { observability: { ready: true, reason: "Ready", since: at }, tracing: { ready: true, reason: "Ready", since: at } },
  "/v1/packages": {
    packages: [
      { provider: "openchoreo-observability-plane", capability: "observability", available: true },
      { provider: "openchoreo-tracing", capability: "tracing", available: true },
    ],
  },
  "/v1/applications/app1": app,
  "/v1/components/c1": web,
  "/v1/components": [web, api],
  "/v1/components?ownerId=app1": [web, api],
  "/v1/projects/p1": project,
  "/v1/environments": envs,
  "/v1/environments?ownerId=p1": envs,
  "/v1/deployments": [dep],
};

const minutes = (n: number, v: number) => Array.from({ length: n }, (_, i) => ({ time: new Date(Date.parse(at) + i * 60_000).toISOString(), value: v }));
const metrics = (extra: object) => ({ cpu: minutes(5, 100), memory: minutes(5, 200), from: at, to: "2026-11-23T10:05:00Z", summary: {}, ...extra });
const requests = { rate: minutes(5, 5), errorRate: minutes(5, 0.5), latencyP50: minutes(5, 12), latencyP95: minutes(5, 45), latencyP99: minutes(5, 120) };

const flow = (verdict: string, extra: Partial<Flow> = {}): Flow => ({
  time: at,
  verdict,
  source: { component: "c2", environment: "e1" },
  destination: { component: "c1", environment: "e1" },
  protocol: "TCP",
  port: 80,
  count: 3,
  ...extra,
});

const tab = "/applications/app1/components/c1?tab=observability";

describe("requests on the observability tab", () => {
  it("charts the request rate, failures and a chosen latency percentile", async () => {
    goTo(tab);
    mockFetch(base, { "/v1/deployments/d1/metrics": () => json(200, metrics({ requests })) });
    render(<App />);
    expect(await screen.findByText("Requests per second")).toBeTruthy();
    expect(screen.getByText("Server errors (5xx) per second")).toBeTruthy();
    expect(screen.getByText("Latency p95, milliseconds")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Latency"), { target: { value: "latencyP99" } });
    expect(screen.getByText("Latency p99, milliseconds")).toBeTruthy();
  });

  it("says why requests can't be read, in the server's words", async () => {
    goTo(tab);
    mockFetch(base, { "/v1/deployments/d1/metrics": () => json(200, metrics({ unavailable: ["requests: the cluster's network layer doesn't provide request metrics"] })) });
    render(<App />);
    const block = (await screen.findByText("HTTP requests")).closest("section")!;
    expect(within(block).getByText(/the cluster's network layer doesn't provide request metrics/)).toBeTruthy();
    // Said once, not again in the general list.
    expect(within(block).getAllByText(/doesn't provide request metrics/).length).toBe(1);
  });
});

describe("connections on the observability tab", () => {
  it("lists refused connections first with the reason, and never names a hidden end", async () => {
    goTo(tab);
    const page = {
      flows: [flow("forwarded"), flow("dropped", { reason: "refused by a network policy", source: { outside: true } }), flow("forwarded", { destination: { namespace: "openchoreo-observability-plane", workload: "opentelemetry-collector" }, source: { component: "c1", environment: "e1" } })],
      from: at,
      to: at,
    };
    mockFetch(base, { "/v1/deployments/d1/flows": () => json(200, page) });
    render(<App />);
    const section = (await screen.findByText("Connections in development")).closest("section")!;
    const rows = await within(section).findAllByRole("row");
    expect(within(rows[1]!).getByText("Refused")).toBeTruthy();
    expect(within(rows[1]!).getByText(/refused by a network policy/)).toBeTruthy();
    expect(within(rows[1]!).getByText("Not shown")).toBeTruthy();
    expect(within(rows[2]!).getByText("api")).toBeTruthy();
    expect(within(section).getByText("openchoreo-observability-plane/opentelemetry-collector")).toBeTruthy();
    const sent = calls.find((u) => u.startsWith("/v1/deployments/d1/flows?"))!;
    expect(sent).toContain("verdict=all");
    fireEvent.change(within(section).getByLabelText("Show"), { target: { value: "dropped" } });
    await waitFor(() => expect(calls.some((u) => u.startsWith("/v1/deployments/d1/flows?") && u.includes("verdict=dropped"))).toBe(true));
  });

  it("says connections aren't available on a cluster whose network layer doesn't record them (criterion 2)", async () => {
    goTo(tab);
    mockFetch(base, { "/v1/deployments/d1/flows": () => problem(409, "connection records aren't available: the cluster's network layer doesn't record connections") });
    render(<App />);
    const section = (await screen.findByText("Connections in development")).closest("section")!;
    expect(await within(section).findByText(/the cluster's network layer doesn't record connections/)).toBeTruthy();
    expect(within(section).getByText("Not available")).toBeTruthy();
    expect(within(section).queryByRole("button", { name: "Refresh" })).toBeNull();
  });
});

const spans: Span[] = [
  { spanId: "a", name: "GET /", start: at, durationNs: 50_000_000, failed: false, component: "c1", environment: "e1", attributes: { "http.route": "/" } },
  { spanId: "b", parentId: "a", name: "SELECT", start: "2026-11-23T10:00:00.010Z", durationNs: 20_000_000, failed: true, status: "error", component: "c2", environment: "e1" },
];

describe("traces on the observability tab", () => {
  it("greys traces out until the tracing package is ready, without reading them", async () => {
    goTo(tab);
    mockFetch({ ...base, "/v1/readiness": { observability: { ready: true, reason: "Ready", since: at }, tracing: { ready: false, reason: "NotInstalled", since: at } } });
    render(<App />);
    const gate = (await screen.findByText("Traces")).closest("section")!;
    expect(within(gate).getByText(/openchoreo-tracing package, which isn't installed/)).toBeTruthy();
    expect(calls.some((u) => u.includes("/traces"))).toBe(false);
  });

  it("lists traces and opens one as a waterfall with a span's attributes", async () => {
    goTo(tab);
    mockFetch(base, {
      "/v1/deployments/d1/traces": () => json(200, { traces: [{ traceId: "t1", start: at, durationNs: 50_000_000, root: "GET /", spans: 2, hasErrors: true }], from: at, to: at }),
      "/v1/deployments/d1/traces/t1": () => json(200, { traceId: "t1", spans, partial: true }),
    });
    render(<App />);
    const section = (await screen.findByText("Traces in development")).closest("section")!;
    fireEvent.click(await within(section).findByRole("button", { name: "Open" }));
    expect(await within(section).findByText("Trace t1")).toBeTruthy();
    expect(within(section).getByText(/Some spans are from components your roles don't let you read/)).toBeTruthy();
    fireEvent.click(within(section).getByRole("button", { name: "GET /" }));
    expect(within(section).getByText("http.route")).toBeTruthy();
    expect(within(section).getByRole("button", { name: "SELECT" }).className).toContain("waterfall-depth-1");
    fireEvent.change(within(section).getByLabelText("Duration"), { target: { value: "500ms" } });
    await waitFor(() => expect(calls.some((u) => u.startsWith("/v1/deployments/d1/traces?") && u.includes("minDuration=500ms"))).toBe(true));
  });

  it("says reading traces needs its own permission, and doesn't read them", async () => {
    goTo(tab);
    mockFetch(base, {}, reader);
    render(<App />);
    expect(await screen.findByText(/reading them needs its own permission \(Log Reader, Contributor or Owner\)/)).toBeTruthy();
    expect(calls.some((u) => u.includes("/traces"))).toBe(false);
  });
});

describe("the topology's live traffic overlay", () => {
  const topo = { components: [{ id: "c1", name: "web", ports: [] }, { id: "c2", name: "api", ports: [] }], dependencies: [{ from: "c1", to: "c2", port: "80", env: "API_URL" }] };

  it("reads nothing until turned on, then labels each observed call", async () => {
    goTo("/applications/app1/topology");
    mockFetch({ ...base, "/v1/applications/app1/topology": topo }, { "/v1/applications/app1/traffic": () => json(200, { edges: [{ from: "c1", to: "c2", rate: 5, errorRate: 0.5 }], from: at, to: at, omitted: 1 }) });
    render(<App />);
    const toggle = await screen.findByLabelText("Show live traffic on the diagram");
    expect(calls.some((u) => u.includes("/traffic"))).toBe(false);
    fireEvent.click(toggle);
    expect(await screen.findByText("5/s, 10% failed")).toBeTruthy();
    expect(screen.getByText("Failing")).toBeTruthy();
    expect(screen.getByText(/1 component isn't shown/)).toBeTruthy();
    expect(calls.find((u) => u.includes("/traffic"))).toContain("environment=e1");
  });

  it("says the overlay isn't available on a cluster without request metrics (criterion 2)", async () => {
    goTo("/applications/app1/topology");
    mockFetch({ ...base, "/v1/applications/app1/topology": topo }, { "/v1/applications/app1/traffic": () => problem(409, "request metrics aren't available: the cluster's network layer doesn't provide request metrics") });
    render(<App />);
    fireEvent.click(await screen.findByLabelText("Show live traffic on the diagram"));
    expect(await screen.findByText(/the cluster's network layer doesn't provide request metrics/)).toBeTruthy();
  });
});

describe("the platform's network layer", () => {
  it("names the plugin, whether policies hold, and what isn't available (criteria 1 and 2)", async () => {
    goTo("/platform");
    mockFetch({
      ...base,
      "/v1/platform": { runtimes: [], componentTypes: [], traits: [] },
      "/v1/projects": [],
      "/v1/networking": {
        plugin: "kindnet",
        policyEnforcement: "unknown",
        policyReason: "kindnet's own policy controller isn't checked",
        flows: false,
        requestMetrics: false,
        unavailable: ["flows: the cluster's network layer doesn't record connections", "requests: the cluster's network layer doesn't provide request metrics"],
        readAt: at,
      },
    });
    render(<App />);
    const section = (await screen.findByText("Network layer")).closest("section")!;
    expect(await within(section).findByText("kindnet")).toBeTruthy();
    expect(within(section).getByText("Unknown")).toBeTruthy();
    expect(within(section).getByText(/doesn't record connections/)).toBeTruthy();
    expect(within(section).getByText(/doesn't provide request metrics/)).toBeTruthy();
    expect(within(section).getByText("Not reported")).toBeTruthy();
  });
});

describe("networking words", () => {
  it("names a flow's ends without ever naming a hidden one", () => {
    const names = new Map([["c1", "web"]]);
    const envNames = new Map([["e2", "staging"]]);
    expect(flowEndText({ component: "c1", environment: "e1" }, names, envNames, "e1")).toBe("web");
    expect(flowEndText({ component: "c1", environment: "e2" }, names, envNames, "e1")).toBe("web in staging");
    expect(flowEndText({ outside: true, component: "c1" }, names)).toBe("Not shown");
    expect(flowEndText({ external: true }, names)).toBe("Outside the cluster");
    expect(flowEndText({ namespace: "kube-system", workload: "coredns" }, names)).toBe("kube-system/coredns");
  });

  it("puts refused connections first and keeps each group's order", () => {
    const a = flow("forwarded", { time: "3" });
    const b = flow("dropped", { time: "2" });
    const c = flow("forwarded", { time: "1" });
    expect(droppedFirst([a, b, c])).toEqual([b, a, c]);
  });

  it("words rates, failures and durations", () => {
    expect(perSecond(0)).toBe("0/s");
    expect(perSecond(0.05)).toBe("0.05/s");
    expect(perSecond(5.36)).toBe("5.4/s");
    expect(perSecond(120.4)).toBe("120/s");
    expect(errorPercent({ rate: 0, errorRate: 0 })).toBeUndefined();
    expect(trafficLevel({ from: "a", to: "b", rate: 10, errorRate: 0 })).toEqual({ kind: "ok", label: "10/s, 0% failed" });
    expect(trafficLevel({ from: "a", to: "b", rate: 100, errorRate: 1 }).kind).toBe("warn");
    expect(trafficLevel({ from: "a", to: "b", rate: 5.36, errorRate: 0.53 })).toEqual({ kind: "error", label: "5.4/s, 9.9% failed" });
    expect(durationText(0)).toBe("0 ms");
    expect(durationText(850_000)).toBe("850 µs");
    expect(durationText(12_300_000)).toBe("12 ms");
    expect(durationText(1_400_000_000)).toBe("1.4 s");
  });

  it("takes the server's reason from a 409 or 503 only", () => {
    expect(notAvailableReason(new ApiError(409, "not available", "flows aren't available: x"))).toBe("flows aren't available: x");
    expect(notAvailableReason(new ApiError(503, "service unavailable", "traces need the tracing package"))).toBe("traces need the tracing package");
    expect(notAvailableReason(new ApiError(500, "internal"))).toBeUndefined();
    expect(notAvailableReason(new NetworkError("down"))).toBeUndefined();
    expect(unavailableFor({ plugin: "x", policyEnforcement: "unknown", flows: false, requestMetrics: true, unavailable: ["flows: no hubble"], readAt: at }, "flows")).toBe("no hubble");
  });

  it("lays out a waterfall under each span's parent, by start time", () => {
    const { rows, totalNs } = waterfall([spans[1]!, spans[0]!]);
    expect(totalNs).toBe(50_000_000);
    expect(rows.map((r) => [r.span.spanId, r.depth])).toEqual([
      ["a", 0],
      ["b", 1],
    ]);
    expect(rows[1]!.offset).toBeCloseTo(20);
    expect(rows[1]!.width).toBeCloseTo(40);
  });

  it("keeps a span whose parent isn't shown, and survives a cycle", () => {
    const orphan = { ...spans[1]!, parentId: "gone" };
    expect(waterfall([orphan]).rows.map((r) => r.depth)).toEqual([0]);
    const x = { ...spans[0]!, spanId: "x", parentId: "y" };
    const y = { ...spans[0]!, spanId: "y", parentId: "x" };
    expect(waterfall([x, y]).rows.length).toBe(2);
    expect(waterfall([]).rows).toEqual([]);
  });
});
