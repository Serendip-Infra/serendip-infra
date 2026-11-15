import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { MetricChart, niceScale } from "../components/MetricChart";
import { diskWords } from "./Platform";
import { mebibytes, millicores, splitUnavailable } from "./Observability";

// Spec 023's console (T011): the Observability tab's metrics charts and
// log search, an Application's log search with its omitted notice, and
// the Packages page's disk lines.

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

type Perms = Array<{ actions: string[]; notActions?: string[] }>;
const owner: Perms = [{ actions: ["*"] }];
/** Reader's actions (core/policy): read everything and metrics, but not logs. */
const reader: Perms = [{ actions: ["*/read"], notActions: ["serendip/components/logs/read"] }];

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
      if (path in prefix && url !== path) return prefix[path]!(url);
      if (url in routes) return json(200, routes[url]);
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

const at = "2026-11-14T10:00:00Z";
const meta = (id: string, name: string, kind: string, ownerRefs?: Array<{ kind: string; id: string }>) => ({ id, name, kind, createdAt: at, updatedAt: at, resourceVersion: 1, ...(ownerRefs ? { ownerRefs } : {}) });
const ready = [{ type: "Ready", status: "True", reason: "Deployed", lastTransitionTime: at }];
const project = { metadata: meta("p1", "shop", "Project"), spec: { pipeline: { paths: [{ from: "e1", to: ["e2"] }] } }, status: {} };
const envs = [
  { metadata: meta("e1", "development", "Environment", [{ kind: "Project", id: "p1" }]), spec: {}, status: {} },
  { metadata: meta("e2", "staging", "Environment", [{ kind: "Project", id: "p1" }]), spec: {}, status: {} },
];
const app = { metadata: meta("app1", "storefront", "Application", [{ kind: "Project", id: "p1" }]), spec: {}, status: { conditions: ready } };
const web = { metadata: meta("c1", "web", "Component", [{ kind: "Application", id: "app1" }]), spec: { image: "nginx:1.27" }, status: { conditions: ready } };
const api = { metadata: meta("c2", "api", "Component", [{ kind: "Application", id: "app1" }]), spec: { image: "api:1" }, status: { conditions: ready } };
const dep = { metadata: meta("d1", "web", "Deployment", [{ kind: "Environment", id: "e1" }]), spec: { component: "c1" }, status: { conditions: ready } };

const observabilityReady = { observability: { ready: true, reason: "Ready", since: at } };
const base = {
  "/v1/session": { actor: "person:pat", method: "session" },
  "/v1/readiness": observabilityReady,
  "/v1/packages": { packages: [{ provider: "openchoreo-observability-plane", capability: "observability", description: "Stored logs and metrics", available: true }] },
  "/v1/applications/app1": app,
  "/v1/components/c1": web,
  "/v1/components?ownerId=app1": [web, api],
  "/v1/projects/p1": project,
  "/v1/environments?ownerId=p1": envs,
  "/v1/deployments": [dep],
};

const minutes = (n: number) => Array.from({ length: n }, (_, i) => ({ time: new Date(Date.parse(at) + i * 60_000).toISOString(), value: 100 + i }));
const metrics = {
  cpu: minutes(30),
  memory: minutes(30).map((p) => ({ ...p, value: 200 })),
  cpuRequest: 100,
  cpuLimit: 500,
  memoryRequest: 256,
  memoryLimit: 256,
  unavailable: ["requests, errors and latency: needs request metrics, not installed"],
  from: at,
  to: "2026-11-14T10:30:00Z",
  summary: {},
};
const line = (n: number, component = "c1") => ({ time: `2026-11-14T10:0${n}:00Z`, component, instance: "web-7d9-abc", line: `line ${n}` });

describe("observability tab", () => {
  it("greys out stored logs and metrics until the package is ready", async () => {
    goTo("/applications/app1/components/c1?tab=observability");
    mockFetch({ ...base, "/v1/readiness": { observability: { ready: false, reason: "NotInstalled", since: at } } });
    render(<App />);
    expect(await screen.findByText("Not installed")).toBeTruthy();
    expect(screen.getByText(/which isn't installed/).textContent).toContain("openchoreo-observability-plane");
    expect(calls.some((u) => u.includes("/metrics") || u.includes("/logs/search"))).toBe(false);
  });

  it("charts CPU and memory against request and limit, and says what isn't available", async () => {
    goTo("/applications/app1/components/c1?tab=observability");
    mockFetch(base, { "/v1/deployments/d1/metrics": () => json(200, metrics), "/v1/deployments/d1/logs/search": () => json(200, { lines: [] }) });
    render(<App />);
    const cpu = (await screen.findByText("CPU, millicores")).closest("figure")!;
    expect(within(cpu).getByText("Limit")).toBeTruthy();
    expect(within(cpu).getByText("Request")).toBeTruthy();
    expect(within(cpu).getByText("Latest 129m, highest 129m, request 100m, limit 500m.")).toBeTruthy();
    // Equal request and limit get one label, not two on top of each other.
    const mem = screen.getByText("Memory, MiB").closest("figure")!;
    expect(within(mem).getByText("Request and limit")).toBeTruthy();
    expect(within(mem).getByText("Latest 200 MiB, highest 200 MiB, request 256 MiB, limit 256 MiB.")).toBeTruthy();
    expect(screen.getByText(/Requests, errors and latency: needs request metrics, not installed/)).toBeTruthy();
    // The range is sent; "to" is left to the server (now).
    // Read once, not again by a range set after the first render.
    expect(calls.filter((u) => u.startsWith("/v1/deployments/d1/metrics?")).length).toBe(1);
    const sent = calls.find((u) => u.startsWith("/v1/deployments/d1/metrics?"))!;
    expect(sent).toMatch(/^\/v1\/deployments\/d1\/metrics\?from=2\d{3}-/);
    const shown = screen.getAllByText(/^GET \/v1\/deployments\/d1\/metrics\?from=/);
    expect(shown.length).toBe(1);
  });

  it("asks for a new range when the preset changes", async () => {
    goTo("/applications/app1/components/c1?tab=observability");
    mockFetch(base, { "/v1/deployments/d1/metrics": () => json(200, metrics), "/v1/deployments/d1/logs/search": () => json(200, { lines: [] }) });
    render(<App />);
    await screen.findByText("CPU, millicores");
    const first = calls.filter((u) => u.includes("/metrics?")).length;
    fireEvent.change(screen.getByLabelText("Time range"), { target: { value: "24h" } });
    await waitFor(() => expect(calls.filter((u) => u.includes("/metrics?")).length).toBe(first + 1));
    const from = new URL(`http://x${calls.filter((u) => u.includes("/metrics?")).at(-1)!}`).searchParams.get("from")!;
    expect(Date.now() - Date.parse(from)).toBeGreaterThan(23 * 3600_000);
  });

  it("searches stored lines by text, newest first, and pages to older ones with the same range", async () => {
    goTo("/applications/app1/components/c1?tab=observability");
    mockFetch(base, {
      "/v1/deployments/d1/metrics": () => json(200, metrics),
      "/v1/deployments/d1/logs/search": (url) => {
        const q = new URL(`http://x${url}`).searchParams;
        if (q.get("before") === "c2") return json(200, { lines: [line(1)] });
        // An empty page with more behind it is read on, not shown as the end.
        if (q.get("before") === "c1") return json(200, { lines: [], next: "c2" });
        if (q.get("q") === "boom") return json(200, { lines: [line(3)], next: "c1" });
        return json(200, { lines: [line(5)] });
      },
    });
    render(<App />);
    expect(await screen.findByText("line 5")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Containing"), { target: { value: " boom " } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByText("line 3")).toBeTruthy();
    expect(screen.queryByText("line 5")).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Show older lines" }));
    expect(await screen.findByText("line 1")).toBeTruthy();
    expect(screen.getByText("line 3")).toBeTruthy();
    // No next: the search is over.
    await waitFor(() => expect(screen.queryByRole("button", { name: "Show older lines" })).toBeNull());
    const searches = calls.filter((u) => u.includes("/logs/search?")).map((u) => new URL(`http://x${u}`).searchParams);
    const boom = searches.filter((p) => p.get("q") === "boom");
    expect(boom.length).toBe(3);
    // Every page of one search has the same from and to.
    expect(new Set(boom.map((p) => `${p.get("from")}|${p.get("to")}`)).size).toBe(1);
    expect(boom.map((p) => p.get("before"))).toEqual([null, "c1", "c2"]);
  });

  it("shows a Reader metrics but not logs", async () => {
    goTo("/applications/app1/components/c1?tab=observability");
    mockFetch(base, { "/v1/deployments/d1/metrics": () => json(200, metrics) }, reader);
    render(<App />);
    expect(await screen.findByText("CPU, millicores")).toBeTruthy();
    expect(await screen.findByText(/needs its own permission \(the Log Reader role\)/)).toBeTruthy();
    expect(calls.some((u) => u.includes("/logs/search"))).toBe(false);
  });

  it("says traces and alerts aren't available yet, without mocking them", async () => {
    goTo("/applications/app1/components/c1?tab=observability");
    mockFetch(base, { "/v1/deployments/d1/metrics": () => json(200, metrics), "/v1/deployments/d1/logs/search": () => json(200, { lines: [] }) });
    render(<App />);
    const later = (await screen.findByRole("heading", { name: "Traces and alerts" })).closest("section")!;
    expect(within(later).getByText("Not available yet")).toBeTruthy();
  });
});

describe("application log search", () => {
  it("searches every component in the chosen Environment, naming each line's component, and says how many were left out", async () => {
    goTo("/applications/app1");
    mockFetch(base, {
      "/v1/applications/app1/logs/search": (url) =>
        new URL(`http://x${url}`).searchParams.get("environment") === "e2" ? json(200, { lines: [], omitted: 0 }) : json(200, { lines: [line(2, "c2"), line(1, "c1")], omitted: 2 }),
    });
    render(<App />);
    expect(await screen.findByText("line 2")).toBeTruthy();
    const log = screen.getByRole("log", { name: "Log search" });
    expect(log.textContent).toContain("api web-7d9-abc");
    expect(log.textContent).toContain("web web-7d9-abc");
    expect(screen.getByText(/2 components' logs aren't searched/)).toBeTruthy();
    expect(calls.find((u) => u.startsWith("/v1/applications/app1/logs/search?"))).toContain("environment=e1");
    fireEvent.change(screen.getByLabelText("Environment"), { target: { value: "e2" } });
    expect(await screen.findByText("No log lines in this range.")).toBeTruthy();
    expect(screen.queryByText(/aren't searched/)).toBeNull();
  });

  it("explains a 403 as having no component's logs to read", async () => {
    goTo("/applications/app1");
    mockFetch(base, { "/v1/applications/app1/logs/search": () => json(403, { title: "forbidden", detail: "missing serendip/components/logs/read" }) });
    render(<App />);
    expect(await screen.findByText(/Your roles don't allow reading the logs of any component in this Environment/)).toBeTruthy();
  });

  it("is greyed out, and sends nothing, while the package isn't ready", async () => {
    goTo("/applications/app1");
    mockFetch({ ...base, "/v1/readiness": { observability: { ready: false, reason: "Installing", since: at } } });
    render(<App />);
    expect(await screen.findByText(/is installing/)).toBeTruthy();
    expect(calls.some((u) => u.includes("/logs/search"))).toBe(false);
  });
});

describe("packages: disk", () => {
  const requirements = {
    idleMemoryMiB: 380,
    idleCPUMillicores: 60,
    peakMemoryMiB: 600,
    peakCPUMillicores: 400,
    measuredOn: "simulated T470",
    results: "profiles/edge-1/bench/results/x",
    disk: { growthMiBPerDay: 120, retentionDays: 3, budgetMiB: 8192 },
  };
  const now = new Date("2026-11-14T10:01:00Z");

  it("states the disk cost, and current use when installed", () => {
    const pkg = { provider: "p", capability: "observability", description: "", available: true, requirements, usage: { usedMiB: 412, capacityMiB: 8192, readAt: at } };
    expect(diskWords(pkg, now).stated).toBe("About 120 MiB a day, kept 3 days, at most 8,192 MiB.");
    expect(diskWords(pkg, now).inUse).toMatch(/^412 MiB of 8,192 MiB, read /);
    expect(diskWords({ ...pkg, usage: { usedMiB: 5, capacityMiB: 0, readAt: at } }, now).inUse).toMatch(/^5 MiB \(its volumes' size isn't known\)/);
    expect(diskWords({ provider: "p", capability: "observability", description: "", available: true, requirements, usageError: "the volume stats couldn't be read" }, now).inUse).toBe("Couldn't be read: the volume stats couldn't be read");
    expect(diskWords({ ...pkg, requirements: null }, now)).toEqual({ inUse: expect.stringMatching(/^412 MiB/) });
  });

  it("shows them on the Packages page", async () => {
    goTo("/packages");
    mockFetch({
      ...base,
      "/v1/packages": { packages: [{ provider: "openchoreo-observability-plane", capability: "observability", description: "Stored logs and metrics", available: true, installable: true, requirements, usage: { usedMiB: 412, capacityMiB: 8192, readAt: at } }] },
    });
    render(<App />);
    const section = (await screen.findByText("Stored logs and metrics")).closest("section")!;
    expect(within(section).getByText("About 120 MiB a day, kept 3 days, at most 8,192 MiB.")).toBeTruthy();
    expect(within(section).getByText(/^412 MiB of 8,192 MiB/)).toBeTruthy();
  });
});

describe("MetricChart", () => {
  it("picks clean axis maxima", () => {
    expect(niceScale(129)).toEqual({ max: 150, step: 50 });
    expect(niceScale(500)).toEqual({ max: 600, step: 200 });
    expect(niceScale(0)).toEqual({ max: 1, step: 0.25 });
    expect(niceScale(7.3)).toEqual({ max: 8, step: 2 });
  });

  it("says when there are no samples, drawing nothing", () => {
    render(<MetricChart id="t" title="CPU, millicores" points={[]} from={at} to="2026-11-14T11:00:00Z" format={millicores} />);
    expect(screen.getByText("No samples in this range.")).toBeTruthy();
    expect(document.querySelector("svg")).toBeNull();
  });

  it("reads samples with the arrow keys, and keeps them all in a table", () => {
    render(<MetricChart id="t" title="CPU, millicores" points={minutes(3)} from={at} to="2026-11-14T10:05:00Z" format={millicores} />);
    const chart = screen.getByRole("img");
    expect(chart.getAttribute("aria-label")).toMatch(/3 samples .*Latest 102m, highest 102m/);
    act(() => chart.focus());
    fireEvent.keyDown(chart, { key: "End" });
    fireEvent.keyDown(chart, { key: "ArrowLeft" });
    expect(document.querySelector(".chart-tip-value")!.textContent).toBe("101m");
    fireEvent.keyDown(chart, { key: "Home" });
    expect(document.querySelector(".chart-tip-value")!.textContent).toBe("100m");
    fireEvent.keyDown(chart, { key: "Escape" });
    expect(document.querySelector(".chart-tip-value")).toBeNull();
    const rows = within(screen.getByRole("table")).getAllByRole("row");
    expect(rows.length).toBe(4);
  });

  it("breaks the line over a gap in the samples", () => {
    const pts = [...minutes(3), { time: "2026-11-14T10:20:00Z", value: 1 }, { time: "2026-11-14T10:21:00Z", value: 2 }];
    render(<MetricChart id="t" title="CPU, millicores" points={pts} from={at} to="2026-11-14T10:30:00Z" format={millicores} />);
    const d = document.querySelector(".chart-line")!.getAttribute("d")!;
    expect(d.match(/M/g)!.length).toBe(2);
  });
});

describe("formatting", () => {
  it("writes units the way the API states them", () => {
    expect(millicores(1234.4)).toBe("1,234m");
    expect(millicores(2.345)).toBe("2.3m");
    expect(mebibytes(256)).toBe("256 MiB");
    expect(splitUnavailable("cpuRequest: not set")).toEqual({ what: "CPU request", why: "not set" });
    expect(splitUnavailable("requests, errors and latency: needs request metrics, not installed").what).toBe("Requests, errors and latency");
  });
});
