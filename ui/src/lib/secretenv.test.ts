import { describe, expect, it } from "vitest";
import type { Application, Secret } from "../api/types";
import { emptySource, fromSpec, toSpec } from "./componentForm";
import { listErrors, maxSecretEnv, mayBeInScope, secretKeyOK, toRows, validateDraft } from "./secretEnv";

const res = (id: string, kind: string, owner?: { kind: string; id: string }) =>
  ({ metadata: { id, name: id, kind, createdAt: "", updatedAt: "", resourceVersion: 1, ...(owner ? { ownerRefs: [owner] } : {}) }, spec: {}, status: {} }) as unknown;

describe("secret variables", () => {
  it("checks keys as the server does", () => {
    expect(secretKeyOK("password")).toBe(true);
    expect(secretKeyOK("")).toBe(false);
    expect(secretKeyOK("a\nb")).toBe(false);
    expect(secretKeyOK("é".repeat(128))).toBe(true);
    expect(secretKeyOK("é".repeat(129))).toBe(false);
  });

  it("refuses a bad draft field by field", () => {
    const rows = toRows([{ env: "DB", secret: "s1", key: "k" }]);
    const taken = new Map([["MODE", "a plain environment variable"]]);
    expect(validateDraft({ secret: "", key: "", env: "1X" }, rows, taken)).toEqual({ secret: expect.any(String), key: expect.any(String), env: expect.any(String) });
    expect(validateDraft({ secret: "s1", key: "k", env: "DB" }, rows, taken).env).toMatch(/already a secret variable/);
    expect(validateDraft({ secret: "s1", key: "k", env: "MODE" }, rows, taken).env).toMatch(/MODE is a plain environment variable/);
    expect(validateDraft({ secret: "s1", key: "k", env: "NEW" }, rows, taken)).toEqual({});
    const full = toRows(Array.from({ length: maxSecretEnv }, (_, i) => ({ env: `V${i}`, secret: "s1", key: "k" })));
    expect(validateDraft({ secret: "s1", key: "k", env: "NEW" }, full, taken).env).toMatch(/At most 64/);
  });

  it("finds clashes that appear after adding", () => {
    expect(listErrors(toRows([{ env: "MODE", secret: "s1", key: "k" }]), new Map([["MODE", "a plain environment variable"]]))).toEqual({ MODE: expect.stringMatching(/MODE is also/) });
  });

  it("rules out only Secrets known to be in another Project", () => {
    const apps = [res("a1", "Application", { kind: "Project", id: "p1" }), res("a2", "Application", { kind: "Project", id: "p2" })] as Application[];
    const s = (owner?: { kind: string; id: string }) => res("s", "Secret", owner) as Secret;
    expect(mayBeInScope(s(), "p1", apps)).toBe(true);
    expect(mayBeInScope(s({ kind: "Project", id: "p1" }), "p1", apps)).toBe(true);
    expect(mayBeInScope(s({ kind: "Project", id: "p2" }), "p1", apps)).toBe(false);
    expect(mayBeInScope(s({ kind: "Application", id: "a1" }), "p1", apps)).toBe(true);
    expect(mayBeInScope(s({ kind: "Application", id: "a2" }), "p1", apps)).toBe(false);
    expect(mayBeInScope(s({ kind: "Organization", id: "o1" }), "p1", apps)).toBe(true);
  });

  it("round-trips through the component form", () => {
    const spec = { image: "nginx", env: { A: "1" }, secretEnv: [{ env: "DB", secret: "s1", key: "password" }] };
    expect(toSpec(fromSpec("web", spec))).toEqual(spec);
    expect(toSpec({ ...fromSpec("web", spec), source: emptySource, secretEnv: [] }).secretEnv).toBeUndefined();
  });
});
