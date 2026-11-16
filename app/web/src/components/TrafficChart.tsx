"use client";

import { t } from "../lib/i18n";
import { useEffect, useMemo, useRef, useState } from "react";
import type { Usage } from "../lib/api";
import type { PollRate } from "../lib/rates";
import { formatBytes } from "../lib/util";
import { usePoll } from "./bits";

const H = 190;
const PAD = { top: 12, right: 10, bottom: 24, left: 64 };
type PlotPoint = { t: string; at: number; send: number; recv: number };

/** Range changes hide the previous series immediately and discard its pending responses. */
export function useTrafficUsage(load: () => Promise<Usage | null>, rate: PollRate, deps: unknown[]) {
  const selection = useMemo(() => ({}), deps);
  const current = useRef(selection);
  current.current = selection;
  const request = useRef(0);
  const [result, setResult] = useState<{ selection: object; usage: Usage | null } | null>(null);
  usePoll(async () => {
    const id = ++request.current;
    const usage = await load().catch(() => null);
    if (current.current === selection && request.current === id) setResult({ selection, usage });
  }, rate, deps);
  return result && result.selection === selection ? result.usage : null;
}

/** Recorded intervals only: missing samples break the paths rather than being interpolated. */
export function TrafficChart({ usage, emptyLabel }: { usage: Usage; emptyLabel?: string }) {
  const [hover, setHover] = useState<number | null>(null);
  const [width, setWidth] = useState(720);
  const holder = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = holder.current;
    if (!el) return;
    const observer = new ResizeObserver((entries) => {
      const w = Math.round(entries[0]?.contentRect.width ?? 0);
      if (w > 80) setWidth(w);
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const model = useMemo(() => {
    const t1 = Date.parse(usage.end ?? usage.points[usage.points.length - 1]?.t ?? "");
    const t0 = usage.start ? Date.parse(usage.start) : t1 - usage.hours * 3600_000;
    if (!Number.isFinite(t0) || !Number.isFinite(t1) || t1 <= t0) return null;
    const innerW = width - PAD.left - PAD.right;
    const innerH = H - PAD.top - PAD.bottom;
    const maxGap = (usage.sample_interval_seconds ?? 300) * 1500;
    const segments: PlotPoint[][] = [];
    let segment: PlotPoint[] = [];
    for (const p of usage.points) {
      const at = Date.parse(p.t);
      if (p.send === null || p.recv === null || !Number.isFinite(p.send) || !Number.isFinite(p.recv) || p.send < 0 || p.recv < 0 || !Number.isFinite(at) || at < t0 || at > t1) {
        segment = [];
        continue;
      }
      const previous = segment[segment.length - 1];
      if (previous && (at <= previous.at || at - previous.at > maxGap)) segment = [];
      if (!segment.length) segments.push(segment);
      segment.push({ t: p.t, at, send: p.send, recv: p.recv });
    }
    const points = segments.flat();
    if (!points.length) return null;
    const max = points.reduce((top, p) => Math.max(top, p.send, p.recv), 1);
    const x = (at: number) => PAD.left + ((at - t0) / (t1 - t0)) * innerW;
    const y = (value: number) => PAD.top + innerH - (value / max) * innerH;
    const path = (run: PlotPoint[], key: "send" | "recv") =>
      run.map((p, i) => `${i ? "L" : "M"}${x(p.at).toFixed(1)},${y(p[key]).toFixed(1)}`).join("");
    const line = (key: "send" | "recv") => segments.map(run => path(run, key)).join("");
    const area = (key: "send" | "recv") => segments.filter(run => run.length > 1).map(run =>
      `${path(run, key)}L${x(run[run.length - 1].at).toFixed(1)},${PAD.top + innerH}L${x(run[0].at).toFixed(1)},${PAD.top + innerH}Z`,
    ).join("");
    const ticks = [0.25, 0.5, 0.75, 1].map(f => ({ v: max * f, y: y(max * f) }));
    return { x, y, line, area, ticks, innerW, innerH, t0, t1, points, segments };
  }, [usage, width]);

  if (!model) {
    return (
      <div className="chart chart--empty micro" ref={holder}>
        {t(emptyLabel ?? "Not enough samples yet — the panel records traffic every few minutes.")}
      </div>
    );
  }

  const formatTime = (at: number, tooltip = false) => {
    const date = new Date(at);
    const day = usage.hours >= 24 ? date.toLocaleDateString(undefined, { month: "numeric", day: "numeric" }) : "";
    const clock = tooltip || usage.hours <= 24 ? date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }) : "";
    return [day, clock].filter(Boolean).join(" ");
  };
  const onMove = (event: React.PointerEvent) => {
    const svg = holder.current?.querySelector("svg");
    if (!svg) return;
    const rect = svg.getBoundingClientRect();
    const px = (event.clientX - rect.left) * width / rect.width;
    const at = model.t0 + (px - PAD.left) / model.innerW * (model.t1 - model.t0);
    if (!model.segments.some(run => run.length === 1 ? Math.abs(model.x(run[0].at) - px) <= 4 : at >= run[0].at && at <= run[run.length - 1].at)) {
      setHover(null);
      return;
    }
    let best = 0;
    let distance = Infinity;
    model.points.forEach((point, index) => {
      const next = Math.abs(model.x(point.at) - px);
      if (next < distance) { distance = next; best = index; }
    });
    setHover(best);
  };
  const h = hover !== null ? model.points[hover] : null;

  return (
    <div className="chart" ref={holder}>
      <div className="chart__legend">
        <span className="chart__key"><i style={{ background: "var(--chart-recv)" }} /> {t("Download")}<b className="mono">{formatBytes(usage.total_recv)}</b></span>
        <span className="chart__key"><i style={{ background: "var(--chart-send)" }} /> {t("Upload")}<b className="mono">{formatBytes(usage.total_send)}</b></span>
        {h && <span className="chart__hoverv mono">{formatTime(h.at, true)}{" · ↓"}{formatBytes(h.recv)} · ↑{formatBytes(h.send)}</span>}
      </div>
      <svg viewBox={`0 0 ${width} ${H}`} width={width} height={H} className="chart__svg" role="img"
        aria-label={t("Traffic over the last {hours} hours: {down} downloaded, {up} uploaded.", { hours: String(usage.hours), down: formatBytes(usage.total_recv), up: formatBytes(usage.total_send) })}
        onPointerMove={onMove} onPointerLeave={() => setHover(null)}>
        {model.ticks.map(tick => (
          <g key={tick.y}>
            <line x1={PAD.left} x2={width - PAD.right} y1={tick.y} y2={tick.y} className="chart__grid" />
            <text x={PAD.left - 8} y={tick.y + 3.5} className="chart__tick" textAnchor="end">{formatBytes(tick.v)}</text>
          </g>
        ))}
        <line x1={PAD.left} x2={width - PAD.right} y1={PAD.top + model.innerH} y2={PAD.top + model.innerH} className="chart__axis" />
        {[0, 0.5, 1].map(f => <text key={f} x={PAD.left + model.innerW * f} y={H - 7} className="chart__tick" textAnchor={f === 0 ? "start" : f === 1 ? "end" : "middle"}>{formatTime(model.t0 + (model.t1 - model.t0) * f)}</text>)}
        <path d={model.area("recv")} fill="var(--chart-recv)" opacity={0.12} />
        <path d={model.area("send")} fill="var(--chart-send)" opacity={0.12} />
        <path d={model.line("recv")} fill="none" stroke="var(--chart-recv)" strokeWidth={2} strokeLinejoin="round" />
        <path d={model.line("send")} fill="none" stroke="var(--chart-send)" strokeWidth={2} strokeLinejoin="round" />
        {model.segments.filter(run => run.length === 1).map(run => <g key={run[0].t}>
          <circle cx={model.x(run[0].at)} cy={model.y(run[0].recv)} r={3} fill="var(--chart-recv)" />
          <circle cx={model.x(run[0].at)} cy={model.y(run[0].send)} r={3} fill="var(--chart-send)" />
        </g>)}
        {h && <g>
          <line x1={model.x(h.at)} x2={model.x(h.at)} y1={PAD.top} y2={PAD.top + model.innerH} className="chart__cross" />
          <circle cx={model.x(h.at)} cy={model.y(h.recv)} r={4} fill="var(--chart-recv)" stroke="var(--glass)" strokeWidth={2} />
          <circle cx={model.x(h.at)} cy={model.y(h.send)} r={4} fill="var(--chart-send)" stroke="var(--glass)" strokeWidth={2} />
        </g>}
      </svg>
    </div>
  );
}

export function RangeSeg({ hours, onChange }: { hours: number; onChange: (hours: number) => void }) {
  const options = [{ h: 6, label: "6h" }, { h: 24, label: "24h" }, { h: 168, label: "7d" }, { h: 720, label: "30d" }];
  return <div className="seg" role="tablist" aria-label={t("Time range")}>
    {options.map(option => <button key={option.h} role="tab" aria-selected={hours === option.h} className={hours === option.h ? "on" : ""} onClick={() => onChange(option.h)}>{option.label}</button>)}
  </div>;
}
