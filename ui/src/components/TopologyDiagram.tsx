import type { TopologyView, TrafficEdge } from "../api/types";
import { trafficLevel } from "../lib/networking";

const COL = 220;
const ROW = 64;
const BOX_W = 168;
const BOX_H = 40;
const PAD = 8;

/**
 * Columns from callers to callees: a Component sits one column right of the
 * furthest Component that calls it. A cycle can't push anything further
 * than the number of Components.
 */
export function layers(view: TopologyView): Map<string, number> {
  const level = new Map(view.components.map((c) => [c.id, 0]));
  for (let i = 0; i < view.components.length; i++) {
    let moved = false;
    for (const d of view.dependencies) {
      const from = level.get(d.from);
      const to = level.get(d.to);
      if (from === undefined || to === undefined || to > from || from + 1 >= view.components.length) continue;
      level.set(d.to, from + 1);
      moved = true;
    }
    if (!moved) break;
  }
  return level;
}

const short = (s: string) => (s.length > 20 ? `${s.slice(0, 19)}…` : s);

/**
 * TopologyDiagram draws an Application's declared calls between its
 * Components (spec 020). With traffic (spec 025's overlay), each call that
 * was observed is labelled with its request rate and failures and drawn in
 * a status colour; an observed call nobody declared is drawn dashed, and a
 * declared one with no traffic stays plain. It's a picture of the tables
 * beside it, which carry the same facts for screen readers.
 */
export function TopologyDiagram({ view, label, traffic }: { view: TopologyView; label: string; traffic?: TrafficEdge[] | undefined }) {
  // Calls observed but not declared are drawn too, once per pair of ends.
  const observed = new Map((traffic ?? []).map((t) => [`${t.from}>${t.to}`, t]));
  const declared = new Set(view.dependencies.map((d) => `${d.from}>${d.to}`));
  const seen = new Set<string>();
  const edges = [
    ...view.dependencies.flatMap((d) => {
      const k = `${d.from}>${d.to}`;
      if (traffic && seen.has(k)) return [];
      seen.add(k);
      return [{ from: d.from, to: d.to, key: `${d.from}-${d.to}-${d.port}`, observedOnly: false }];
    }),
    ...(traffic ?? []).filter((t) => !declared.has(`${t.from}>${t.to}`)).map((t) => ({ from: t.from, to: t.to, key: `obs-${t.from}-${t.to}`, observedOnly: true })),
  ];
  const level = layers({ ...view, dependencies: edges.map((e) => ({ from: e.from, to: e.to, port: "", env: "" })) });
  const rows = new Map<number, number>();
  const pos = new Map<string, { x: number; y: number }>();
  for (const c of view.components) {
    const l = level.get(c.id) ?? 0;
    const r = rows.get(l) ?? 0;
    rows.set(l, r + 1);
    pos.set(c.id, { x: PAD + l * COL, y: PAD + r * ROW });
  }
  const width = PAD * 2 + (Math.max(0, ...level.values()) * COL + BOX_W);
  const height = PAD * 2 + Math.max(1, ...rows.values()) * ROW - (ROW - BOX_H);
  return (
    <div className="table-wrap">
      <svg className="topology" viewBox={`0 0 ${width} ${height}`} width={width} height={height} role="img" aria-label={label}>
        <defs>
          <marker id="topology-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0 0 10 5 0 10z" className="topology-arrow" />
          </marker>
        </defs>
        {edges.map((d) => {
          const a = pos.get(d.from);
          const b = pos.get(d.to);
          if (!a || !b) return null;
          const x1 = a.x + BOX_W;
          const y1 = a.y + BOX_H / 2;
          const x2 = b.x;
          const y2 = b.y + BOX_H / 2;
          const bend = Math.max(40, Math.abs(x2 - x1) / 2);
          const t = observed.get(`${d.from}>${d.to}`);
          const level = t && trafficLevel(t);
          const cls = ["topology-edge", level ? `topology-edge--${level.kind}` : "", d.observedOnly ? "topology-edge--undeclared" : ""].filter(Boolean).join(" ");
          // The label sits at the curve's middle, which for this cubic is the chord's midpoint.
          return (
            <g key={d.key}>
              <path className={cls} d={`M${x1} ${y1} C${x1 + bend} ${y1} ${x2 - bend} ${y2} ${x2} ${y2}`} markerEnd="url(#topology-arrow)" />
              {level && (
                <text className={`topology-edge-label topology-edge-label--${level.kind}`} x={(x1 + x2) / 2} y={(y1 + y2) / 2 - 6} textAnchor="middle">
                  {level.label}
                </text>
              )}
            </g>
          );
        })}
        {view.components.map((c) => {
          const p = pos.get(c.id)!;
          return (
            <g key={c.id}>
              <title>{c.name}</title>
              <rect className="topology-node" x={p.x} y={p.y} width={BOX_W} height={BOX_H} rx={4} />
              <text className="topology-label" x={p.x + 12} y={p.y + BOX_H / 2} dominantBaseline="central">
                {short(c.name)}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}
