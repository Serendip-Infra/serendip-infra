// Words and layout for spec 025's networking reads: connection records,
// live traffic between Components, and traces. Pure functions, tested in
// lib.test.ts; the server decides everything they describe.
import { ApiError, type NetworkError } from "../api/client";
import type { Flow, FlowEnd, NetworkFacts, Span, TrafficEdge } from "../api/types";

/** Reading traces is its own data action, as reading logs is (core/policy). */
export const readTraces = "serendip/components/traces/read";

/**
 * The server's reason when a read can't be served: 409 means it never will
 * on this cluster (the network layer doesn't provide it), 503 that it isn't
 * there now (a package not installed or not ready). Undefined for any other
 * failure, which is shown as an error.
 */
export function notAvailableReason(err: ApiError | NetworkError | undefined): string | undefined {
  if (!(err instanceof ApiError) || (err.status !== 409 && err.status !== 503)) return undefined;
  return err.detail ?? err.title;
}

/** "the cluster's network layer doesn't record connections" from "flows: the cluster's ...". */
export function unavailableFor(facts: NetworkFacts | undefined, prefix: "flows" | "requests"): string | undefined {
  const item = facts?.unavailable?.find((u) => u.startsWith(`${prefix}: `));
  return item?.slice(prefix.length + 2);
}

/** "web", "web in staging", a platform workload, an outside address, or an end the caller can't see. */
export function flowEndText(end: FlowEnd, names: ReadonlyMap<string, string>, envNames: ReadonlyMap<string, string> = new Map(), hereEnv?: string): string {
  if (end.outside) return "Not shown";
  if (end.external) return "Outside the cluster";
  if (end.component) {
    const name = names.get(end.component) ?? end.component;
    if (end.environment && end.environment !== hereEnv) return `${name} in ${envNames.get(end.environment) ?? end.environment}`;
    return name;
  }
  if (end.workload) return end.namespace ? `${end.namespace}/${end.workload}` : end.workload;
  if (end.namespace) return end.namespace;
  return "Unknown";
}

/** A connection's verdict in the status vocabulary; a refused one carries its reason. */
export function verdictWords(f: Flow): { kind: "ok" | "error" | "unknown"; word: string } {
  switch (f.verdict) {
    case "forwarded":
      return { kind: "ok", word: "Allowed" };
    case "dropped":
      return { kind: "error", word: "Refused" };
    default:
      return { kind: "unknown", word: f.verdict };
  }
}

/** Refused connections first, each group newest first: what someone debugging a policy looks for. */
export function droppedFirst(flows: Flow[]): Flow[] {
  const refused = flows.filter((f) => f.verdict === "dropped");
  return refused.length === 0 ? flows : [...refused, ...flows.filter((f) => f.verdict !== "dropped")];
}

/** "1.2/s", "0.05/s", "120/s". */
export function perSecond(v: number): string {
  if (v === 0) return "0/s";
  if (v >= 10) return `${Math.round(v).toLocaleString()}/s`;
  if (v >= 0.1) return `${Math.round(v * 10) / 10}/s`;
  return `${Math.round(v * 1000) / 1000}/s`;
}

/** The share of an edge's requests that failed, 0 to 100; undefined with no requests. */
export function errorPercent(e: Pick<TrafficEdge, "rate" | "errorRate">): number | undefined {
  if (!(e.rate > 0)) return undefined;
  return Math.min(100, Math.max(0, (e.errorRate / e.rate) * 100));
}

/** "53%", "0.4%", "0%". */
export function percentText(p: number): string {
  if (p === 0) return "0%";
  return p >= 10 ? `${Math.round(p)}%` : `${Math.round(p * 10) / 10}%`;
}

/**
 * An edge's health as a status: an error rate of 5% or more is an error,
 * any failure a warning. Colour is never the only signal: the label says
 * the numbers.
 */
export function trafficLevel(e: TrafficEdge): { kind: "ok" | "warn" | "error"; label: string } {
  const p = errorPercent(e) ?? 0;
  const label = `${perSecond(e.rate)}, ${percentText(p)} failed`;
  if (p >= 5) return { kind: "error", label };
  if (p > 0) return { kind: "warn", label };
  return { kind: "ok", label };
}

/** "850 µs", "12 ms", "1.4 s" from nanoseconds. */
export function durationText(ns: number): string {
  if (!Number.isFinite(ns) || ns < 0) return "";
  // A chart's 0 tick reads in the axis's unit, not "0 µs" (T012).
  if (ns === 0) return "0 ms";
  if (ns < 1_000_000) return `${Math.round(ns / 1000)} µs`;
  if (ns < 1_000_000_000) {
    const ms = ns / 1_000_000;
    return `${ms >= 10 ? Math.round(ms) : Math.round(ms * 10) / 10} ms`;
  }
  return `${Math.round(ns / 100_000_000) / 10} s`;
}

/** "45 ms" as a latency reading: the request series are in milliseconds. */
export function milliseconds(v: number): string {
  return durationText(v * 1_000_000);
}

export interface WaterfallRow {
  span: Span;
  depth: number;
  /** Where the bar starts and how long it is, as percentages of the trace. */
  offset: number;
  width: number;
}

/**
 * A trace's spans as waterfall rows: each child under its parent, siblings
 * by start time. A span whose parent isn't shown (hidden, or not stored)
 * starts its own tree at the left. Depth is capped so a deep trace stays
 * readable; a cycle in the parent links can't loop.
 */
export function waterfall(spans: Span[], maxDepth = 12): { rows: WaterfallRow[]; totalNs: number } {
  const start = (s: Span) => Date.parse(s.start);
  const valid = spans.filter((s) => Number.isFinite(start(s)));
  if (valid.length === 0) return { rows: [], totalNs: 0 };
  // Times as nanoseconds after the first span: Date.parse is in ms, so
  // sub-millisecond starts collapse, which a waterfall can live with.
  const t0 = Math.min(...valid.map(start));
  const end = Math.max(...valid.map((s) => (start(s) - t0) * 1_000_000 + Math.max(0, s.durationNs)));
  const totalNs = Math.max(end, 1);
  const ids = new Set(valid.map((s) => s.spanId));
  const children = new Map<string, Span[]>();
  const roots: Span[] = [];
  for (const s of valid) {
    if (s.parentId && s.parentId !== s.spanId && ids.has(s.parentId)) {
      const list = children.get(s.parentId) ?? [];
      list.push(s);
      children.set(s.parentId, list);
    } else roots.push(s);
  }
  const byStart = (a: Span, b: Span) => start(a) - start(b) || a.spanId.localeCompare(b.spanId);
  const rows: WaterfallRow[] = [];
  const seen = new Set<string>();
  const visit = (s: Span, depth: number) => {
    if (seen.has(s.spanId)) return;
    seen.add(s.spanId);
    const offsetNs = (start(s) - t0) * 1_000_000;
    rows.push({
      span: s,
      depth: Math.min(depth, maxDepth),
      offset: (offsetNs / totalNs) * 100,
      width: Math.max(0, Math.min(100 - (offsetNs / totalNs) * 100, (Math.max(0, s.durationNs) / totalNs) * 100)),
    });
    for (const c of (children.get(s.spanId) ?? []).sort(byStart)) visit(c, depth + 1);
  };
  for (const r of roots.sort(byStart)) visit(r, 0);
  // Spans only reachable through a cycle: shown at the left, never lost.
  for (const s of valid.sort(byStart)) visit(s, 0);
  return { rows, totalNs };
}
