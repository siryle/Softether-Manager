"use client";

import { t } from "../lib/i18n";
import { useEffect, useMemo, useRef, useState } from "react";
import type { Usage } from "../lib/api";
import type { PollRate } from "../lib/rates";
import { formatBytes } from "../lib/util";
import { usePoll } from "./bits";

const H = 190;
const PAD_BASE = { top: 12, right: 10, bottom: 24, left: 64 };
type PlotPoint = { t: string; at: number; send: number; recv: number; sessions?: number };
type ChartMode = "rate" | "vol";

function formatBitrate(bytesPerSec: number): string {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec <= 0) return "0 bps";
  const bps = bytesPerSec * 8;
  if (bps >= 1e9) return `${(bps / 1e9).toFixed(2)} Gbps`;
  if (bps >= 1e6) return `${(bps / 1e6).toFixed(bps >= 1e8 ? 0 : 1)} Mbps`;
  if (bps >= 1e3) return `${(bps / 1e3).toFixed(bps >= 1e5 ? 0 : 1)} Kbps`;
  return `${Math.round(bps)} bps`;
}

function formatGranularity(sec: number): string {
  if (sec >= 3600) {
    const h = Math.round(sec / 3600);
    return t("{n}h/pt", { n: String(h) });
  }
  if (sec >= 60) {
    const m = Math.round(sec / 60);
    return t("{n}m/pt", { n: String(m) });
  }
  return t("{n}s/pt", { n: String(Math.max(1, Math.round(sec))) });
}

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
export function TrafficChart({
  usage,
  emptyLabel,
  defaultMode = "vol",
}: {
  usage: Usage;
  emptyLabel?: string;
  defaultMode?: ChartMode;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const [width, setWidth] = useState(720);
  const [mode, setMode] = useState<ChartMode>(defaultMode);
  const [showRecv, setShowRecv] = useState(true);
  const [showSend, setShowSend] = useState(true);
  const [showSessions, setShowSessions] = useState(true);
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

  const intervalSec = Math.max(1, Number(usage.sample_interval_seconds) || 300);

  const model = useMemo(() => {
    const t1 = Date.parse(usage.end ?? usage.points[usage.points.length - 1]?.t ?? "");
    const t0 = usage.start ? Date.parse(usage.start) : t1 - usage.hours * 3600_000;
    if (!Number.isFinite(t0) || !Number.isFinite(t1) || t1 <= t0) return null;

    const maxGap = intervalSec * 1500;
    const segments: PlotPoint[][] = [];
    const gapSpans: [number, number][] = [];
    let segment: PlotPoint[] = [];
    let lastValidAt: number | null = null;
    let hasSessions = false;

    for (const p of usage.points) {
      const at = Date.parse(p.t);
      if (
        p.send === null ||
        p.recv === null ||
        !Number.isFinite(p.send) ||
        !Number.isFinite(p.recv) ||
        p.send < 0 ||
        p.recv < 0 ||
        !Number.isFinite(at) ||
        at < t0 ||
        at > t1
      ) {
        segment = [];
        continue;
      }
      const previous = segment[segment.length - 1];
      if (previous && (at <= previous.at || at - previous.at > maxGap)) {
        if (at - previous.at > maxGap) gapSpans.push([previous.at, at]);
        segment = [];
      } else if (!previous && lastValidAt !== null && at - lastValidAt > maxGap) {
        gapSpans.push([lastValidAt, at]);
      }
      if (!segment.length) segments.push(segment);
      const sess = typeof p.sessions === "number" && Number.isFinite(p.sessions) ? p.sessions : undefined;
      if (sess !== undefined) hasSessions = true;
      segment.push({ t: p.t, at, send: p.send, recv: p.recv, sessions: sess });
      lastValidAt = at;
    }

    const points = segments.flat();
    if (!points.length) return null;

    const padRight = hasSessions && showSessions ? 42 : PAD_BASE.right;
    const padLeft = mode === "rate" ? 72 : PAD_BASE.left;
    const innerW = Math.max(40, width - padLeft - padRight);
    const innerH = H - PAD_BASE.top - PAD_BASE.bottom;

    const valOf = (p: PlotPoint, key: "send" | "recv") => (mode === "rate" ? p[key] / intervalSec : p[key]);

    let maxVal = 0;
    let sumVal = 0;
    let peakPoint: PlotPoint = points[0];
    let maxSess = 0;
    for (const p of points) {
      const r = showRecv ? valOf(p, "recv") : 0;
      const s = showSend ? valOf(p, "send") : 0;
      const top = Math.max(r, s);
      if (top > maxVal) {
        maxVal = top;
        peakPoint = p;
      }
      sumVal += r + s;
      if (p.sessions !== undefined && p.sessions > maxSess) maxSess = p.sessions;
    }
    const yCap = Math.max(mode === "rate" ? 128 : 1, maxVal);
    const avgVal = sumVal / Math.max(1, points.length);
    const sessCap = Math.max(1, Math.ceil(maxSess * 1.15));

    const x = (at: number) => padLeft + ((at - t0) / (t1 - t0)) * innerW;
    const y = (value: number) => PAD_BASE.top + innerH - (value / yCap) * innerH;
    const ySess = (value: number) => PAD_BASE.top + innerH - (value / sessCap) * innerH;

    const path = (run: PlotPoint[], key: "send" | "recv") =>
      run.map((p, i) => `${i ? "L" : "M"}${x(p.at).toFixed(1)},${y(valOf(p, key)).toFixed(1)}`).join("");
    const line = (key: "send" | "recv") => segments.map((run) => path(run, key)).join("");
    const area = (key: "send" | "recv") =>
      segments
        .filter((run) => run.length > 1)
        .map(
          (run) =>
            `${path(run, key)}L${x(run[run.length - 1].at).toFixed(1)},${PAD_BASE.top + innerH}L${x(run[0].at).toFixed(1)},${PAD_BASE.top + innerH}Z`,
        )
        .join("");
    const sessLine = () =>
      segments
        .map((run) =>
          run
            .filter((p) => p.sessions !== undefined)
            .map((p, i) => `${i ? "L" : "M"}${x(p.at).toFixed(1)},${ySess(p.sessions ?? 0).toFixed(1)}`)
            .join(""),
        )
        .filter(Boolean)
        .join("");

    const ticks = [0.25, 0.5, 0.75, 1].map((f) => ({ v: yCap * f, y: y(yCap * f) }));
    const sessTicks = hasSessions && showSessions ? [0.5, 1].map((f) => ({ v: Math.round(sessCap * f), y: ySess(sessCap * f) })) : [];
    const xFracs = width >= 460 ? [0, 0.25, 0.5, 0.75, 1] : [0, 0.5, 1];

    return {
      x,
      y,
      ySess,
      valOf,
      line,
      area,
      sessLine,
      ticks,
      sessTicks,
      xFracs,
      padLeft,
      padRight,
      innerW,
      innerH,
      t0,
      t1,
      points,
      segments,
      gapSpans,
      hasSessions,
      maxSess,
      peakVal: maxVal,
      peakPoint,
      avgVal,
    };
  }, [usage, width, mode, showRecv, showSend, showSessions, intervalSec]);

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

  const fmtAxis = (v: number) => (mode === "rate" ? formatBitrate(v) : formatBytes(v));

  const onMove = (event: React.PointerEvent) => {
    const svg = holder.current?.querySelector("svg");
    if (!svg) return;
    const rect = svg.getBoundingClientRect();
    const px = ((event.clientX - rect.left) * width) / rect.width;
    const at = model.t0 + ((px - model.padLeft) / model.innerW) * (model.t1 - model.t0);
    if (
      !model.segments.some((run) =>
        run.length === 1 ? Math.abs(model.x(run[0].at) - px) <= 6 : at >= run[0].at && at <= run[run.length - 1].at,
      )
    ) {
      setHover(null);
      return;
    }
    let best = 0;
    let distance = Infinity;
    model.points.forEach((point, index) => {
      const next = Math.abs(model.x(point.at) - px);
      if (next < distance) {
        distance = next;
        best = index;
      }
    });
    setHover(best);
  };

  const h = hover !== null ? model.points[hover] : null;
  const hPct = h ? model.x(h.at) / width : 0;

  return (
    <div className="chart" ref={holder}>
      <div className="chart__bar">
        <div className="chart__legend">
          <button
            type="button"
            className={`chart__key chart__key--btn${showRecv ? "" : " is-off"}`}
            aria-pressed={showRecv}
            onClick={() => setShowRecv((v) => !v)}
          >
            <i style={{ background: "var(--chart-recv)" }} /> {t("Download")}
            <b className="mono">{formatBytes(usage.total_recv)}</b>
          </button>
          <button
            type="button"
            className={`chart__key chart__key--btn${showSend ? "" : " is-off"}`}
            aria-pressed={showSend}
            onClick={() => setShowSend((v) => !v)}
          >
            <i style={{ background: "var(--chart-send)" }} /> {t("Upload")}
            <b className="mono">{formatBytes(usage.total_send)}</b>
          </button>
          {model.hasSessions && (
            <button
              type="button"
              className={`chart__key chart__key--btn${showSessions ? "" : " is-off"}`}
              aria-pressed={showSessions}
              onClick={() => setShowSessions((v) => !v)}
            >
              <i className="chart__key-dash" /> {t("Sessions")}
              <b className="mono">{t("Peak {n}", { n: String(model.maxSess) })}</b>
            </button>
          )}
        </div>

        <div className="chart__controls">
          <span className="chart__stat mono" title={formatTime(model.peakPoint.at, true)}>
            {t("Peak")} <b>{fmtAxis(model.peakVal)}</b> · {t("Avg")} <b>{fmtAxis(model.avgVal)}</b>
          </span>
          <span className="chart__gran mono">{formatGranularity(intervalSec)}</span>
          <div className="chart__mode" role="tablist" aria-label={t("Chart metric")}>
            <button
              type="button"
              role="tab"
              aria-selected={mode === "rate"}
              className={mode === "rate" ? "on" : ""}
              onClick={() => setMode("rate")}
            >
              {t("Rate")}
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={mode === "vol"}
              className={mode === "vol" ? "on" : ""}
              onClick={() => setMode("vol")}
            >
              {t("Volume")}
            </button>
          </div>
        </div>
      </div>

      <div className="chart__stage">
        <svg
          viewBox={`0 0 ${width} ${H}`}
          width={width}
          height={H}
          className="chart__svg"
          role="img"
          aria-label={t("Traffic over the last {hours} hours: {down} downloaded, {up} uploaded.", {
            hours: String(usage.hours),
            down: formatBytes(usage.total_recv),
            up: formatBytes(usage.total_send),
          })}
          onPointerMove={onMove}
          onPointerLeave={() => setHover(null)}
        >
          <defs>
            <pattern id="chart-gap-hatch" width="6" height="6" patternTransform="rotate(45 0 0)" patternUnits="userSpaceOnUse">
              <line x1="0" y1="0" x2="0" y2="6" stroke="var(--text-faint)" strokeOpacity="0.22" strokeWidth="1.5" />
            </pattern>
          </defs>

          {model.gapSpans.map(([g0, g1]) => {
            const gx0 = model.x(g0);
            const gx1 = model.x(g1);
            const gw = Math.max(3, gx1 - gx0);
            return (
              <rect
                key={`${g0}-${g1}`}
                x={gx0}
                y={PAD_BASE.top}
                width={gw}
                height={model.innerH}
                fill="url(#chart-gap-hatch)"
              />
            );
          })}

          {model.ticks.map((tick) => (
            <g key={tick.y}>
              <line x1={model.padLeft} x2={width - model.padRight} y1={tick.y} y2={tick.y} className="chart__grid" />
              <text x={model.padLeft - 8} y={tick.y + 3.5} className="chart__tick" textAnchor="end">
                {fmtAxis(tick.v)}
              </text>
            </g>
          ))}

          {model.sessTicks.map((st) => (
            <text
              key={`s-${st.y}`}
              x={width - model.padRight + 6}
              y={st.y + 3.5}
              className="chart__tick chart__tick--sess"
              textAnchor="start"
            >
              {st.v}
            </text>
          ))}

          <line
            x1={model.padLeft}
            x2={width - model.padRight}
            y1={PAD_BASE.top + model.innerH}
            y2={PAD_BASE.top + model.innerH}
            className="chart__axis"
          />

          {model.xFracs.map((f) => (
            <text
              key={f}
              x={model.padLeft + model.innerW * f}
              y={H - 7}
              className="chart__tick"
              textAnchor={f === 0 ? "start" : f === 1 ? "end" : "middle"}
            >
              {formatTime(model.t0 + (model.t1 - model.t0) * f)}
            </text>
          ))}

          {showRecv && <path d={model.area("recv")} fill="var(--chart-recv)" opacity={0.12} />}
          {showSend && <path d={model.area("send")} fill="var(--chart-send)" opacity={0.12} />}
          {showRecv && <path d={model.line("recv")} fill="none" stroke="var(--chart-recv)" strokeWidth={2} strokeLinejoin="round" />}
          {showSend && <path d={model.line("send")} fill="none" stroke="var(--chart-send)" strokeWidth={2} strokeLinejoin="round" />}
          {model.hasSessions && showSessions && (
            <path
              d={model.sessLine()}
              fill="none"
              stroke="var(--warn)"
              strokeWidth={1.5}
              strokeDasharray="4 3"
              strokeLinejoin="round"
            />
          )}

          {model.segments
            .filter((run) => run.length === 1)
            .map((run) => (
              <g key={run[0].t}>
                {showRecv && <circle cx={model.x(run[0].at)} cy={model.y(model.valOf(run[0], "recv"))} r={3} fill="var(--chart-recv)" />}
                {showSend && <circle cx={model.x(run[0].at)} cy={model.y(model.valOf(run[0], "send"))} r={3} fill="var(--chart-send)" />}
              </g>
            ))}

          {h && (
            <g>
              <line x1={model.x(h.at)} x2={model.x(h.at)} y1={PAD_BASE.top} y2={PAD_BASE.top + model.innerH} className="chart__cross" />
              {showRecv && (
                <circle
                  cx={model.x(h.at)}
                  cy={model.y(model.valOf(h, "recv"))}
                  r={4}
                  fill="var(--chart-recv)"
                  stroke="var(--glass)"
                  strokeWidth={2}
                />
              )}
              {showSend && (
                <circle
                  cx={model.x(h.at)}
                  cy={model.y(model.valOf(h, "send"))}
                  r={4}
                  fill="var(--chart-send)"
                  stroke="var(--glass)"
                  strokeWidth={2}
                />
              )}
              {model.hasSessions && showSessions && h.sessions !== undefined && (
                <circle
                  cx={model.x(h.at)}
                  cy={model.ySess(h.sessions)}
                  r={3.5}
                  fill="var(--warn)"
                  stroke="var(--glass)"
                  strokeWidth={1.5}
                />
              )}
            </g>
          )}
        </svg>

        {h && (
          <div
            className="chart__tooltip"
            style={
              hPct > 0.58
                ? { right: `${Math.max(8, (1 - hPct) * 100 + 2)}%` }
                : { left: `${Math.max(8, hPct * 100 + 2)}%` }
            }
          >
            <div className="chart__tooltip-time mono">{formatTime(h.at, true)}</div>
            <div className="chart__tooltip-row mono">
              <span style={{ color: "var(--chart-recv)" }}>↓ {t("Download")}</span>
              <span>
                {formatBytes(h.recv)} <small>({formatBitrate(h.recv / intervalSec)})</small>
              </span>
            </div>
            <div className="chart__tooltip-row mono">
              <span style={{ color: "var(--chart-send)" }}>↑ {t("Upload")}</span>
              <span>
                {formatBytes(h.send)} <small>({formatBitrate(h.send / intervalSec)})</small>
              </span>
            </div>
            {h.sessions !== undefined && (
              <div className="chart__tooltip-row mono">
                <span style={{ color: "var(--warn)" }}>● {t("Sessions")}</span>
                <span>{h.sessions}</span>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

export function RangeSeg({ hours, onChange }: { hours: number; onChange: (hours: number) => void }) {
  const options = [
    { h: 6, label: "6h" },
    { h: 24, label: "24h" },
    { h: 168, label: "7d" },
    { h: 720, label: "30d" },
  ];
  return (
    <div className="seg" role="tablist" aria-label={t("Time range")}>
      {options.map((option) => (
        <button
          key={option.h}
          role="tab"
          aria-selected={hours === option.h}
          className={hours === option.h ? "on" : ""}
          onClick={() => onChange(option.h)}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}
