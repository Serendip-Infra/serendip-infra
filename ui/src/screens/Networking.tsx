import { useEffect, useRef, useState } from "react";
import { apiFetch, apiList, isShown, type ApiError, type NetworkError } from "../api/client";
import type { Component, Environment, Flow, FlowPage, Metrics, MetricPoint, Span, TracePage, TraceSummary, TraceView } from "../api/types";
import { ApiEquivalent } from "../components/ApiEquivalent";
import { AvailabilityGate } from "../components/Availability";
import { KeyValueList } from "../components/Data";
import { SelectField } from "../components/Field";
import { MetricChart } from "../components/MetricChart";
import { RequestError } from "../components/RequestError";
import { LoadingState, Notice } from "../components/States";
import { StatusText } from "../components/Status";
import { useOptional } from "../hooks/useOptional";
import { usePoll } from "../hooks/usePoll";
import { availability } from "../lib/health";
import { droppedFirst, durationText, flowEndText, milliseconds, notAvailableReason, perSecond, verdictWords, waterfall } from "../lib/networking";
import { QUERY_TIMEOUT_MS, rangeEnding, splitUnavailable } from "./Observability";

// Spec 025's console: a Deployment's HTTP requests, its connection
// records and its traces. Every "not available" says the server's reason.

const enc = encodeURIComponent;

/** Said plainly, never drawn (constitution 6). */
export function NotAvailable({ why }: { why: string }) {
  return (
    <p className="state-body">
      <StatusText kind="unknown">Not available</StatusText> {why}
    </p>
  );
}

/** The latency series a chart shows: one series per chart (ui/DESIGN.md). */
const percentiles = [
  { value: "latencyP50", label: "p50 (median)" },
  { value: "latencyP95", label: "p95" },
  { value: "latencyP99", label: "p99" },
] as const;
type Percentile = (typeof percentiles)[number]["value"];

/**
 * RequestsBlock: a Deployment's HTTP requests from the metrics answer
 * (spec 025 criterion 4): rate, 5xx answers and a latency percentile, or
 * the reason they can't be read.
 */
export function RequestsBlock({ m, stale }: { m: Metrics; stale: boolean }) {
  const [p, setP] = useState<Percentile>("latencyP95");
  const r = m.requests;
  const why = m.unavailable?.map(splitUnavailable).find((u) => u.what === "Requests")?.why;
  return (
    <>
      <div className="pipeline-head">
        <h3 id="obs-requests" className="subsection-title">
          HTTP requests
        </h3>
        {r && <SelectField id="obs-percentile" label="Latency" value={p} onChange={(e) => setP(e.target.value as Percentile)} options={percentiles.map(({ value, label }) => ({ value, label }))} />}
      </div>
      {!r ? (
        <NotAvailable why={why ?? "no request metrics were returned"} />
      ) : (
        <>
          <p className="field-hint">Requests the component answered, as the network layer saw them; non-HTTP traffic is in Connections below.</p>
          <div className={stale ? "chart-grid-2 chart-grid-2--stale" : "chart-grid-2"}>
            <MetricChart id="obs-rate" title="Requests per second" points={r.rate ?? []} from={m.from} to={m.to} format={perSecond} />
            <MetricChart id="obs-errors" title="Server errors (5xx) per second" points={r.errorRate ?? []} from={m.from} to={m.to} format={perSecond} />
            <MetricChart id="obs-latency" title={`Latency ${percentiles.find((x) => x.value === p)!.label}, milliseconds`} points={(r[p] as MetricPoint[] | undefined) ?? []} from={m.from} to={m.to} format={milliseconds} />
          </div>
        </>
      )}
    </>
  );
}

/** The names a flow's ends are shown with: Components and Environments the caller can read. */
function useNames() {
  const comps = usePoll((s) => apiList<Component>("/v1/components", s), 60_000, "names-components");
  const envs = usePoll((s) => apiList<Environment>("/v1/environments", s), 60_000, "names-environments");
  return {
    comps: new Map((comps.data ?? []).map((c) => [c.metadata.id, c.metadata.name])),
    envs: new Map((envs.data ?? []).map((e) => [e.metadata.id, e.metadata.name])),
  };
}

/**
 * FlowsSection: a Deployment's connection records, newest first, refused
 * ones first with the policy's reason (spec 025 criteria 3 and 6). An end
 * the caller can't read is said to be hidden, never named.
 */
export function FlowsSection({ id, envId, envName, range, allowed }: { id: string; envId: string; envName: string; range: string; allowed: boolean | undefined }) {
  const names = useNames();
  const [verdict, setVerdict] = useState("all");
  const [win, setWin] = useState(() => rangeEnding(range));
  const [flows, setFlows] = useState<Flow[]>();
  const [next, setNext] = useState<string>();
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState<ApiError | NetworkError>();
  const [busy, setBusy] = useState(false);
  const ctrl = useRef<AbortController | undefined>(undefined);

  const query = (w: { from: string; to: string }, before?: string) => {
    const q = new URLSearchParams({ from: w.from, to: w.to, verdict, limit: "50" });
    if (before) q.set("before", before);
    return q.toString();
  };

  async function read(w: { from: string; to: string }, before: string | undefined) {
    ctrl.current?.abort();
    const c = new AbortController();
    ctrl.current = c;
    setBusy(true);
    setError(undefined);
    if (!before) {
      setFlows(undefined);
      setNext(undefined);
      setTruncated(false);
    }
    try {
      const page = await apiFetch<FlowPage>("GET", `/v1/deployments/${enc(id)}/flows?${query(w, before)}`, undefined, c.signal, QUERY_TIMEOUT_MS);
      setFlows((prev) => [...(before ? (prev ?? []) : []), ...(page.flows ?? [])]);
      setNext(page.next || undefined);
      if (page.truncated) setTruncated(true);
    } catch (err) {
      if (!c.signal.aborted && isShown(err)) setError(err);
    } finally {
      if (ctrl.current === c) setBusy(false);
    }
  }

  useEffect(() => {
    if (!allowed) return;
    const w = rangeEnding(range);
    setWin(w);
    void read(w, undefined);
    return () => ctrl.current?.abort();
  }, [id, range, verdict, allowed]);

  const why = notAvailableReason(error);
  return (
    <section className="section" aria-labelledby="obs-flows" aria-busy={busy}>
      <div className="pipeline-head">
        <h2 id="obs-flows" className="section-title">
          Connections in {envName}
        </h2>
        {allowed && !why && (
          <button type="button" className="button" disabled={busy} onClick={() => (setWin(rangeEnding(range)), void read(rangeEnding(range), undefined))}>
            {busy ? "Reading…" : "Refresh"}
          </button>
        )}
      </div>
      {allowed === false ? (
        <p className="state-body">Your roles here don't allow reading connections.</p>
      ) : allowed === undefined ? (
        <LoadingState what="your access" />
      ) : why ? (
        <NotAvailable why={why} />
      ) : (
        <>
          <div className="form-row">
            <SelectField
              id="obs-verdict"
              label="Show"
              value={verdict}
              onChange={(e) => setVerdict(e.target.value)}
              options={[
                { value: "all", label: "All connections" },
                { value: "dropped", label: "Refused only" },
              ]}
            />
          </div>
          <p className="field-hint">Connections to and from the component, one row per connection, not per request. Refused connections are listed first. An end your roles don't let you read isn't named.</p>
          {error ? (
            <RequestError error={error} what="connections" />
          ) : !flows ? (
            <LoadingState what="connections" />
          ) : flows.length === 0 ? (
            <p className="state-body">{verdict === "dropped" ? "No refused connections in this range." : "No connections in this range."}</p>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">Time</th>
                    <th scope="col">From</th>
                    <th scope="col">To</th>
                    <th scope="col">Port</th>
                    <th scope="col">Result</th>
                    <th scope="col">Count</th>
                  </tr>
                </thead>
                <tbody>
                  {droppedFirst(flows).map((f, i) => {
                    const v = verdictWords(f);
                    return (
                      <tr key={`${f.time}-${i}`}>
                        <td>{new Date(f.time).toLocaleString()}</td>
                        <td>{flowEndText(f.source, names.comps, names.envs, envId)}</td>
                        <td>{flowEndText(f.destination, names.comps, names.envs, envId)}</td>
                        <td className="mono">
                          {f.port ? `${f.port}/` : ""}
                          {f.protocol}
                        </td>
                        <td>
                          <StatusText kind={v.kind}>{v.word}</StatusText>
                          {f.reason && <span className="field-hint"> {f.reason}</span>}
                        </td>
                        <td>{f.count.toLocaleString()}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
          {truncated && <Notice>A limit on the read was reached: some connections aren't listed.</Notice>}
          {next && !error && (
            <div className="form-actions">
              <button type="button" className="button" disabled={busy} onClick={() => void read(win, next)}>
                {busy ? "Reading…" : "Show older connections"}
              </button>
            </div>
          )}
        </>
      )}
      <ApiEquivalent request={{ method: "GET", path: `/v1/deployments/${id}/flows?${query(win)}` }} />
    </section>
  );
}

const minDurations = [
  { value: "", label: "Any duration" },
  { value: "100ms", label: "100 ms or longer" },
  { value: "500ms", label: "500 ms or longer" },
  { value: "1s", label: "1 s or longer" },
  { value: "5s", label: "5 s or longer" },
];

/**
 * TracesSection: a Deployment's traces (spec 025 criterion 7), greyed out
 * until the tracing package is ready. Reading them is its own permission,
 * as logs are.
 */
export function TracesSection(props: { id: string; envName: string; range: string; allowed: boolean | undefined }) {
  const opt = useOptional("tracing");
  if (!opt.loaded) return null;
  if (availability(opt.readiness) !== "ready") {
    return <AvailabilityGate title="Traces" id="obs-traces" readiness={opt.readiness} packageName={opt.packageName} what="traces" />;
  }
  return <TraceList {...props} />;
}

function TraceList({ id, envName, range, allowed }: { id: string; envName: string; range: string; allowed: boolean | undefined }) {
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [minDuration, setMinDuration] = useState("");
  const [win, setWin] = useState(() => rangeEnding(range));
  const [traces, setTraces] = useState<TraceSummary[]>();
  const [next, setNext] = useState<string>();
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState<ApiError | NetworkError>();
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState<string>();
  const ctrl = useRef<AbortController | undefined>(undefined);

  const query = (w: { from: string; to: string }, before?: string) => {
    const q = new URLSearchParams({ from: w.from, to: w.to, limit: "50" });
    if (errorsOnly) q.set("errorsOnly", "true");
    if (minDuration) q.set("minDuration", minDuration);
    if (before) q.set("before", before);
    return q.toString();
  };

  async function read(w: { from: string; to: string }, before: string | undefined) {
    ctrl.current?.abort();
    const c = new AbortController();
    ctrl.current = c;
    setBusy(true);
    setError(undefined);
    if (!before) {
      setTraces(undefined);
      setNext(undefined);
      setTruncated(false);
    }
    try {
      const page = await apiFetch<TracePage>("GET", `/v1/deployments/${enc(id)}/traces?${query(w, before)}`, undefined, c.signal, QUERY_TIMEOUT_MS);
      setTraces((prev) => [...(before ? (prev ?? []) : []), ...(page.traces ?? [])]);
      setNext(page.next || undefined);
      if (page.truncated) setTruncated(true);
    } catch (err) {
      if (!c.signal.aborted && isShown(err)) setError(err);
    } finally {
      if (ctrl.current === c) setBusy(false);
    }
  }

  useEffect(() => {
    if (!allowed) return;
    const w = rangeEnding(range);
    setWin(w);
    setOpen(undefined);
    void read(w, undefined);
    return () => ctrl.current?.abort();
  }, [id, range, errorsOnly, minDuration, allowed]);

  const why = notAvailableReason(error);
  return (
    <section className="section" aria-labelledby="obs-traces" aria-busy={busy}>
      <div className="pipeline-head">
        <h2 id="obs-traces" className="section-title">
          Traces in {envName}
        </h2>
        {allowed && !why && (
          <button type="button" className="button" disabled={busy} onClick={() => (setWin(rangeEnding(range)), void read(rangeEnding(range), undefined))}>
            {busy ? "Reading…" : "Refresh"}
          </button>
        )}
      </div>
      {allowed === false ? (
        <p className="state-body">Traces can hold sensitive data, so reading them needs its own permission (the Trace Reader role), which your roles here don't include.</p>
      ) : allowed === undefined ? (
        <LoadingState what="your access" />
      ) : why ? (
        <NotAvailable why={why} />
      ) : (
        <>
          <div className="form-row">
            <SelectField id="obs-min-duration" label="Duration" value={minDuration} onChange={(e) => setMinDuration(e.target.value)} options={minDurations} />
            <label className="checkbox">
              <input type="checkbox" checked={errorsOnly} onChange={(e) => setErrorsOnly(e.target.checked)} />
              Only traces with an error
            </label>
          </div>
          <p className="field-hint">
            Traces with a span from this component, newest first. A component sends traces once it's redeployed after the tracing package is installed: it's given a standard OpenTelemetry endpoint (OTEL_EXPORTER_OTLP_ENDPOINT).
          </p>
          {error ? (
            <RequestError error={error} what="traces" />
          ) : !traces ? (
            <LoadingState what="traces" />
          ) : traces.length === 0 ? (
            <p className="state-body">No traces in this range. If the component should send them, redeploy it so it's given the endpoint.</p>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">Started</th>
                    <th scope="col">Root span</th>
                    <th scope="col">Duration</th>
                    <th scope="col">Spans</th>
                    <th scope="col">Result</th>
                    <th scope="col">
                      <span className="visually-hidden">Open</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {traces.map((t) => (
                    <tr key={t.traceId}>
                      <td>{new Date(t.start).toLocaleString()}</td>
                      <td className="mono">{t.root ?? "(root not stored)"}</td>
                      <td>{durationText(t.durationNs)}</td>
                      <td>{t.spans}</td>
                      <td>{t.hasErrors ? <StatusText kind="error">Error</StatusText> : <StatusText kind="ok">OK</StatusText>}</td>
                      <td>
                        <button type="button" className="button" aria-expanded={open === t.traceId} onClick={() => setOpen(open === t.traceId ? undefined : t.traceId)}>
                          {open === t.traceId ? "Close" : "Open"}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {truncated && <Notice>A limit on the read was reached: some traces aren't listed.</Notice>}
          {next && !error && (
            <div className="form-actions">
              <button type="button" className="button" disabled={busy} onClick={() => void read(win, next)}>
                {busy ? "Reading…" : "Show older traces"}
              </button>
            </div>
          )}
          {open && <TraceDetail key={open} deployment={id} traceId={open} />}
        </>
      )}
      <ApiEquivalent request={{ method: "GET", path: `/v1/deployments/${id}/traces?${query(win)}` }} />
    </section>
  );
}

/** Bar geometry inside the waterfall's SVG, in its own units. */
const BAR_H = 14;

/** TraceDetail: one trace's spans as a waterfall, and a chosen span's attributes. */
function TraceDetail({ deployment, traceId }: { deployment: string; traceId: string }) {
  const names = useNames();
  const [trace, setTrace] = useState<TraceView>();
  const [error, setError] = useState<ApiError | NetworkError>();
  const [chosen, setChosen] = useState<string>();
  useEffect(() => {
    const c = new AbortController();
    apiFetch<TraceView>("GET", `/v1/deployments/${enc(deployment)}/traces/${enc(traceId)}`, undefined, c.signal, QUERY_TIMEOUT_MS)
      .then(setTrace)
      .catch((err: unknown) => {
        if (!c.signal.aborted && isShown(err)) setError(err);
      });
    return () => c.abort();
  }, [deployment, traceId]);

  const path = `/v1/deployments/${deployment}/traces/${traceId}`;
  if (error) return notAvailableReason(error) ? <NotAvailable why={notAvailableReason(error)!} /> : <RequestError error={error} what="the trace" />;
  if (!trace) return <LoadingState what="the trace" />;
  const { rows, totalNs } = waterfall(trace.spans ?? []);
  const span: Span | undefined = rows.find((r) => r.span.spanId === chosen)?.span;
  return (
    <div className="panel" aria-labelledby="trace-title">
      <h3 id="trace-title" className="subsection-title mono">
        Trace {traceId}
      </h3>
      <p className="field-hint">
        {rows.length} {rows.length === 1 ? "span" : "spans"} over {durationText(totalNs)}. Each bar is a span, placed by when it started; nested spans are indented under the span that started them.
      </p>
      {trace.partial && <Notice>Some spans are from components your roles don't let you read, and aren't shown.</Notice>}
      {trace.truncated && <Notice>The trace has more spans than one read returns: some aren't shown.</Notice>}
      {rows.length === 0 ? (
        <p className="state-body">No spans of this trace are stored.</p>
      ) : (
        <div className="table-wrap">
          <table className="table waterfall">
            <thead>
              <tr>
                <th scope="col">Span</th>
                <th scope="col">Component</th>
                <th scope="col">Duration</th>
                <th scope="col" className="waterfall-col">
                  Timeline
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map(({ span: s, depth, offset, width }) => (
                <tr key={s.spanId} aria-selected={s.spanId === chosen}>
                  <td>
                    <button type="button" className={`link-button mono waterfall-depth-${Math.min(depth, 12)}`} onClick={() => setChosen(s.spanId === chosen ? undefined : s.spanId)}>
                      {s.name}
                    </button>
                    {s.failed && (
                      <>
                        {" "}
                        <StatusText kind="error">Error</StatusText>
                      </>
                    )}
                  </td>
                  <td>{names.comps.get(s.component) ?? s.component}</td>
                  <td>{durationText(s.durationNs)}</td>
                  <td className="waterfall-col">
                    <svg className="waterfall-track" viewBox={`0 0 100 ${BAR_H}`} preserveAspectRatio="none" role="img" aria-label={`starts ${durationText((offset / 100) * totalNs)} in, lasts ${durationText(s.durationNs)}`}>
                      <rect className={s.failed ? "waterfall-bar waterfall-bar--failed" : "waterfall-bar"} x={offset} y={2} width={Math.max(width, 0.3)} height={BAR_H - 4} />
                    </svg>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {span && (
        <section aria-label={`Span ${span.name}`}>
          <h4 className="subsection-title mono">{span.name}</h4>
          <KeyValueList
            items={[
              ["Started", new Date(span.start).toLocaleString()],
              ["Duration", durationText(span.durationNs)],
              ["Kind", span.kind ?? ""],
              ["Status", span.status ?? (span.failed ? "error" : "")],
              ["Component", names.comps.get(span.component) ?? span.component],
              ["Environment", names.envs.get(span.environment) ?? span.environment],
              ...Object.entries(span.attributes ?? {})
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([k, v]): [string, string] => [k, v]),
            ]}
          />
        </section>
      )}
      <ApiEquivalent request={{ method: "GET", path }} />
    </div>
  );
}
