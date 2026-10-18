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
    <span className="inline-flex items-center gap-2" title={statusLabel[status]} style={{ color }}>
      <span className="dot" />
      {withLabel && <span className="text-[0.8125rem] font-medium first-letter:uppercase">{statusLabel[status]}</span>}
    </span>
  );
}

/** Capsule meter; the fill turns orange past 75% and red past 90%. */
export function Meter({ value }: { value: number }) {
  const v = Math.min(Math.max(value, 0), 100);
  const color = v > 90 ? "var(--color-alarm)" : v > 75 ? "var(--color-warn)" : "var(--color-accent)";
  return (
    <div className="meter" role="meter" aria-valuenow={Math.round(v)} aria-valuemin={0} aria-valuemax={100}>
      <span style={{ width: `${v}%`, background: color }} />
    </div>
  );
}

export function Sparkline({
  values,
  max,
  color = "var(--color-accent)",
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
      <polygon points={area} fill={color} opacity={0.14} />
      <polyline points={pts.join(" ")} fill="none" stroke={color} strokeWidth={1.5} strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

export function PanelHeader({ title, children }: { title: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex min-h-12 items-center justify-between gap-4 border-b border-rule px-4 py-2.5">
      <h2 className="text-[0.9375rem] font-semibold tracking-[-0.01em] text-ink">{title}</h2>
      {children}
    </div>
  );
}

export function ErrorText({ error }: { error: unknown }) {
  if (!error) return null;
  const msg = error instanceof Error ? error.message : String(error);
  return (
    <p className="rounded-[var(--radius-control)] bg-alarm/10 px-3 py-2 text-sm text-alarm" role="alert">
      {msg}
    </p>
  );
}

export function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="block text-[0.8125rem] font-medium text-ink-2">{label}</span>
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
      // StrictMode re-runs the effect (close, then showModal); ignore that stale close event.
      onClose={(e) => !e.currentTarget.open && onClose()}
      className="sheet m-auto max-h-[calc(100dvh-2rem)] w-[min(600px,calc(100vw-2rem))] overflow-y-auto overscroll-contain p-0"
    >
      <div className="flex items-start justify-between gap-4 px-6 pt-5">
        <h2 className="text-[1.0625rem] font-semibold tracking-[-0.015em]">{title}</h2>
        <button
          className="-mt-0.5 -mr-2 grid size-7 shrink-0 place-items-center rounded-full bg-panel-2 text-ink-3 transition-colors hover:text-ink active:scale-95"
          onClick={onClose}
          aria-label="Zamknij"
        >
          <svg viewBox="0 0 12 12" className="size-2.5" aria-hidden>
            <path d="M2 2l8 8M10 2l-8 8" stroke="currentColor" strokeWidth={1.8} strokeLinecap="round" />
          </svg>
        </button>
      </div>
      <div className="px-6 pt-4 pb-6">{children}</div>
    </dialog>
  );
}

export function CopyBox({ value, rows = 3 }: { value: string; rows?: number }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="relative">
      <textarea readOnly value={value} rows={rows} className="field readout resize-none pr-24 text-xs break-all" onFocus={(e) => e.currentTarget.select()} />
      <button
        type="button"
        className="btn btn-sm absolute top-2 right-2"
        onClick={async () => {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }}
      >
        {copied ? "Skopiowano" : "Kopiuj"}
      </button>
    </div>
  );
}

/**
 * Confirmation dialog. With `typeToConfirm`, the user must type that word
 * first; used for operations that destroy data.
 */
export function Confirm({
  title,
  children,
  confirmLabel,
  danger = false,
  typeToConfirm,
  pending = false,
  error,
  onConfirm,
  onClose,
}: {
  title: string;
  children: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  typeToConfirm?: string;
  pending?: boolean;
  error?: unknown;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const [typed, setTyped] = useState("");
  const blocked = !!typeToConfirm && typed !== typeToConfirm;
  return (
    <Modal title={title} onClose={onClose}>
      <div className="space-y-4">
        <div className="space-y-2 text-sm text-ink-2">{children}</div>
        {typeToConfirm && (
          <Field label={`Wpisz "${typeToConfirm}", aby potwierdzić`}>
            <input className="field readout" value={typed} onChange={(e) => setTyped(e.target.value)} autoFocus />
          </Field>
        )}
        <ErrorText error={error} />
        <div className="flex justify-end gap-2">
          <button className="btn" onClick={onClose}>
            Anuluj
          </button>
          <button className={`btn ${danger ? "btn-danger" : "btn-primary"}`} disabled={pending || blocked} onClick={onConfirm}>
            {pending ? "Wykonywanie…" : confirmLabel.charAt(0).toUpperCase() + confirmLabel.slice(1)}
          </button>
        </div>
      </div>
    </Modal>
  );
}

export function Stat({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="label">{label}</div>
      <div className="readout mt-1 truncate text-lg font-medium text-ink">{value}</div>
      {sub && <div className="readout truncate text-xs text-ink-3">{sub}</div>}
    </div>
  );
}
