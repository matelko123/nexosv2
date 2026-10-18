import { useEffect, useRef } from "react";
import uPlot from "uplot";

export interface Series {
  label: string;
  color: string;
  values: (number | null)[];
}

/** Time-series chart (uPlot) sized to its container. x is unix seconds. */
export function Chart({
  x,
  series,
  height = 180,
  format,
  yMax,
}: {
  x: number[];
  series: Series[];
  height?: number;
  format: (v: number) => string;
  yMax?: number;
}) {
  const host = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const fmt = useRef(format);
  fmt.current = format;

  const key = series.map((s) => s.label + s.color).join("|") + String(yMax);

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const axis: uPlot.Axis = {
      stroke: "#6b675f",
      grid: { stroke: "rgba(255,255,255,0.05)", width: 1 },
      ticks: { stroke: "rgba(255,255,255,0.08)", width: 1, size: 4 },
      font: "11px IBM Plex Mono",
    };
    const opts: uPlot.Options = {
      width: el.clientWidth,
      height,
      padding: [8, 8, 0, 0],
      cursor: { points: { size: 6 }, drag: { x: false, y: false } },
      legend: { show: false },
      scales: { x: { time: true }, y: { range: (_u, _min, max) => [0, yMax ?? Math.max(max * 1.15, 1)] } },
      axes: [axis, { ...axis, size: 72, values: (_u, vals) => vals.map((v) => fmt.current(v)) }],
      series: [
        {},
        ...series.map((s) => ({
          label: s.label,
          stroke: s.color,
          width: 1.5,
          fill: s.color + "1f",
          points: { show: false },
        })),
      ],
    };
    plot.current = new uPlot(opts, [x, ...series.map((s) => s.values)] as uPlot.AlignedData, el);
    const ro = new ResizeObserver(() => plot.current?.setSize({ width: el.clientWidth, height }));
    ro.observe(el);
    return () => {
      ro.disconnect();
      plot.current?.destroy();
      plot.current = null;
    };
    // Recreate only when the series layout changes; data updates go through setData.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, height]);

  useEffect(() => {
    plot.current?.setData([x, ...series.map((s) => s.values)] as uPlot.AlignedData);
  }, [x, series]);

  return (
    <div>
      <div ref={host} className="w-full" />
      <div className="mt-2 flex flex-wrap gap-4 px-1">
        {series.map((s) => (
          <span key={s.label} className="label inline-flex items-center gap-2">
            <span className="inline-block h-0.5 w-4" style={{ background: s.color }} />
            {s.label}
          </span>
        ))}
      </div>
    </div>
  );
}
