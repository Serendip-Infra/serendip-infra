import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { bellCounts, bellLabel, conditionText, formatValue, ruleStateWords, silenceUntil } from "../lib/alerts";
import type { AlertRule } from "../api/types";

// Spec 024 T009: the bell, the Component's Alerts section (acknowledge,
// silence, the rule form), the Alerts page, and Settings › Notifications
// (allowances for Owners only, Secrets shown only as whether they're set,
// and the Telegram Find chat step).

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

type Perms = Array<{ actions: string[]; notActions?: string[]; dataActions?: string[] }>;
const owner: Perms = [{ actions: ["*"], dataActions: ["serendip/secrets/value/read"] }];
/** Contributor (core/policy): everything but access management and the allowance. */
const contributor: Perms = [{ actions: ["*"], notActions: ["serendip/authorization/*", "serendip/notificationchannels/allowPrivateDestination/action"] }];

interface Call {
  method: string;
  url: string;
  body: unknown;
}
let calls: Call[];

/** routes answer by "METHOD url" first, then by url for GETs. */
function mockFetch(routes: Record<string, unknown>, perms: Perms = owner) {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      if (url.startsWith("/v1/authorization/permissions?scope=")) return json(200, { scope: "", permissions: perms });
      const keyed = routes[`${method} ${url}`];
      if (keyed instanceof Response) return keyed;
      if (keyed !== undefined) return json(200, keyed);
      if (method === "GET" && url in routes) {
        const v = routes[url];
        return v instanceof Response ? v : json(200, v);
      }
      return method === "GET" ? json(200, []) : json(200, {});
    }),
  );
}

/** A Guarded action, once the caller's permissions have loaded and enabled it. */
async function enabled(name: string, within_: HTMLElement = document.body): Promise<HTMLElement> {
  let b: HTMLElement | undefined;
  await waitFor(() => {
    b = within(within_).getAllByRole("button", { name }).find((x) => !(x as HTMLButtonElement).disabled);
    expect(b).toBeTruthy();
  });
  return b!;
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

const at = "2026-11-15T10:00:00Z";
const meta = (id: string, name: string, kind: string, ownerRefs?: Array<{ kind: string; id: string }>) => ({ id, name, kind, createdAt: at, updatedAt: at, resourceVersion: 1, ...(ownerRefs ? { ownerRefs } : {}) });
const ready = [{ type: "Ready", status: "True", reason: "Deployed", lastTransitionTime: at }];
const project = { metadata: meta("p1", "shop", "Project"), spec: {}, status: {} };
const env = { metadata: meta("e1", "development", "Environment", [{ kind: "Project", id: "p1" }]), spec: {}, status: {} };
const app = { metadata: meta("app1", "storefront", "Application", [{ kind: "Project", id: "p1" }]), spec: {}, status: { conditions: ready } };
const web = { metadata: meta("c1", "web", "Component", [{ kind: "Application", id: "app1" }]), spec: { image: "nginx:1.27" }, status: { conditions: ready } };
const dep = { metadata: meta("d1", "web", "Deployment", [{ kind: "Environment", id: "e1" }]), spec: { component: "c1" }, status: { conditions: ready } };

const rule = {
  metadata: meta("r1", "web-memory", "AlertRule", [{ kind: "Component", id: "c1" }]),
  spec: { environmentId: "e1", condition: { type: "metric", metric: { metric: "memory", as: "percentOfLimit", above: 90, forMinutes: 5 } }, severity: "critical", summary: "web is out of memory", intervalMinutes: 1 },
  status: { active: true, lastValue: 95, lastEvaluated: at },
};
const firing = {
  metadata: meta("a1", "web-memory", "Alert", [{ kind: "AlertRule", id: "r1" }]),
  spec: { ruleId: "r1", componentId: "c1", environmentId: "e1", severity: "critical", summary: "web is out of memory", fingerprint: "r1|c1|e1" },
  status: { state: "firing", startedAt: at, value: 95, threshold: 90, deliveries: [{ channelId: "ch1", event: "firing", state: "belowMinSeverity", at }] },
};

const base = {
  "/v1/session": { actor: "person:pat", method: "session" },
  "/v1/readiness": { observability: { ready: true, reason: "Ready", since: at } },
  "/v1/packages": { packages: [{ provider: "openchoreo-observability-plane", capability: "observability", available: true }] },
  "/v1/applications/app1": app,
  "/v1/components/c1": web,
  "/v1/projects/p1": project,
  "/v1/projects": [project],
  "/v1/environments?ownerId=p1": [env],
  "/v1/deployments": [dep],
};

const BOT_TOKEN = "123456:AAH-a-bot-token-the-console-never-sees";
const secret = { metadata: meta("s1", "ops-bot", "Secret"), spec: { path: "ops/bot" }, status: {} };
const channel = {
  metadata: meta("ch1", "ops-telegram", "NotificationChannel", [{ kind: "Project", id: "p1" }]),
  spec: { type: "telegram", telegram: { chatId: "-1001234", botTokenSecretId: "s1" }, minSeverity: "warning" },
  status: {},
  secretSet: { botTokenSecretId: true },
};

describe("the bell", () => {
  it("shows firing counts by severity in words and links to the Alerts page", async () => {
    mockFetch({ ...base, "/v1/alerts/summary": { firing: { critical: 2, warning: 1 }, total: 3, unacknowledged: 3 } });
    render(<App />);
    const bell = await screen.findByRole("link", { name: "Alerts: 2 critical, 1 warning firing" });
    expect(bell.textContent).toBe("2 critical · 1 warning");
    expect(bell.getAttribute("href")).toBe("/alerts?state=firing");
  });

  it("says nothing is firing, never a zero count", async () => {
    mockFetch({ ...base, "/v1/alerts/summary": { firing: {}, total: 0, unacknowledged: 0 } });
    render(<App />);
    const bell = await screen.findByRole("link", { name: "Alerts: No alerts firing" });
    expect(bell.textContent).toBe("No alerts firing");
  });
});

describe("a component's alerts", () => {
  const routes = { ...base, "/v1/alertrules?ownerId=c1": [rule], "/v1/alerts?componentId=c1&state=firing": [firing], "/v1/notificationchannels": [channel] };

  it("lists rules and firing Alerts, and acknowledges", async () => {
    goTo("/applications/app1/components/c1?tab=observability");
    mockFetch(routes);
    render(<App />);
    const section = (await screen.findByRole("heading", { name: "Alerts" })).closest("section")!;
    expect(await within(section).findByText("Memory above 90% of its limit for 5 minutes")).toBeTruthy();
    expect(within(section).getByText(/^95%/)).toBeTruthy();
    fireEvent.click(await enabled("Acknowledge", section));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url === "/v1/alerts/a1/acknowledge")).toBe(true));
  });

  it("silences with a reason, and refuses without one", async () => {
    goTo("/applications/app1/components/c1?tab=observability");
    mockFetch(routes);
    render(<App />);
    fireEvent.click(await enabled("Silence"));
    const form = screen.getByRole("form", { name: "Silence web-memory" });
    fireEvent.click(within(form).getByRole("button", { name: "Silence" }));
    expect(within(form).getByText("Say why, so others know.")).toBeTruthy();
    fireEvent.change(within(form).getByLabelText("Reason"), { target: { value: "deploying a fix" } });
    fireEvent.click(within(form).getByRole("button", { name: "Silence" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST" && c.url === "/v1/alertrules/r1/silence")).toBeTruthy());
    const sent = calls.find((c) => c.url === "/v1/alertrules/r1/silence")!.body as { until: string; reason: string };
    expect(sent.reason).toBe("deploying a fix");
    expect(Date.parse(sent.until)).toBeGreaterThan(Date.now());
  });

  it("creates a rule owned by the component, naming the chosen channel", async () => {
    goTo("/applications/app1/components/c1?tab=observability");
    mockFetch(routes);
    render(<App />);
    fireEvent.click(await enabled("New rule"));
    const form = screen.getByRole("form", { name: "New alert rule" });
    fireEvent.change(within(form).getByLabelText("Name"), { target: { value: "web-restarts" } });
    fireEvent.change(within(form).getByLabelText("Summary"), { target: { value: "web keeps restarting" } });
    fireEvent.change(within(form).getByLabelText("Condition"), { target: { value: "restarts" } });
    fireEvent.change(within(form).getByLabelText("More than, restarts"), { target: { value: "3" } });
    fireEvent.click(await within(form).findByLabelText(/ops-telegram/));
    fireEvent.click(within(form).getByRole("button", { name: "Create rule" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST" && c.url === "/v1/alertrules")).toBeTruthy());
    const body = calls.find((c) => c.method === "POST" && c.url === "/v1/alertrules")!.body as { metadata: { ownerRefs: unknown }; spec: { condition: unknown; channelIds: string[]; environmentId: string } };
    expect(body.metadata.ownerRefs).toEqual([{ kind: "Component", id: "c1" }]);
    expect(body.spec.condition).toEqual({ type: "restarts", restarts: { above: 3, windowMinutes: 5 } });
    expect(body.spec.channelIds).toEqual(["ch1"]);
    expect(body.spec.environmentId).toBe("e1");
  });

  it("creates request and trace rules, saying what each needs (spec 025)", async () => {
    goTo("/applications/app1/components/c1?tab=observability");
    mockFetch(routes);
    render(<App />);
    fireEvent.click(await enabled("New rule"));
    const form = screen.getByRole("form", { name: "New alert rule" });
    fireEvent.change(within(form).getByLabelText("Name"), { target: { value: "web-slow" } });
    fireEvent.change(within(form).getByLabelText("Summary"), { target: { value: "web is slow" } });
    fireEvent.change(within(form).getByLabelText("Condition"), { target: { value: "requestLatency" } });
    expect(within(form).getByText(/Needs request metrics from the cluster's network layer/)).toBeTruthy();
    fireEvent.change(within(form).getByLabelText("Percentile"), { target: { value: "99" } });
    fireEvent.change(within(form).getByLabelText("Above, milliseconds"), { target: { value: "700000" } });
    fireEvent.click(within(form).getByRole("button", { name: "Create rule" }));
    expect(await within(form).findByText(/at most 600000/)).toBeTruthy();
    fireEvent.change(within(form).getByLabelText("Above, milliseconds"), { target: { value: "250" } });
    fireEvent.click(within(form).getByRole("button", { name: "Create rule" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST" && c.url === "/v1/alertrules")).toBeTruthy());
    const body = calls.find((c) => c.method === "POST" && c.url === "/v1/alertrules")!.body as { spec: { condition: unknown } };
    expect(body.spec.condition).toEqual({ type: "requestLatency", requestLatency: { percentile: 99, aboveMs: 250, windowMinutes: 5 } });
  });

  it("sends an error-rate rule's minimum only when it's set", async () => {
    goTo("/applications/app1/components/c1?tab=observability");
    mockFetch(routes);
    render(<App />);
    fireEvent.click(await enabled("New rule"));
    const form = screen.getByRole("form", { name: "New alert rule" });
    fireEvent.change(within(form).getByLabelText("Name"), { target: { value: "web-errors" } });
    fireEvent.change(within(form).getByLabelText("Summary"), { target: { value: "web is failing" } });
    fireEvent.change(within(form).getByLabelText("Condition"), { target: { value: "requestErrorRate" } });
    fireEvent.change(within(form).getByLabelText("Above, % of requests failing"), { target: { value: "5" } });
    fireEvent.change(within(form).getByLabelText("At least, requests"), { target: { value: "20" } });
    fireEvent.click(within(form).getByRole("button", { name: "Create rule" }));
    await waitFor(() => expect(calls.find((c) => c.method === "POST" && c.url === "/v1/alertrules")).toBeTruthy());
    const body = calls.find((c) => c.method === "POST" && c.url === "/v1/alertrules")!.body as { spec: { condition: unknown } };
    expect(body.spec.condition).toEqual({ type: "requestErrorRate", requestErrorRate: { abovePercent: 5, minRequests: 20, windowMinutes: 5 } });
  });
});

describe("an alert", () => {
  it("says why a notification wasn't sent", async () => {
    goTo("/alerts/a1");
    mockFetch({ ...base, "/v1/alerts/a1": firing, "/v1/notificationchannels": [channel] });
    render(<App />);
    expect(await screen.findByText("Not sent: below the channel's minimum")).toBeTruthy();
    expect(await screen.findByText("ops-telegram")).toBeTruthy();
  });

  it("names the rule, and the component and environment on the Alerts page (T010 F2)", async () => {
    goTo("/alerts/a1");
    mockFetch({ ...base, "/v1/alerts/a1": firing, "/v1/alertrules/r1": rule });
    const { unmount } = render(<App />);
    expect(await screen.findByText("web-memory")).toBeTruthy();
    unmount();
    goTo("/alerts");
    mockFetch({ ...base, "/v1/alerts?state=firing": [firing], "/v1/components": [web], "/v1/environments": [env] });
    render(<App />);
    expect(await screen.findByText("web · development")).toBeTruthy();
  });

  it("decides acknowledge at the Alert's rule, a scope the permissions route takes", async () => {
    goTo("/alerts");
    mockFetch({ ...base, "/v1/alerts?state=firing": [firing] });
    render(<App />);
    fireEvent.click(await enabled("Acknowledge"));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url === "/v1/alerts/a1/acknowledge")).toBe(true));
    expect(calls.some((c) => c.url === `/v1/authorization/permissions?scope=${encodeURIComponent("AlertRule/r1")}`)).toBe(true);
    expect(calls.some((c) => c.url.includes("scope=Alert%2F"))).toBe(false);
  });
});

describe("settings › notifications", () => {
  const routes = { ...base, "/v1/notificationchannels": [channel], "/v1/secrets": [secret], "/v1/heartbeat": new Response(JSON.stringify({ title: "not found" }), { status: 404 }) };

  it("shows a channel's Secret only as whether it holds a value, and never reads the value", async () => {
    goTo("/settings/notifications");
    mockFetch(routes);
    render(<App />);
    const item = (await screen.findByRole("heading", { name: "ops-telegram · Telegram" })).closest("li")!;
    expect(within(item).getByText("Value stored")).toBeTruthy();
    expect(within(item).getByText("Telegram chat -1001234")).toBeTruthy();
    fireEvent.click(await enabled("Edit", item));
    expect(within(item).getByLabelText("Bot token").tagName).toBe("SELECT");
    expect(document.body.textContent).not.toContain("AAH-");
    expect(calls.some((c) => c.url.includes("/value"))).toBe(false);
  });

  it("offers the private-address allowance to Owners only", async () => {
    goTo("/settings/notifications");
    mockFetch(routes, owner);
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "New channel" }));
    expect(await screen.findByText("Allowances (Owners only)")).toBeTruthy();
    cleanup();

    mockFetch(routes, contributor);
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "New channel" }));
    await screen.findByRole("form", { name: "New channel" });
    // Permissions load before the form settles; the allowance never appears.
    await waitFor(() => expect(calls.filter((c) => c.url.startsWith("/v1/authorization/permissions")).length).toBeGreaterThan(0));
    expect(screen.queryByText("Allowances (Owners only)")).toBeNull();
    expect(screen.queryByLabelText(/May reach a private address/)).toBeNull();
  });

  it("finds a Telegram chat through the server, which reads the token, and fills the chat ID", async () => {
    goTo("/settings/notifications");
    mockFetch({ ...routes, "POST /v1/secrets/s1/telegramchats": { chats: [{ id: "-100777", type: "group", title: "Ops room" }] } });
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "New channel" }));
    const form = screen.getByRole("form", { name: "New channel" });
    expect(within(form).getByText(/leaves this machine/)).toBeTruthy();
    fireEvent.change(within(form).getByLabelText("Bot token"), { target: { value: "s1" } });
    fireEvent.click(within(form).getByRole("button", { name: "Find chat" }));
    fireEvent.click(await within(form).findByRole("button", { name: "Use Ops room (group, -100777)" }));
    expect((within(form).getByLabelText("Chat ID") as HTMLInputElement).value).toBe("-100777");
    expect(calls.some((c) => c.method === "POST" && c.url === "/v1/secrets/s1/telegramchats" && c.body === undefined)).toBe(true);
    expect(document.body.textContent).not.toContain(BOT_TOKEN);
  });

  it("says when no heartbeat is set, and offers it to platform Owners", async () => {
    goTo("/settings/notifications");
    mockFetch(routes);
    render(<App />);
    expect(await screen.findByText(/No heartbeat is set/)).toBeTruthy();
    expect(await screen.findByRole("button", { name: "Set up a heartbeat" })).toBeTruthy();
  });
});

describe("alert words", () => {
  it("counts the bell's severities, most severe first, without zeros", () => {
    expect(bellCounts({ firing: { info: 4, critical: 1, warning: 0 }, total: 5, unacknowledged: 0 })).toEqual([
      { severity: "critical", count: 1 },
      { severity: "info", count: 4 },
    ]);
    expect(bellLabel(undefined)).toBe("No alerts firing");
  });

  it("describes each condition in its own units", () => {
    expect(conditionText({ type: "metric", metric: { metric: "cpu", as: "value", above: 250, forMinutes: 1 } })).toBe("CPU above 250m for 1 minute");
    expect(conditionText({ type: "logMatch", logMatch: { text: "panic", above: 0, windowMinutes: 10 } })).toBe("More than 0 log lines containing “panic” in 10 minutes");
    expect(conditionText({ type: "packageDisk", packageDisk: { percentOfBudget: 85 } })).toBe("Stored data above 85% of the package's disk budget");
    expect(formatValue(512, { type: "metric", metric: { metric: "memory", as: "value", above: 1, forMinutes: 1 } })).toBe("512 MiB");
    expect(conditionText({ type: "requestErrorRate", requestErrorRate: { abovePercent: 5, minRequests: 20, windowMinutes: 10 } })).toBe("More than 5% of requests failing in 10 minutes, with at least 20 requests");
    expect(conditionText({ type: "requestLatency", requestLatency: { percentile: 95, aboveMs: 250, windowMinutes: 5 } })).toBe("p95 request latency above 250 ms over 5 minutes");
    expect(conditionText({ type: "traceErrors", traceErrors: { above: 0, windowMinutes: 1 } })).toBe("More than 0 traces with an error in 1 minute");
  });

  it("says what a refused condition needs, in the evaluator's words", () => {
    const r = {
      ...rule,
      status: { active: false, activeReason: "ConditionRefused", conditions: [{ type: "Evaluating", status: "False", reason: "ConditionRefused", message: "trace conditions need the tracing package installed", lastTransitionTime: at }] },
    } as unknown as AlertRule;
    expect(ruleStateWords(r, new Date(at)).detail).toBe("trace conditions need the tracing package installed");
  });

  it("keeps a 30-day silence inside the server's limit", () => {
    const now = new Date(at);
    expect(Date.parse(silenceUntil(720, now)) - now.getTime()).toBeLessThan(30 * 24 * 3_600_000);
    expect(Date.parse(silenceUntil(1, now)) - now.getTime()).toBe(3_600_000);
  });

  it("says why a rule isn't evaluating", () => {
    const r = { ...rule, status: { active: false, activeReason: "PackageNotReady" } } as unknown as AlertRule;
    expect(ruleStateWords(r, new Date(at))).toEqual({ kind: "warn", word: "Inactive", detail: "the observability package isn't ready" });
    const silenced = { ...rule, status: { active: true, silence: { by: "person:pat", reason: "fix", until: "2026-11-16T00:00:00Z" } } } as unknown as AlertRule;
    expect(ruleStateWords(silenced, new Date(at)).word).toBe("Silenced");
  });
});
