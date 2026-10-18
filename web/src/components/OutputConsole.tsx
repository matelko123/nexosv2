import { useLayoutEffect, useRef } from "react";

export interface RunState {
  title: string;
  lines: string[];
  running: boolean;
  exitCode?: number;
  error?: string;
}

/** Scrolling output of a long-running operation (compose up, pull...). */
export function OutputConsole({ run, onClose }: { run: RunState; onClose?: () => void }) {
  const box = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [run.lines.length]);
  const ok = !run.running && run.exitCode === 0 && !run.error;
  const failed = !run.running && !ok;
  return (
    <div className="panel">
      <div className="flex items-center justify-between gap-3 border-b border-rule px-3 py-2">
        <span className="inline-flex items-center gap-2">
          <span
            className={`inline-block size-2 rounded-full ${run.running ? "lamp-on bg-warn text-warn" : ok ? "bg-signal" : "bg-alarm"}`}
          />
          <span className="label !text-ink-2">{run.title}</span>
        </span>
        <span className="flex items-center gap-3">
          <span className={`label ${ok ? "!text-signal" : failed ? "!text-alarm" : ""}`}>
            {run.running ? "w toku…" : run.error ? "błąd" : `kod wyjścia ${run.exitCode}`}
          </span>
          {onClose && !run.running && (
            <button className="label hover:!text-ink" onClick={onClose}>
              ✕
            </button>
          )}
        </span>
      </div>
      <div ref={box} className="readout max-h-80 overflow-auto bg-chassis px-3 py-2 text-[12px] leading-[1.55] break-all whitespace-pre-wrap">
        {run.lines.map((l, i) => (
          <div key={i} className={l.startsWith("$ ") ? "text-amber" : /error|failed|denied/i.test(l) ? "text-[#ff9a8f]" : "text-ink-2"}>
            {l}
          </div>
        ))}
        {run.error && <div className="text-alarm">{run.error}</div>}
      </div>
    </div>
  );
}
