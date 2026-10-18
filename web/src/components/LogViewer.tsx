import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { wsUrl } from "../lib/api";
import { useJsonSocket } from "../lib/ws";
import type { LogLine } from "../lib/types";

const MAX_LINES = 10_000;

type LogMsg = { lines?: LogLine[]; error?: string; end?: boolean };

export function LogViewer({ hostId, containerId, name }: { hostId: string; containerId: string; name: string }) {
  const [tail, setTail] = useState(500);
  const [timestamps, setTimestamps] = useState(false);
  const [wrap, setWrap] = useState(true);
  const [filter, setFilter] = useState("");
  const [follow, setFollow] = useState(true);
  const [lines, setLines] = useState<LogLine[]>([]);
  const [status, setStatus] = useState<string | null>(null);
  // Bump to reconnect manually; logs are not auto-reconnected to avoid
  // replaying the tail as duplicates.
  const [session, setSession] = useState(0);
  const box = useRef<HTMLDivElement>(null);

  const url = useMemo(
    () => wsUrl(`/hosts/${hostId}/containers/${containerId}/logs`, { tail, timestamps, s: session }),
    [hostId, containerId, tail, timestamps, session],
  );
  useEffect(() => {
    setLines([]);
    setStatus(null);
  }, [url]);

  const state = useJsonSocket<LogMsg>(
    url,
    (m) => {
      if (m.lines) setLines((prev) => {
        const next = prev.concat(m.lines!);
        return next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next;
      });
      if (m.error) setStatus("błąd: " + m.error);
      if (m.end) setStatus("strumień zakończony (kontener zatrzymany)");
    },
    { reconnect: false },
  );

  const visible = useMemo(() => {
    if (!filter) return lines;
    const q = filter.toLowerCase();
    return lines.filter((l) => l.text?.toLowerCase().includes(q));
  }, [lines, filter]);

  useLayoutEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [visible, follow]);

  const download = () => {
    const blob = new Blob([lines.map((l) => l.text).join("\n") + "\n"], { type: "text/plain" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `${name}-${new Date().toISOString().replace(/[:.]/g, "-")}.log`;
    a.click();
    URL.revokeObjectURL(a.href);
  };

  return (
    <div className="panel flex h-[calc(100vh-300px)] min-h-[420px] flex-col">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-rule px-3 py-2">
        <span className="inline-flex items-center gap-2">
          <span
            className={`inline-block size-2 rounded-full ${state === "open" ? "bg-signal text-signal" : state === "connecting" ? "bg-warn" : "bg-ink-3"}`}
          />
          <span className="label">{state === "open" ? "na żywo" : state === "connecting" ? "łączenie" : "rozłączono"}</span>
        </span>
        <label className="label flex items-center gap-1.5">
          ostatnie
          <select className="field !w-auto !py-0.5 !text-xs" value={tail} onChange={(e) => setTail(Number(e.target.value))}>
            {[100, 500, 2000, 10000].map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
        {(
          [
            ["czas", timestamps, setTimestamps],
            ["zawijaj", wrap, setWrap],
            ["przewijaj", follow, setFollow],
          ] as [string, boolean, (v: boolean) => void][]
        ).map(([l, v, set]) => (
          <label key={l} className="label flex cursor-pointer items-center gap-1.5">
            <input type="checkbox" className="accent-accent" checked={v} onChange={(e) => set(e.target.checked)} />
            {l}
          </label>
        ))}
        <input className="field !w-56 !py-1 text-xs" placeholder="filtruj…" value={filter} onChange={(e) => setFilter(e.target.value)} />
        <div className="ml-auto flex gap-1.5">
          <button className="btn btn-sm" onClick={() => setLines([])}>
            Wyczyść
          </button>
          <button className="btn btn-sm" onClick={download} disabled={!lines.length}>
            Pobierz
          </button>
          {state !== "open" && state !== "connecting" && (
            <button className="btn btn-sm" onClick={() => setSession((s) => s + 1)}>
              Połącz
            </button>
          )}
        </div>
      </div>
      <div
        ref={box}
        onWheel={(e) => e.deltaY < 0 && setFollow(false)}
        className={`readout flex-1 overflow-auto bg-chassis px-3 py-2 text-[12px] leading-[1.55] ${wrap ? "break-all whitespace-pre-wrap" : "whitespace-pre"}`}
      >
        {visible.map((l, i) => {
          const text = l.text ?? "";
          const sp = timestamps ? text.indexOf(" ") : -1;
          return (
            <div key={i} className={l.stream === "stderr" ? "text-alarm" : "text-ink"}>
              {sp > 0 && <span className="mr-2 text-ink-3 select-none">{text.slice(11, Math.min(sp, 23))}</span>}
              {sp > 0 ? text.slice(sp + 1) : text}
            </div>
          );
        })}
        {status && <div className="label mt-2 !text-warn">— {status}</div>}
        {!lines.length && !status && <div className="label mt-2">brak logów</div>}
      </div>
      <div className="readout flex justify-between border-t border-rule px-3 py-1 text-[11px] text-ink-3">
        <span>
          {visible.length}
          {filter && ` / ${lines.length}`} linii
        </span>
        <span>
          <span className="text-alarm">■</span> stderr
        </span>
      </div>
    </div>
  );
}
