import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { ApiError, apiFetch, apiList, isShown, type NetworkError } from "../api/client";
import type { Application, Component, Deployment, Environment, LogLine, LogSearchPage, Metrics, Project, StoredLogLine } from "../api/types";
import { ApiEquivalent } from "../components/ApiEquivalent";
import { AvailabilityGate } from "../components/Availability";
import { SelectField, TextField } from "../components/Field";
import { LogView } from "../components/LogView";
import { MetricChart } from "../components/MetricChart";
import { RequestError } from "../components/RequestError";
import { EmptyState, LoadingState, Notice } from "../components/States";
import { StatusText } from "../components/Status";
import { useOptional } from "../hooks/useOptional";
import { usePermissions } from "../hooks/usePermissions";
import { usePoll } from "../hooks/usePoll";
import { availability } from "../lib/health";
import { readLogs } from "../lib/permissions";
import { orderEnvironments, ownerOf } from "../lib/pipeline";
import { readTraces } from "../lib/networking";
import { ComponentAlerts } from "./Alerts";
import { useDeployData } from "./Deploy";
import { FlowsSection, RequestsBlock, TracesSection } from "./Networking";

// Spec 023's console: stored logs, searched by text and time, and a
// Deployment's CPU and memory, once the observability package is ready.
// Spec 025 adds its HTTP requests, connections and traces (Networking.tsx).

const enc = encodeURIComponent;

/** Metrics are their own action, in the read-only roles; logs aren't (core/policy). */
export const readObservability = "serendip/observability/read";

/** The server gives a query 20 s, so the console waits a little longer. */
export const QUERY_TIMEOUT_MS = 25_000;

const MIN = 60_000;
/** Time presets, newest data first. The longest stays inside the 3-day retention. */
export const ranges = [
  { value: "15m", label: "Last 15 minutes", ms: 15 * MIN },
  { value: "1h", label: "Last hour", ms: 60 * MIN },
  { value: "6h", label: "Last 6 hours", ms: 360 * MIN },
  { value: "24h", label: "Last 24 hours", ms: 1440 * MIN },
  { value: "2d", label: "Last 2 days", ms: 2880 * MIN },
];
export const rangeMs = (v: string) => (ranges.find((r) => r.value === v) ?? ranges[1]!).ms;

/** A range ending now, frozen when a read starts, so its pages agree. */
export function rangeEnding(range: string): { from: string; to: string } {
  const to = new Date();
  return { from: new Date(to.getTime() - rangeMs(range)).toISOString(), to: to.toISOString() };
}

export function RangeField({ id, value, onChange }: { id: string; value: string; onChange: (v: string) => void }) {
  return <SelectField id={id} label="Time range" value={value} onChange={(e) => onChange(e.target.value)} options={ranges.map(({ value, label }) => ({ value, label }))} />;
}

/** "120m", or "1.2m" below 10: CPU in millicores. */
export const millicores = (v: number) => `${v >= 10 ? Math.round(v).toLocaleString() : (Math.round(v * 10) / 10).toString()}m`;
/** "256 MiB". */
export const mebibytes = (v: number) => `${v >= 10 ? Math.round(v).toLocaleString() : (Math.round(v * 10) / 10).toString()} MiB`;

/** "requests, errors and latency: needs request metrics, not installed" as what and why. */
export function splitUnavailable(item: string): { what: string; why: string } {
  const i = item.indexOf(": ");
  const what = i < 0 ? item : item.slice(0, i);
  const why = i < 0 ? "" : item.slice(i + 2);
  const named: Record<string, string> = { cpu: "CPU", memory: "Memory", cpuRequest: "CPU request", cpuLimit: "CPU limit", memoryRequest: "Memory request", memoryLimit: "Memory limit" };
  return { what: named[what] ?? `${what.charAt(0).toUpperCase()}${what.slice(1)}`, why };
}

/**
 * ObservabilityTab: one Component's stored logs and its CPU and memory,
 * per Environment it's deployed to (spec 023). Greyed out until the
 * observability package is ready.
 */
export function ObservabilityTab({ app, comp }: { app: Application; comp: Component }) {
  const opt = useOptional("observability");
  if (!opt.loaded) return <LoadingState what="package availability" />;
  if (availability(opt.readiness) !== "ready") {
    return (
      <>
        <AvailabilityGate title="Stored logs and metrics" id="obs-title" readiness={opt.readiness} packageName={opt.packageName} what="stored logs and metrics" />
        <p className="field-hint">Recent logs and events don't need this package: they're on the Runtime tab.</p>
      </>
    );
  }
  return (
    <>
      <ComponentObservability app={app} comp={comp} />
      <ComponentAlertsFor app={app} comp={comp} />
    </>
  );
}

/** The Component's alert rules and Alerts, with the Project's Environments for the rule form. */
function ComponentAlertsFor({ app, comp }: { app: Application; comp: Component }) {
  const projectId = ownerOf(app.metadata, "Project");
  const data = useDeployData(projectId, comp.metadata.id);
  if (projectId && !data.data) return null;
  const envs = data.data ? orderEnvironments(data.data.project.spec.pipeline, data.data.envs).map(({ env }) => env) : [];
  return <ComponentAlerts comp={comp} envs={envs} />;
}

function ComponentObservability({ app, comp }: { app: Application; comp: Component }) {
  const projectId = ownerOf(app.metadata, "Project");
  const data = useDeployData(projectId, comp.metadata.id);
  const [chosen, setChosen] = useState<string>();
  const [range, setRange] = useState("1h");

  if (data.error) return <RequestError error={data.error} what="deployments" />;
  if (projectId && !data.data) return <LoadingState what="deployments" />;
  const ordered = data.data ? orderEnvironments(data.data.project.spec.pipeline, data.data.envs) : [];
  const deployed = ordered.flatMap(({ env }) => {
    const d = data.data?.deployments.find((x) => ownerOf(x.metadata, "Environment") === env.metadata.id);
    return d ? [{ env: env.metadata.name, d }] : [];
  });
  if (deployed.length === 0) return <EmptyState title="Not deployed anywhere">Deploy the component to an Environment to see its stored logs and metrics.</EmptyState>;
  const current = deployed.find((x) => x.d.metadata.id === chosen) ?? deployed[0]!;

  return (
    <>
      <div className="form-row">
        {deployed.length > 1 && (
          <SelectField
            id="obs-env"
            label="Environment"
            value={current.d.metadata.id}
            onChange={(e) => setChosen(e.target.value)}
            options={deployed.map((x) => ({ value: x.d.metadata.id, label: x.env }))}
          />
        )}
        <RangeField id="obs-range" value={range} onChange={setRange} />
      </div>
      <DeploymentObservability key={current.d.metadata.id} d={current.d} envName={current.env} range={range} />
    </>
  );
}

function DeploymentObservability({ d, envName, range }: { d: Deployment; envName: string; range: string }) {
  const id = d.metadata.id;
  const access = usePermissions({ kind: "Deployment", id });
  return (
    <>
      <MetricsSection id={id} envName={envName} range={range} allowed={access.can(readObservability)} />
      <FlowsSection id={id} envId={ownerOf(d.metadata, "Environment") ?? ""} envName={envName} range={range} allowed={access.can(readObservability)} />
      <TracesSection id={id} envName={envName} range={range} allowed={access.can(readTraces)} />
      <LogSearch
        idPrefix="obs"
        title={`Stored logs in ${envName}`}
        path={`/v1/deployments/${enc(id)}/logs/search`}
        shownPath={`/v1/deployments/${id}/logs/search`}
        range={range}
        allowed={access.can(readLogs)}
        source={(l) => l.instance}
      />
    </>
  );
}

/** A read that keeps what it showed while the next one loads (dataviz: refetch keeps the frame). */
export function useQuery<T>(path: string | undefined, key: string) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<ApiError | NetworkError>();
  const [loading, setLoading] = useState(false);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (!path) return;
    const ctrl = new AbortController();
    setLoading(true);
    setError(undefined);
    apiFetch<T>("GET", path, undefined, ctrl.signal, QUERY_TIMEOUT_MS)
      .then((v) => setData(v))
      .catch((err: unknown) => {
        if (!ctrl.signal.aborted && isShown(err)) setError(err);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
    return () => ctrl.abort();
  }, [key, tick]);
  return { data, error, loading, refresh: () => setTick((t) => t + 1) };
}

function MetricsSection({ id, envName, range, allowed }: { id: string; envName: string; range: string; allowed: boolean | undefined }) {
  // The range ends now when it's read; Refresh reads it again.
  const [w, setW] = useState(() => ({ range, ...rangeEnding(range) }));
  // A new preset is a new range, set while rendering so it's read once.
  if (w.range !== range) setW({ range, ...rangeEnding(range) });
  const path = `/v1/deployments/${enc(id)}/metrics?from=${enc(w.from)}`;
  const q = useQuery<Metrics>(allowed ? path : undefined, `${path}-${allowed}`);
  const m = q.data;

  return (
    <section className="section" aria-labelledby="obs-metrics" aria-busy={q.loading}>
      <div className="pipeline-head">
        <h2 id="obs-metrics" className="section-title">
          CPU, memory and requests in {envName}
        </h2>
        {allowed && (
          <button type="button" className="button" onClick={() => (setW({ range, ...rangeEnding(range) }), q.refresh())} disabled={q.loading}>
            {q.loading ? "Reading…" : "Refresh"}
          </button>
        )}
      </div>
      {allowed === false ? (
        <p className="state-body">Your roles here don't allow reading metrics.</p>
      ) : allowed === undefined ? (
        <LoadingState what="your access" />
      ) : q.error ? (
        <RequestError error={q.error} what="metrics" />
      ) : !m ? (
        <LoadingState what="metrics" />
      ) : (
        <>
          <p className="field-hint">All instances together, against what the component asks for (request) and the most it may use (limit).</p>
          <div className={q.loading ? "chart-grid-2 chart-grid-2--stale" : "chart-grid-2"}>
            <MetricChart id="obs-cpu" title="CPU, millicores" points={m.cpu ?? []} from={m.from} to={m.to} request={m.cpuRequest} limit={m.cpuLimit} format={millicores} />
            <MetricChart id="obs-mem" title="Memory, MiB" points={m.memory ?? []} from={m.from} to={m.to} request={m.memoryRequest} limit={m.memoryLimit} format={mebibytes} />
          </div>
          <RequestsBlock m={m} stale={q.loading} />
          {(m.unavailable ?? []).some((u) => !u.startsWith("requests: ")) && (
            <ul className="plain-list" aria-label="Not available">
              {m.unavailable!.filter((u) => !u.startsWith("requests: ")).map((item) => {
                const { what, why } = splitUnavailable(item);
                return (
                  <li key={item}>
                    <StatusText kind="unknown">Not available</StatusText> {what}
                    {why && `: ${why}`}
                  </li>
                );
              })}
            </ul>
          )}
        </>
      )}
      <ApiEquivalent request={{ method: "GET", path: `/v1/deployments/${id}/metrics?from=${enc(w.from)}` }} />
    </section>
  );
}

/** At most this many empty pages are read on in a row before "older" is offered: a page can be empty with more behind it. */
const EMPTY_HOPS = 4;

/**
 * LogSearch: stored lines, newest first, by text and time (spec 023). A
 * search's range is frozen when it starts, so "Show older lines" pages
 * through the same lines; only an empty next ends it.
 */
function LogSearch({
  idPrefix,
  title,
  path: base,
  shownPath,
  params = {},
  range,
  allowed,
  source,
  explain,
  children,
}: {
  idPrefix: string;
  title: string;
  /** The search route, IDs encoded. */
  path: string;
  /** The same, as "Same request through the API" shows it. */
  shownPath: string;
  params?: Record<string, string>;
  range: string;
  allowed: boolean | undefined;
  /** What a line came from, shown before it. */
  source: (l: StoredLogLine) => string;
  explain?: (err: ApiError | NetworkError) => ReactNode | undefined;
  children?: ReactNode;
}) {
  const [text, setText] = useState("");
  const [search, setSearch] = useState<{ q: string; from: string; to: string }>();
  const [lines, setLines] = useState<LogLine[]>();
  const [next, setNext] = useState<string>();
  const [truncated, setTruncated] = useState(false);
  const [omitted, setOmitted] = useState(0);
  const [error, setError] = useState<ApiError | NetworkError>();
  const [busy, setBusy] = useState(false);
  const [lastQuery, setLastQuery] = useState<string>();
  const ctrl = useRef<AbortController | undefined>(undefined);
  const paramKey = JSON.stringify(params);

  function query(s: { q: string; from: string; to: string }, before?: string): string {
    const p = new URLSearchParams(params);
    if (s.q) p.set("q", s.q);
    p.set("from", s.from);
    p.set("to", s.to);
    if (before) p.set("before", before);
    return p.toString();
  }

  async function read(s: { q: string; from: string; to: string }, before: string | undefined, fresh: boolean) {
    ctrl.current?.abort();
    const c = new AbortController();
    ctrl.current = c;
    setBusy(true);
    setError(undefined);
    if (fresh) {
      setLines(undefined);
      setNext(undefined);
      setTruncated(false);
      setOmitted(0);
    }
    try {
      let cursor = before;
      let got: LogLine[] = [];
      let page: LogSearchPage;
      let hops = 0;
      for (;;) {
        const qs = query(s, cursor);
        setLastQuery(qs);
        page = await apiFetch<LogSearchPage>("GET", `${base}?${qs}`, undefined, c.signal, QUERY_TIMEOUT_MS);
        got = got.concat((page.lines ?? []).map((l) => ({ time: l.time, instance: source(l), line: l.line })));
        cursor = page.next || undefined;
        if (got.length > 0 || !cursor || ++hops >= EMPTY_HOPS) break;
      }
      setLines((prev) => (fresh ? got : [...(prev ?? []), ...got]));
      setNext(cursor);
      if (page.truncated) setTruncated(true);
      setOmitted(page.omitted ?? 0);
    } catch (err) {
      if (!c.signal.aborted && isShown(err)) setError(err);
    } finally {
      if (ctrl.current === c) setBusy(false);
    }
  }

  // A new range, or a new place to search, starts the search again with the text already searched.
  useEffect(() => {
    if (!allowed) return;
    const s = { q: search?.q ?? "", ...rangeEnding(range) };
    setSearch(s);
    void read(s, undefined, true);
    return () => ctrl.current?.abort();
  }, [base, paramKey, range, allowed]);

  function submit(e: FormEvent) {
    e.preventDefault();
    const s = { q: text.trim(), ...rangeEnding(range) };
    setSearch(s);
    void read(s, undefined, true);
  }

  const explained = error && explain?.(error);
  const id = `${idPrefix}-search`;
  return (
    <section className="section" aria-labelledby={id} aria-busy={busy}>
      <h2 id={id} className="section-title">
        {title}
      </h2>
      {children}
      {allowed === false ? (
        <p className="state-body">Logs can hold sensitive data, so reading them needs its own permission (the Log Reader role), which your roles here don't include.</p>
      ) : allowed === undefined ? (
        <LoadingState what="your access" />
      ) : (
        <>
          <form className="form-row" onSubmit={submit} role="search" aria-label={title}>
            <TextField id={`${id}-text`} label="Containing" value={text} maxLength={256} onChange={(e) => setText(e.target.value)} />
            <div className="form-actions">
              <button type="submit" className="button button--primary" disabled={busy}>
                Search
              </button>
            </div>
          </form>
          <p className="field-hint" role="status">
            {busy ? "Searching…" : "Lines containing the phrase, or every line when it's empty, newest first. Lines stay searchable after their instance is replaced, until they reach the package's retention period."}
          </p>
          {omitted > 0 && (
            <Notice>
              {omitted === 1 ? "1 component's logs aren't searched" : `${omitted} components' logs aren't searched`}: your roles don't allow reading them.
            </Notice>
          )}
          {explained ? <p className="state-body">{explained}</p> : error ? <RequestError error={error} what="logs" /> : !lines ? <LoadingState what="logs" /> : <LogView lines={lines} label={title} following={false} />}
          {truncated && <Notice>Some lines were cut short, or a limit on the search was reached.</Notice>}
          {next && !error && (
            <div className="form-actions">
              <button type="button" className="button" disabled={busy} onClick={() => search && void read(search, next, false)}>
                {busy ? "Reading…" : "Show older lines"}
              </button>
            </div>
          )}
          <ApiEquivalent request={{ method: "GET", path: `${shownPath}?${lastQuery ?? query(search ?? { q: "", ...rangeEnding(range) })}` }} />
        </>
      )}
    </section>
  );
}

/** A 403 on an Application's search: every Deployment here is one the caller can't read. */
function explainAppSearch(err: ApiError | NetworkError): string | undefined {
  if (err instanceof ApiError && err.status === 403) return "Your roles don't allow reading the logs of any component in this Environment (the Log Reader role).";
  if (err instanceof ApiError && err.status === 422) return "This Application has more components here than one search reads (20). Search a component's logs from its Observability tab instead.";
  return undefined;
}

/**
 * ApplicationLogSearch: every Component's stored lines in one
 * Environment (spec 023). Only Deployments whose logs the caller may read
 * are searched; how many were left out is said, not who.
 */
export function ApplicationLogSearch({ app, comps }: { app: Application; comps: Component[] | undefined }) {
  const opt = useOptional("observability");
  if (!opt.loaded) return null;
  if (availability(opt.readiness) !== "ready") {
    return <AvailabilityGate title="Log search" id="app-logs-title" readiness={opt.readiness} packageName={opt.packageName} what="log search" />;
  }
  return <AppSearch app={app} comps={comps} />;
}

function AppSearch({ app, comps }: { app: Application; comps: Component[] | undefined }) {
  const projectId = ownerOf(app.metadata, "Project");
  const envs = usePoll(
    async (s) => {
      if (!projectId) return [];
      const [project, list] = await Promise.all([apiFetch<Project>("GET", `/v1/projects/${enc(projectId)}`, undefined, s), apiList<Environment>(`/v1/environments?ownerId=${enc(projectId)}`, s)]);
      return orderEnvironments(project.spec.pipeline, list).map(({ env }) => env);
    },
    60_000,
    `app-search-envs-${projectId ?? ""}`,
  );
  const [env, setEnv] = useState<string>();
  const [range, setRange] = useState("1h");
  const names = new Map((comps ?? []).map((c) => [c.metadata.id, c.metadata.name]));

  if (!projectId) {
    return (
      <section className="section" aria-labelledby="app-logs">
        <h2 id="app-logs" className="section-title">
          Log search
        </h2>
        <p className="state-body">This application isn't in a Project, so it has no Environments to search.</p>
      </section>
    );
  }
  if (envs.error) return <RequestError error={envs.error} what="environments" />;
  if (!envs.data) return <LoadingState what="environments" />;
  if (envs.data.length === 0) return null;
  const current = envs.data.find((e) => e.metadata.id === env) ?? envs.data[0]!;
  const appId = app.metadata.id;

  return (
    <LogSearch
      idPrefix="app-logs"
      title="Log search"
      path={`/v1/applications/${enc(appId)}/logs/search`}
      shownPath={`/v1/applications/${appId}/logs/search`}
      params={{ environment: current.metadata.id }}
      range={range}
      allowed
      source={(l) => `${names.get(l.component) ?? l.component} ${l.instance}`}
      explain={explainAppSearch}
    >
      <div className="form-row">
        <SelectField id="app-logs-env" label="Environment" value={current.metadata.id} onChange={(e) => setEnv(e.target.value)} options={envs.data.map((e) => ({ value: e.metadata.id, label: e.metadata.name }))} />
        <RangeField id="app-logs-range" value={range} onChange={setRange} />
      </div>
    </LogSearch>
  );
}
