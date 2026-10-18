import { useEffect, useRef, useSyncExternalStore } from "react";
import uPlot from "uplot";

export interface Series {
  label: string;
  color: string;
  values: (number | null)[];
}

const dark = window.matchMedia("(prefers-color-scheme: dark)");
const subscribeScheme = (cb: () => void) => {
  dark.addEventListener("change", cb);
  return () => dark.removeEventListener("change", cb);
};

/** Canvas can't read CSS variables; resolve "var(--x)" to its current value. */
function resolve(color: string) {
  const m = color.match(/^var\((--[\w-]+)\)$/);
  return m ? getComputedStyle(document.documentElement).getPropertyValue(m[1]).trim() : color;
}

/** Translucent version of a #rrggbb color for area fills. */
function tint(color: string, alpha: number) {
  return /^#[0-9a-f]{6}$/i.test(color) ? color + Math.round(alpha * 255).toString(16).padStart(2, "0") : undefined;
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

  // Series colors are tokens that differ per appearance, so rebuild on a scheme change.
  const isDark = useSyncExternalStore(subscribeScheme, () => dark.matches);
  const key = series.map((s) => s.label + s.color).join("|") + String(yMax) + String(isDark);

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    const rule = resolve("var(--color-rule)");
    const axis: uPlot.Axis = {
      stroke: resolve("var(--color-ink-3)"),
      grid: { stroke: rule, width: 1 },
      ticks: { show: false },
      font: `11px ${getComputedStyle(el).fontFamily}`,
    };
    const opts: uPlot.Options = {
      width: el.clientWidth,
      height,
      padding: [8, 8, 0, 0],
      cursor: { points: { size: 7 }, drag: { x: false, y: false } },
      legend: { show: false },
      scales: { x: { time: true }, y: { range: (_u, _min, max) => [0, yMax ?? Math.max(max * 1.15, 1)] } },
      // Horizontal guides only; vertical ones add noise without aiding the read.
      axes: [{ ...axis, grid: { show: false } }, { ...axis, size: 72, values: (_u, vals) => vals.map((v) => fmt.current(v)) }],
      series: [
        {},
        ...series.map((s) => {
          const color = resolve(s.color);
          return { label: s.label, stroke: color, width: 1.75, fill: tint(color, 0.14), points: { show: false } };
        }),
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
          <span key={s.label} className="inline-flex items-center gap-1.5 text-xs text-ink-2">
            <span className="inline-block size-2 rounded-full" style={{ background: s.color }} />
            {s.label}
          </span>
        ))}
      </div>
    </div>
  );
}
