import { useState } from "react";
import { apiFetch, apiList } from "../api/client";
import type { Application, Environment, Project, TopologyView, TrafficView } from "../api/types";
import { ApiEquivalent } from "../components/ApiEquivalent";
import { AvailabilityGate } from "../components/Availability";
import { SelectField } from "../components/Field";
import { RequestError } from "../components/RequestError";
import { PageHeader } from "../components/Shell";
import { EmptyState, LoadingState } from "../components/States";
import { StatusText } from "../components/Status";
import { TopologyDiagram } from "../components/TopologyDiagram";
import { useOptional } from "../hooks/useOptional";
import { usePoll, useNow } from "../hooks/usePoll";
import { availability } from "../lib/health";
import { errorPercent, notAvailableReason, percentText, perSecond, trafficLevel } from "../lib/networking";
import { orderEnvironments, ownerOf } from "../lib/pipeline";
import { Link } from "../lib/router";
import { UpdatedLine } from "./common";
import { NotAvailable } from "./Networking";
import { RangeField, rangeEnding, useQuery } from "./Observability";

const enc = encodeURIComponent;

const levelWord = { ok: "No failures", warn: "Some failures", error: "Failing" } as const;

/**
 * ApplicationTopology: an Application's Components and the calls they
 * declare to each other (spec 020), with spec 025's live traffic overlay:
 * the requests observed between them in one Environment, when turned on.
 */
export function ApplicationTopology({ id }: { id: string }) {
  const now = useNow();
  const app = usePoll((s) => apiFetch<Application>("GET", `/v1/applications/${enc(id)}`, undefined, s), 15_000, `topo-app-${id}`);
  const topo = usePoll((s) => apiFetch<TopologyView>("GET", `/v1/applications/${enc(id)}/topology`, undefined, s), 15_000, `topo-${id}`);
  const traffic = useOptional("observability");
  const appPath = `/applications/${enc(id)}`;
  const projectId = app.data ? ownerOf(app.data.metadata, "Project") : undefined;
  const envs = usePoll(
    async (s) => {
      if (!projectId) return [];
      const [project, list] = await Promise.all([apiFetch<Project>("GET", `/v1/projects/${enc(projectId)}`, undefined, s), apiList<Environment>(`/v1/environments?ownerId=${enc(projectId)}`, s)]);
      return orderEnvironments(project.spec.pipeline, list).map(({ env }) => env);
    },
    60_000,
    `topo-envs-${projectId ?? ""}`,
  );
  const [overlay, setOverlay] = useState(false);
  const [env, setEnv] = useState<string>();
  const [range, setRange] = useState("1h");
  // The range ends when it's read; a new choice or Refresh reads it again.
  const [w, setW] = useState(() => ({ range, ...rangeEnding(range) }));
  if (w.range !== range) setW({ range, ...rangeEnding(range) });
  const ready = traffic.loaded && availability(traffic.readiness) === "ready";
  const currentEnv = envs.data?.find((e) => e.metadata.id === env) ?? envs.data?.[0];
  const query = currentEnv ? `environment=${enc(currentEnv.metadata.id)}&from=${enc(w.from)}&to=${enc(w.to)}` : "";
  const trafficPath = overlay && ready && currentEnv ? `/v1/applications/${enc(id)}/traffic?${query}` : undefined;
  const live = useQuery<TrafficView>(trafficPath, trafficPath ?? "off");
  const why = notAvailableReason(live.error);
  const edges = trafficPath && live.data && !live.error ? (live.data.edges ?? []) : undefined;
  const name = (cid: string) => topo.data?.components.find((c) => c.id === cid)?.name ?? cid;

  return (
    <>
      <PageHeader
        crumbs={[
          { label: "Applications", to: "/applications" },
          { label: app.data?.metadata.name ?? "Application", to: appPath },
        ]}
        title="Topology"
        meta={<UpdatedLine at={topo.updatedAt} now={now} />}
      />
      <p>
        The calls each component declares to others in this application.
        {edges
          ? " With live traffic on, each observed call is labelled with its requests per second and the share that failed; a dashed line is traffic nobody declared."
          : " This is what's declared, not traffic that was observed."}
      </p>
      {topo.error ? (
        <RequestError error={topo.error} what="topology" />
      ) : !topo.data ? (
        <LoadingState what="the topology" />
      ) : topo.data.components.length === 0 ? (
        <EmptyState title="No components yet">Add components to the application to see how they call each other.</EmptyState>
      ) : (
        <>
          <TopologyDiagram
            view={topo.data}
            traffic={edges}
            label={`${topo.data.components.length} components and ${topo.data.dependencies.length} declared calls${edges ? `, with ${edges.length} observed` : ""}, listed in the tables below`}
          />
          <section className="section" aria-labelledby="topo-calls">
            <h2 id="topo-calls" className="section-title">
              Declared calls
            </h2>
            {topo.data.dependencies.length === 0 ? (
              <p className="state-body">No component declares a call to another.</p>
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th scope="col">From</th>
                      <th scope="col">To</th>
                      <th scope="col">Port</th>
                      <th scope="col">Address in</th>
                    </tr>
                  </thead>
                  <tbody>
                    {topo.data.dependencies.map((d) => (
                      <tr key={`${d.from}-${d.to}-${d.port}`}>
                        <td>
                          <Link to={`${appPath}/components/${enc(d.from)}`}>{name(d.from)}</Link>
                        </td>
                        <td>
                          <Link to={`${appPath}/components/${enc(d.to)}`}>{name(d.to)}</Link>
                        </td>
                        <td>{d.port}</td>
                        <td className="mono">{d.env}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      )}
      {traffic.loaded && !ready && <AvailabilityGate title="Live traffic" id="topo-traffic" readiness={traffic.readiness} packageName={traffic.packageName} what="live traffic between components" />}
      {ready && (
        <section className="section" aria-labelledby="topo-traffic" aria-busy={live.loading}>
          <div className="pipeline-head">
            <h2 id="topo-traffic" className="section-title">
              Live traffic
            </h2>
            {trafficPath && !why && (
              <button type="button" className="button" disabled={live.loading} onClick={() => setW({ range, ...rangeEnding(range) })}>
                {live.loading ? "Reading…" : "Refresh"}
              </button>
            )}
          </div>
          {!projectId && app.data ? (
            <p className="state-body">This application isn't in a Project, so it has no Environments to read traffic in.</p>
          ) : envs.error ? (
            <RequestError error={envs.error} what="environments" />
          ) : !envs.data || !currentEnv ? (
            envs.data ? <p className="state-body">The Project has no Environments yet.</p> : <LoadingState what="environments" />
          ) : (
            <>
              <div className="form-row">
                <label className="checkbox">
                  <input type="checkbox" checked={overlay} onChange={(e) => setOverlay(e.target.checked)} />
                  Show live traffic on the diagram
                </label>
                {overlay && (
                  <>
                    <SelectField id="topo-env" label="Environment" value={currentEnv.metadata.id} onChange={(e) => setEnv(e.target.value)} options={envs.data.map((e) => ({ value: e.metadata.id, label: e.metadata.name }))} />
                    <RangeField id="topo-range" value={range} onChange={setRange} />
                  </>
                )}
              </div>
              {!overlay ? (
                <p className="field-hint">HTTP requests between this application's components, as the cluster's network layer saw them, averaged over a range.</p>
              ) : why ? (
                <NotAvailable why={why} />
              ) : live.error ? (
                <RequestError error={live.error} what="traffic" />
              ) : !live.data ? (
                <LoadingState what="traffic" />
              ) : (
                <>
                  {live.data.omitted > 0 && (
                    <p className="field-hint">
                      {live.data.omitted === 1 ? "1 component isn't shown" : `${live.data.omitted} components aren't shown`}: your roles don't allow reading their metrics, so calls to or from them aren't either.
                    </p>
                  )}
                  {(live.data.edges ?? []).length === 0 ? (
                    <p className="state-body">No HTTP requests between these components in this range.</p>
                  ) : (
                    <div className="table-wrap">
                      <table className="table">
                        <thead>
                          <tr>
                            <th scope="col">From</th>
                            <th scope="col">To</th>
                            <th scope="col">Requests</th>
                            <th scope="col">Failed (5xx)</th>
                            <th scope="col">Status</th>
                          </tr>
                        </thead>
                        <tbody>
                          {(live.data.edges ?? []).map((e) => {
                            const level = trafficLevel(e);
                            const p = errorPercent(e);
                            return (
                              <tr key={`${e.from}-${e.to}`}>
                                <td>
                                  <Link to={`${appPath}/components/${enc(e.from)}`}>{name(e.from)}</Link>
                                </td>
                                <td>
                                  <Link to={`${appPath}/components/${enc(e.to)}`}>{name(e.to)}</Link>
                                </td>
                                <td>{perSecond(e.rate)}</td>
                                <td>
                                  {perSecond(e.errorRate)}
                                  {p !== undefined && ` (${percentText(p)})`}
                                </td>
                                <td>
                                  <StatusText kind={level.kind}>{levelWord[level.kind]}</StatusText>
                                </td>
                              </tr>
                            );
                          })}
                        </tbody>
                      </table>
                    </div>
                  )}
                  <p className="field-hint">Failing means 5% of requests or more. Only calls whose two ends you can read are shown.</p>
                </>
              )}
              {overlay && <ApiEquivalent request={{ method: "GET", path: `/v1/applications/${id}/traffic?environment=${currentEnv.metadata.id}&from=${w.from}&to=${w.to}` }} />}
            </>
          )}
        </section>
      )}
      <ApiEquivalent request={{ method: "GET", path: `/v1/applications/${id}/topology` }} />
    </>
  );
}
