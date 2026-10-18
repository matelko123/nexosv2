import { useEffect, useRef, useState, type ReactNode } from "react";
import type { HostStatus } from "../lib/types";

const statusColor: Record<HostStatus, string> = {
  online: "var(--color-signal)",
  connecting: "var(--color-warn)",
  offline: "var(--color-alarm)",
  pending: "var(--color-ink-3)",
};

const statusLabel: Record<HostStatus, string> = {
  online: "online",
  connecting: "łączenie",
  offline: "offline",
  pending: "oczekuje",
};

export function StatusLamp({ status, withLabel = true }: { status: HostStatus; withLabel?: boolean }) {
  const color = statusColor[status];
  return (
    <span className="inline-flex items-center gap-2" title={statusLabel[status]}>
      <span
        className={`inline-block size-2 rounded-full ${status === "online" ? "lamp-on" : ""}`}
        style={{ background: color, color }}
      />
      {withLabel && (
        <span className="label" style={{ color }}>
          {statusLabel[status]}
        </span>
      )}
    </span>
  );
}

/** Segmented LED meter: the signature gauge of the console. */
export function SegmentMeter({ value, segments = 24 }: { value: number; segments?: number }) {
  const lit = Math.round((Math.min(Math.max(value, 0), 100) / 100) * segments);
  return (
    <div className="flex h-2.5 gap-[2px]" role="meter" aria-valuenow={Math.round(value)} aria-valuemin={0} aria-valuemax={100}>
      {Array.from({ length: segments }, (_, i) => {
        const at = ((i + 1) / segments) * 100;
        const on = i < lit;
        const color = at > 90 ? "var(--color-alarm)" : at > 75 ? "var(--color-warn)" : "var(--color-amber)";
        return (
          <span
            key={i}
            className="flex-1 transition-colors duration-300"
            style={{ background: on ? color : "var(--color-rule)", boxShadow: on ? `0 0 6px -1px ${color}` : undefined }}
          />
        );
      })}
    </div>
  );
}

export function Sparkline({
  values,
  max,
  color = "var(--color-amber)",
  height = 36,
}: {
  values: number[];
  max?: number;
  color?: string;
  height?: number;
}) {
  const w = 120;
  if (values.length < 2) {
    return <svg viewBox={`0 0 ${w} ${height}`} className="w-full" style={{ height }} preserveAspectRatio="none" />;
  }
  const top = max ?? Math.max(...values, 1);
  const pts = values.map((v, i) => `${(i / (values.length - 1)) * w},${height - (Math.min(v, top) / top) * (height - 2) - 1}`);
  const area = `0,${height} ${pts.join(" ")} ${w},${height}`;
  return (
    <svg viewBox={`0 0 ${w} ${height}`} className="w-full" style={{ height }} preserveAspectRatio="none" aria-hidden>
      <polygon points={area} fill={color} opacity={0.12} />
      <polyline points={pts.join(" ")} fill="none" stroke={color} strokeWidth={1.25} vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

export function PanelHeader({ title, children }: { title: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-rule px-4 py-2.5">
      <h2 className="label !text-ink-2">{title}</h2>
      {children}
    </div>
  );
}

export function ErrorText({ error }: { error: unknown }) {
  if (!error) return null;
  const msg = error instanceof Error ? error.message : String(error);
  return (
    <p className="border-l-2 border-alarm bg-alarm/10 px-3 py-2 text-sm text-alarm" role="alert">
      {msg}
    </p>
  );
}

export function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="label block">{label}</span>
      {children}
      {hint && <span className="block text-xs text-ink-3">{hint}</span>}
    </label>
  );
}

export function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    d?.showModal();
    return () => d?.close();
  }, []);
  return (
    <dialog
      ref={ref}
      onClose={onClose}
      className="panel m-auto w-[min(640px,calc(100vw-2rem))] p-0 text-ink backdrop:bg-black/70 backdrop:backdrop-blur-sm"
    >
      <PanelHeader title={title}>
        <button className="label hover:!text-ink" onClick={onClose} aria-label="Zamknij">
          esc ✕
        </button>
      </PanelHeader>
      <div className="p-5">{children}</div>
    </dialog>
  );
}

export function CopyBox({ value, rows = 3 }: { value: string; rows?: number }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="relative">
      <textarea readOnly value={value} rows={rows} className="field readout resize-none pr-24 text-xs break-all text-amber" onFocus={(e) => e.currentTarget.select()} />
      <button
        type="button"
        className="btn absolute top-2 right-2 !px-2.5 !py-1"
        onClick={async () => {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }}
      >
        {copied ? "skopiowano" : "kopiuj"}
      </button>
    </div>
  );
}

export function Stat({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="label">{label}</div>
      <div className="readout mt-1 truncate text-lg text-ink">{value}</div>
      {sub && <div className="readout truncate text-xs text-ink-3">{sub}</div>}
    </div>
  );
}
