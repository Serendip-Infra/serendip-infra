import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";

// Spec 026: secret variables on the Component and Deployment forms, their
// delivery on the Deploy tab, and the encryption banner on the Secrets
// page. No value is ever fetched.

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });

let calls: Array<{ url: string; init: RequestInit }>;

function mockFetch(routes: Record<string, unknown>, other?: (url: string, init: RequestInit) => Response | undefined) {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, init });
      const method = init.method ?? "GET";
      const answer = other?.(url, init);
      if (answer) return answer;
      if (url.startsWith("/v1/authorization/permissions?scope=")) {
        const scope = decodeURIComponent(url.slice(url.indexOf("=") + 1));
        return json(200, { scope, permissions: [{ actions: ["*"] }] });
      }
      if (method === "GET" && url in routes) return json(200, routes[url]);
      if (method === "GET" && url === "/v1/readiness") return json(200, {});
      return json(200, []);
    }),
  );
}

const goTo = (path: string) => window.history.replaceState(null, "", path);
const noValueReads = () => expect(calls.some((c) => c.url.includes("/value"))).toBe(false);
const sentPut = () => JSON.parse(calls.find((c) => c.init.method === "PUT")!.init.body as string);

beforeEach(() => {
  goTo("/");
  vi.stubGlobal("scrollTo", () => {});
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const at = "2026-11-24T10:00:00Z";
const meta = (id: string, name: string, kind: string, ownerRefs?: Array<{ kind: string; id: string }>) => ({
  id,
  name,
  kind,
  createdAt: at,
  updatedAt: at,
  resourceVersion: 4,
  ...(ownerRefs ? { ownerRefs } : {}),
});
const ready = [{ type: "Ready", status: "True", reason: "Deployed", lastTransitionTime: at }];

const project = { metadata: meta("p1", "shop", "Project"), spec: { pipeline: { paths: [{ from: "e1", to: ["e2"] }] } }, status: {} };
const envs = [
  { metadata: meta("e1", "development", "Environment", [{ kind: "Project", id: "p1" }]), spec: {}, status: {} },
  { metadata: meta("e2", "staging", "Environment", [{ kind: "Project", id: "p1" }]), spec: {}, status: {} },
];
const app = { metadata: meta("app1", "storefront", "Application", [{ kind: "Project", id: "p1" }]), spec: {}, status: { conditions: ready } };
const comp = {
  metadata: meta("c1", "web", "Component", [{ kind: "Application", id: "app1" }]),
  spec: {
    image: "nginx:1.27",
    env: { LOG: "info" },
    secretEnv: [{ env: "DB_PASSWORD", secret: "s1", key: "password" }],
    dependencies: [{ component: "c2", port: "http", env: "API_URL" }],
  },
  status: { conditions: ready },
};
const devDeploy = {
  metadata: meta("d1", "web", "Deployment", [{ kind: "Environment", id: "e1" }]),
  spec: { component: "c1" },
  status: { conditions: [...ready, { type: "SecretEnvDelivered", status: "True", reason: "Delivered", lastTransitionTime: at }], release: "web-3" },
};
const stgDeploy = {
  metadata: meta("d2", "web", "Deployment", [{ kind: "Environment", id: "e2" }]),
  spec: { component: "c1", overrides: { env: { MODE: "staging" } } },
  status: {
    conditions: [...ready, { type: "SecretEnvDelivered", status: "False", reason: "SecretKeyNotFound", message: "key passwd isn't in Secret db", lastTransitionTime: at }],
    release: "web-3",
  },
};
const dbSecret = { metadata: meta("s1", "db", "Secret", [{ kind: "Project", id: "p1" }]), spec: { path: "db" }, status: {} };
const otherSecret = { metadata: meta("s2", "elsewhere", "Secret", [{ kind: "Project", id: "p9" }]), spec: { path: "x" }, status: {} };
const rootSecret = { metadata: meta("s3", "shared", "Secret"), spec: { path: "shared" }, status: {} };

const routes = {
  "/v1/session": { actor: "person:pat", method: "session" },
  "/v1/applications": [app],
  "/v1/applications/app1": app,
  "/v1/components/c1": comp,
  "/v1/projects/p1": project,
  "/v1/environments?ownerId=p1": envs,
  "/v1/environments/e2": envs[1],
  "/v1/deployments": [devDeploy, stgDeploy],
  "/v1/deployments/d2": stgDeploy,
  "/v1/components/c1/schemas": { environment: { type: "object", properties: {} }, traits: {} },
  "/v1/secrets": [dbSecret, otherSecret, rootSecret],
};

describe("secret variables on the Component", () => {
  it("lists them on the Component page with the Secret linked, and no value", async () => {
    goTo("/applications/app1/components/c1");
    mockFetch(routes);
    render(<App />);
    const table = await screen.findByRole("table", { name: "Secret variables" });
    expect(within(table).getByText("DB_PASSWORD")).toBeTruthy();
    expect(within(table).getByText("password")).toBeTruthy();
    expect((await within(table).findByRole("link", { name: "db" })).getAttribute("href")).toBe("/secrets/s1");
    noValueReads();
  });

  it("adds one, keeps dependencies, and offers only Secrets that may be in scope", async () => {
    goTo("/applications/app1/components/c1/edit");
    mockFetch(routes, (_url, init) => (init.method === "PUT" ? json(200, comp) : undefined));
    render(<App />);
    await screen.findByRole("option", { name: "shared" });
    expect(screen.queryByRole("option", { name: "elsewhere" })).toBeNull();
    const add = screen.getByRole("button", { name: "Add secret variable" });
    fireEvent.change(screen.getByLabelText("Secret"), { target: { value: "s3" } });
    fireEvent.change(screen.getByLabelText("Key"), { target: { value: "token" } });
    // A dependency's variable is refused before it's added.
    fireEvent.change(screen.getByLabelText("Variable"), { target: { value: "API_URL" } });
    fireEvent.click(add);
    expect(await screen.findByText(/API_URL is a dependency's address variable/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Variable"), { target: { value: "API_TOKEN" } });
    fireEvent.click(add);
    expect(await screen.findByRole("button", { name: "Remove secret variable API_TOKEN from this form" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Remove secret variable DB_PASSWORD from this form" }));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls.some((c) => c.init.method === "PUT")).toBe(true));
    const body = sentPut();
    expect(body.spec.secretEnv).toEqual([{ env: "API_TOKEN", secret: "s3", key: "token" }]);
    expect(body.spec.dependencies).toEqual(comp.spec.dependencies);
    noValueReads();
  });

  it("refuses at save a plain variable with a secret variable's name", async () => {
    goTo("/applications/app1/components/c1/edit");
    mockFetch(routes);
    render(<App />);
    fireEvent.change(await screen.findByDisplayValue("LOG"), { target: { value: "DB_PASSWORD" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/DB_PASSWORD is also a plain environment variable/)).toBeTruthy();
    expect(calls.some((c) => c.init.method === "PUT")).toBe(false);
  });
});

describe("secret variables on a Deployment", () => {
  it("overrides one for an environment, and refuses a plain override of it", async () => {
    goTo("/applications/app1/components/c1/deployments/d2/configure");
    mockFetch(routes, (_url, init) => (init.method === "PUT" ? json(200, stgDeploy) : undefined));
    render(<App />);
    expect(await screen.findByRole("group", { name: "Secret variables for staging" })).toBeTruthy();
    expect(await screen.findByText(/The component sets DB_PASSWORD in every environment/)).toBeTruthy();
    await screen.findByRole("option", { name: "db" });
    fireEvent.change(screen.getByLabelText("Secret"), { target: { value: "s1" } });
    fireEvent.change(screen.getByLabelText("Key"), { target: { value: "staging-password" } });
    fireEvent.change(screen.getByLabelText("Variable"), { target: { value: "DB_PASSWORD" } });
    fireEvent.click(screen.getByRole("button", { name: "Add secret variable" }));
    await screen.findByRole("button", { name: "Remove secret variable DB_PASSWORD from this form" });

    fireEvent.change(screen.getByDisplayValue("MODE"), { target: { value: "DB_PASSWORD" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/This is a secret variable of the component/)).toBeTruthy();
    expect(calls.some((c) => c.init.method === "PUT")).toBe(false);

    fireEvent.change(screen.getByDisplayValue("DB_PASSWORD"), { target: { value: "MODE" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls.some((c) => c.init.method === "PUT")).toBe(true));
    const body = sentPut();
    expect(body.spec.overrides.secretEnv).toEqual([{ env: "DB_PASSWORD", secret: "s1", key: "staging-password" }]);
    expect(body.spec.overrides.env).toEqual({ MODE: "staging" });
    noValueReads();
  });

  it("shows delivery on the Deploy tab from SecretEnvDelivered", async () => {
    goTo("/applications/app1/components/c1?tab=deploy");
    mockFetch(routes);
    render(<App />);
    expect(await screen.findByText("1 secret variable, delivered")).toBeTruthy();
    expect(screen.getByText("1 secret variable, not delivered: key passwd isn't in Secret db")).toBeTruthy();
    noValueReads();
  });
});

describe("encryption at rest on the Secrets page", () => {
  it.each([
    ["off", "The cluster doesn't encrypt Secrets at rest"],
    ["unknown", "It isn't known whether the cluster encrypts Secrets at rest"],
    ["on", "Values delivered to workloads rest in the cluster encrypted"],
  ])("says %s", async (state, text) => {
    goTo("/secrets");
    mockFetch({ ...routes, "/v1/secrets/status": { ready: true, encryptionAtRest: state, reason: "X", message: "because" } });
    render(<App />);
    expect(await screen.findByText(new RegExp(text))).toBeTruthy();
    noValueReads();
  });

  it("shows nothing when the status can't be read", async () => {
    goTo("/secrets");
    mockFetch(routes, (url) => (url === "/v1/secrets/status" ? json(403, { title: "forbidden", status: 403 }) : undefined));
    render(<App />);
    await screen.findByRole("link", { name: "db" });
    await waitFor(() => expect(calls.some((c) => c.url === "/v1/secrets/status")).toBe(true));
    expect(screen.queryByText(/encrypt/)).toBeNull();
  });
});
