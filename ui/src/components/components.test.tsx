import { render, screen, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ApiEquivalent, formatRequest } from "./ApiEquivalent";
import { SafeLink } from "./Data";
import { ReconciliationPath } from "./ReconciliationPath";
import { conditionKind, ReadyStatus, StatusText } from "./Status";
import { match } from "../lib/router";
import { logTime, LogView } from "./LogView";

afterEach(cleanup);

describe("StatusText", () => {
  it.each(["ok", "progress", "warn", "error", "unknown"] as const)("renders a word, not just colour (%s)", (kind) => {
    render(<StatusText kind={kind}>Word</StatusText>);
    const el = screen.getByText("Word");
    expect(el.className).toContain(`status--${kind}`);
    expect(el.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("says when nothing has been reconciled", () => {
    render(<ReadyStatus conditions={undefined} />);
    expect(screen.getByText("Not reconciled yet")).toBeTruthy();
  });
});

describe("conditionKind", () => {
  const c = (status: "True" | "False" | "Unknown", reason?: string) => ({ type: "Ready", status, lastTransitionTime: "", ...(reason ? { reason } : {}) });
  it.each([
    [c("True", "Deployed"), "ok"],
    [c("False", "Progressing"), "progress"],
    [c("False", "WaitingForApplication"), "progress"],
    [c("False", "DeployFailed"), "error"],
    [c("False", "RouteRejected"), "error"],
    [c("False", "SomethingNew"), "warn"],
    [c("Unknown"), "unknown"],
  ] as const)("%o is %s", (cond, want) => {
    expect(conditionKind(cond)).toBe(want);
  });
});

describe("ApiEquivalent", () => {
  it("shows method, path and body, never a credential", () => {
    const text = formatRequest({ method: "POST", path: "/v1/applications", body: { metadata: { name: "storefront" } } });
    expect(text).toContain("POST /v1/applications");
    expect(text).toContain('"name": "storefront"');
    expect(text).not.toMatch(/authorization|bearer|cookie|csrf/i);
    render(<ApiEquivalent request={{ method: "GET", path: "/v1/components/x" }} />);
    expect(document.body.textContent).not.toMatch(/authorization|bearer|cookie/i);
  });
});

describe("SafeLink", () => {
  it.each([
    ["http://web.localhost:30080/", true],
    ["https://example.com/a", true],
    ["javascript:alert(1)", false],
    ["data:text/html,<script>alert(1)</script>", false],
    ["/relative/path", false],
    ["vbscript:msgbox", false],
  ])("%s links: %s", (href, linked) => {
    const { container } = render(<SafeLink href={href} />);
    const a = container.querySelector("a");
    expect(Boolean(a)).toBe(linked);
    if (a) expect(a.getAttribute("rel")).toContain("noopener");
    expect(container.textContent).toBe(href);
  });
});

describe("ReconciliationPath", () => {
  it("names each stage's state for screen readers", () => {
    render(
      <ReconciliationPath
        label="Reconciliation"
        stages={[
          { label: "Saved", state: "done" },
          { label: "Deployed", state: "active", detail: "1 of 1 starting" },
        ]}
      />,
    );
    expect(screen.getByRole("list", { name: "Reconciliation" }).textContent).toContain("Deployed: in progress");
  });
});

describe("match", () => {
  it.each([
    ["/applications/:id", "/applications/abc", { id: "abc" }],
    ["/applications/:id", "/applications/a%20b", { id: "a b" }],
    ["/applications/:id", "/applications", null],
    ["/applications/:id/components/:cid", "/applications/a/components/b", { id: "a", cid: "b" }],
    ["/", "/", {}],
  ])("%s ~ %s", (pattern, path, want) => {
    expect(match(pattern, path)).toEqual(want);
  });
});

describe("LogView", () => {
  it("shows a line's time in local time, like the charts, with the UTC timestamp as its title", () => {
    const time = "2026-11-14T14:37:53.411Z";
    render(<LogView lines={[{ time, instance: "i1", line: "hello" }]} label="Logs" following={false} />);
    const el = screen.getByText(logTime(time));
    expect(el.getAttribute("title")).toBe(time);
    const local = new Date(time).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3 });
    expect(logTime(time)).toBe(local);
  });

  it("keeps a time it can't read as it came, and shows nothing for none", () => {
    expect(logTime("not a time")).toBe("not a time");
    expect(logTime(undefined)).toBe("");
  });
});
