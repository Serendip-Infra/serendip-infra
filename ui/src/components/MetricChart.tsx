import { useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import type { MetricPoint } from "../api/types";

/**
 * MetricChart draws one measured series over a time range as a line
 * (spec 023, following the dataviz method): a 2 px line with a 10% wash,
 * hairline grid, and the request and limit as labelled reference lines on
 * the same axis. One series, so no legend: the title names it. A
 * crosshair snaps to the nearest sample on hover, and the arrow keys move
 * it when the chart has focus. Every value is also in the table view.
 * Nothing is drawn that wasn't measured: no samples is said in words.
 */
export function MetricChart({
  id,
  title,
  points,
  from,
  to,
  request,
  limit,
  format,
}: {
  id: string;
  /** What's plotted and its unit, e.g. "CPU, millicores". */
  title: string;
  points: MetricPoint[];
  /** The range shown, RFC 3339: gaps in the samples show as gaps. */
  from: string;
  to: string;
  request?: number | undefined;
  limit?: number | undefined;
  /** A value with its unit, e.g. "120m" or "256 MiB". */
  format: (v: number) => string;
}) {
  const box = useRef<HTMLDivElement>(null);
  const width = useWidth(box);
  const [active, setActive] = useState<number>();
  const samples = points
    .map((p) => ({ t: Date.parse(p.time), v: p.value }))
    .filter((p) => Number.isFinite(p.t) && Number.isFinite(p.v))
    .sort((a, b) => a.t - b.t);

  const t0 = Date.parse(from);
  const t1 = Date.parse(to);
  if (samples.length === 0 || !(t1 > t0)) {
    return (
      <figure className="chart" aria-labelledby={`${id}-title`}>
        <figcaption id={`${id}-title`} className="chart-title">
          {title}
        </figcaption>
        <p className="state-body">No samples in this range.</p>
      </figure>
    );
  }

  const left = 56;
  const right = 16;
  const top = 12;
  const bottom = 28;
  const height = HEIGHT;
  const plotW = Math.max(40, width - left - right);
  const plotH = height - top - bottom;

  const peak = Math.max(...samples.map((s) => s.v), request ?? 0, limit ?? 0);
  const { max: yMax, step: yStep } = niceScale(peak);
  const x = (t: number) => left + ((t - t0) / (t1 - t0)) * plotW;
  const y = (v: number) => top + plotH - (v / yMax) * plotH;
  const baseline = top + plotH;

  // A gap of more than three sample steps breaks the line: no data there.
  const step = typicalStep(samples.map((s) => s.t));
  const segments: Array<typeof samples> = [];
  for (const s of samples) {
    const last = segments[segments.length - 1];
    const prev = last?.[last.length - 1];
    if (!last || !prev || (step > 0 && s.t - prev.t > 3 * step)) segments.push([s]);
    else last.push(s);
  }
  const linePath = segments.map((seg) => seg.map((s, i) => `${i === 0 ? "M" : "L"}${r(x(s.t))},${r(y(s.v))}`).join("")).join("");
  const areaPath = segments
    .filter((seg) => seg.length > 1)
    .map((seg) => `M${r(x(seg[0]!.t))},${baseline}${seg.map((s) => `L${r(x(s.t))},${r(y(s.v))}`).join("")}L${r(x(seg[seg.length - 1]!.t))},${baseline}Z`)
    .join("");

  const yTicks: number[] = [];
  for (let v = 0; v <= yMax + yStep / 2; v += yStep) yTicks.push(v);
  const xTicks = timeTicks(t0, t1, Math.max(2, Math.min(6, Math.floor(plotW / 110))));
  const refs = referenceLines(request, limit, y);

  const last = samples[samples.length - 1]!;
  const peakSample = samples.reduce((a, b) => (b.v > a.v ? b : a));
  const cur = active === undefined ? undefined : samples[Math.min(active, samples.length - 1)];

  function nearest(px: number): number {
    const t = t0 + ((px - left) / plotW) * (t1 - t0);
    let lo = 0;
    let hi = samples.length - 1;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (samples[mid]!.t < t) lo = mid + 1;
      else hi = mid;
    }
    return lo > 0 && t - samples[lo - 1]!.t < samples[lo]!.t - t ? lo - 1 : lo;
  }

  function onMove(e: PointerEvent<SVGSVGElement>) {
    const rect = e.currentTarget.getBoundingClientRect();
    const scale = rect.width > 0 ? width / rect.width : 1;
    setActive(nearest((e.clientX - rect.left) * scale));
  }

  function onKey(e: KeyboardEvent<SVGSVGElement>) {
    const i = active ?? samples.length - 1;
    const next =
      e.key === "ArrowLeft" ? Math.max(0, i - 1) : e.key === "ArrowRight" ? Math.min(samples.length - 1, i + 1) : e.key === "Home" ? 0 : e.key === "End" ? samples.length - 1 : undefined;
    if (next === undefined) {
      if (e.key === "Escape") setActive(undefined);
      return;
    }
    e.preventDefault();
    setActive(next);
  }

  const tip = cur && tooltip(x(cur.t), y(cur.v), left, left + plotW, top);

  return (
    <figure className="chart" aria-labelledby={`${id}-title`}>
      <figcaption id={`${id}-title`} className="chart-title">
        {title}
      </figcaption>
      <p className="chart-meta">
        Latest {format(last.v)}, highest {format(peakSample.v)}
        {request !== undefined && `, request ${format(request)}`}
        {limit !== undefined && `, limit ${format(limit)}`}.
      </p>
      <div ref={box} className="chart-box">
        <svg
          className="chart-svg"
          width={width}
          height={height}
          viewBox={`0 0 ${width} ${height}`}
          role="img"
          aria-label={`${title}: ${samples.length} samples from ${fmtTime(t0, t1 - t0)} to ${fmtTime(t1, t1 - t0)}. Latest ${format(last.v)}, highest ${format(peakSample.v)}. Use the arrow keys to read samples.`}
          tabIndex={0}
          onPointerMove={onMove}
          onPointerLeave={() => setActive(undefined)}
          onKeyDown={onKey}
          onBlur={() => setActive(undefined)}
        >
          {yTicks.map((v) => (
            <g key={`y${v}`}>
              <line className="chart-grid" x1={left} x2={left + plotW} y1={r(y(v))} y2={r(y(v))} />
              <text className="chart-axis" x={left - 8} y={r(y(v))} textAnchor="end" dominantBaseline="middle">
                {format(v)}
              </text>
            </g>
          ))}
          {xTicks.map((t) => (
            <text key={`x${t}`} className="chart-axis" x={r(x(t))} y={height - 8} textAnchor={x(t) > left + plotW - 40 ? "end" : "middle"}>
              {fmtTime(t, t1 - t0)}
            </text>
          ))}
          {areaPath && <path className="chart-area" d={areaPath} />}
          <path className="chart-line" d={linePath} />
          {refs.map((ref) => (
            <g key={ref.key}>
              <line className={`chart-ref chart-ref--${ref.key}`} x1={left} x2={left + plotW} y1={r(ref.y)} y2={r(ref.y)} />
              <text className="chart-ref-label" x={left + plotW} y={r(ref.labelY - 5)} textAnchor="end">
                {ref.label}
              </text>
            </g>
          ))}
          {/* A lone sample, or the latest, gets its dot; the line alone can't show one point. */}
          {segments
            .filter((seg) => seg.length === 1)
            .map(([s]) => (
              <circle key={`p${s!.t}`} className="chart-dot" cx={r(x(s!.t))} cy={r(y(s!.v))} r={4} />
            ))}
          {!cur && <circle className="chart-dot" cx={r(x(last.t))} cy={r(y(last.v))} r={4} />}
          {cur && tip && (
            <g className="chart-hover" aria-hidden="true">
              <line className="chart-crosshair" x1={r(x(cur.t))} x2={r(x(cur.t))} y1={top} y2={baseline} />
              <circle className="chart-dot" cx={r(x(cur.t))} cy={r(y(cur.v))} r={4} />
              <rect className="chart-tip" x={tip.x} y={tip.y} width={TIP_W} height={TIP_H} rx={4} />
              <text className="chart-tip-value" x={tip.x + 10} y={tip.y + 18}>
                {format(cur.v)}
              </text>
              <text className="chart-tip-label" x={tip.x + 10} y={tip.y + 35}>
                {fmtTime(cur.t, 0)}
              </text>
            </g>
          )}
        </svg>
      </div>
      <p className="visually-hidden" aria-live="polite">
        {cur ? `${format(cur.v)} at ${fmtTime(cur.t, 0)}` : ""}
      </p>
      <details className="chart-table">
        <summary>Show as a table</summary>
        <div className="table-wrap">
          <table className="table">
            <caption className="visually-hidden">{title}, by time</caption>
            <thead>
              <tr>
                <th scope="col">Time</th>
                <th scope="col">Value</th>
              </tr>
            </thead>
            <tbody>
              {samples.map((s) => (
                <tr key={s.t}>
                  <td>
                    <time dateTime={new Date(s.t).toISOString()}>{fmtTime(s.t, 0)}</time>
                  </td>
                  <td className="chart-num">{format(s.v)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </figure>
  );
}

const HEIGHT = 220;
const TIP_W = 150;
const TIP_H = 44;
const DEFAULT_WIDTH = 640;

const r = (v: number) => Math.round(v * 10) / 10;

/** The box's width, followed as it resizes; a default where it can't be measured (tests). */
function useWidth(ref: React.RefObject<HTMLDivElement | null>): number {
  const [w, setW] = useState(DEFAULT_WIDTH);
  // Measured before the first paint, so the chart never draws at the wrong width.
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && el.clientWidth > 0) setW(el.clientWidth);
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(([entry]) => {
      const cw = Math.floor(entry?.contentRect.width ?? 0);
      if (cw > 0) setW(cw);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return w;
}

/** A y-axis from 0 to a clean number at or above peak, in about 4 steps of 1, 2, 2.5 or 5 × 10ⁿ. */
export function niceScale(peak: number): { max: number; step: number } {
  if (!(peak > 0)) return { max: 1, step: 0.25 };
  const raw = peak / 4;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) ?? 10 * mag;
  return { max: Math.ceil(peak / step) * step, step };
}

/** The usual time between samples (the median gap), 0 for fewer than two. */
function typicalStep(ts: number[]): number {
  const gaps = ts.slice(1).map((t, i) => t - ts[i]!).sort((a, b) => a - b);
  return gaps.length ? gaps[Math.floor(gaps.length / 2)]! : 0;
}

/** About n ticks on whole minutes, hours or days between t0 and t1. */
function timeTicks(t0: number, t1: number, n: number): number[] {
  const min = 60_000;
  const steps = [1, 2, 5, 10, 15, 30, 60, 120, 180, 360, 720, 1440, 2880].map((m) => m * min);
  const step = steps.find((s) => (t1 - t0) / s <= n) ?? steps[steps.length - 1]!;
  // Whole steps in local time, so ticks land on 10:00, not 10:07.
  const offset = new Date(t0).getTimezoneOffset() * min;
  const out: number[] = [];
  for (let t = Math.ceil((t0 - offset) / step) * step + offset; t <= t1; t += step) out.push(t);
  return out;
}

/** A time for an axis or a reading: with the day once the range covers more than one. */
export function fmtTime(t: number, range: number): string {
  const d = new Date(t);
  const time = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  if (range > 0 && range <= 24 * 3600_000) return time;
  return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} ${time}`;
}

/**
 * Request and limit lines, named at the plot's right end (their values are in the
 * line above the chart, the tooltip's axis and the table); one name when
 * they're equal.
 */
function referenceLines(request: number | undefined, limit: number | undefined, y: (v: number) => number) {
  const out: Array<{ key: "request" | "limit"; y: number; labelY: number; label: string }> = [];
  if (request !== undefined && limit !== undefined && request === limit) {
    return [{ key: "limit" as const, y: y(limit), labelY: y(limit), label: "Request and limit" }];
  }
  if (limit !== undefined) out.push({ key: "limit", y: y(limit), labelY: y(limit), label: "Limit" });
  if (request !== undefined) out.push({ key: "request", y: y(request), labelY: y(request), label: "Request" });
  // Names sit just above their lines; when the lines are closer than a
  // name's height, the request's goes below its line instead.
  if (out.length === 2 && Math.abs(out[0]!.labelY - out[1]!.labelY) < 16) out[1]!.labelY += 19;
  return out;
}

/** Where the tooltip box goes: right of the crosshair, or left of it near the right edge. */
function tooltip(cx: number, cy: number, minX: number, maxX: number, top: number) {
  const xPos = cx + 12 + TIP_W <= maxX ? cx + 12 : Math.max(minX, cx - 12 - TIP_W);
  const yPos = Math.max(top, Math.min(cy - TIP_H / 2, HEIGHT - 28 - TIP_H));
  return { x: xPos, y: yPos };
}
