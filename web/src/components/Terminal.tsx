import { useEffect, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { wsUrl } from "../lib/api";

const SHELLS = [
  { label: "auto (bash → sh)", cmd: "" },
  { label: "/bin/sh", cmd: "/bin/sh" },
  { label: "/bin/bash", cmd: "/bin/bash" },
  { label: "/bin/ash", cmd: "/bin/ash" },
];

const theme = {
  background: "#0b0c0d",
  foreground: "#e6e1d6",
  cursor: "#ffb000",
  cursorAccent: "#0b0c0d",
  selectionBackground: "#ffb00055",
  black: "#181b1e",
  red: "#ff5a4e",
  green: "#4ade80",
  yellow: "#ffd166",
  blue: "#5ccfe6",
  magenta: "#d7a6ff",
  cyan: "#5ccfe6",
  white: "#e6e1d6",
  brightBlack: "#6b675f",
};

/** Interactive shell in a container over the hub exec WebSocket. */
export function ContainerTerminal({ hostId, containerId, name, running }: { hostId: string; containerId: string; name: string; running: boolean }) {
  const [shell, setShell] = useState("");
  const [custom, setCustom] = useState("");
  const [user, setUser] = useState("");
  const [session, setSession] = useState(0);
  const [state, setState] = useState<"idle" | "connecting" | "open" | "closed">("idle");
  const host = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (session === 0 || !host.current) return;
    const term = new XTerm({ theme, fontFamily: "IBM Plex Mono, monospace", fontSize: 13, cursorBlink: true, scrollback: 5000 });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host.current);
    fit.fit();
    setState("connecting");
    const cmd = shell === "custom" ? custom : shell;
    const ws = new WebSocket(wsUrl(`/hosts/${hostId}/containers/${containerId}/exec`, { cols: term.cols, rows: term.rows, cmd, user, name }));
    ws.binaryType = "arraybuffer";
    const send = (m: object) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(m));
    ws.onopen = () => {
      setState("open");
      term.focus();
    };
    ws.onmessage = (e) => {
      if (typeof e.data === "string") {
        const m = JSON.parse(e.data);
        if (m.type === "exit") term.write(`\r\n\x1b[33m[proces zakończony, kod ${m.code}]\x1b[0m\r\n`);
        if (m.type === "error") term.write(`\r\n\x1b[31m[błąd: ${m.error}]\x1b[0m\r\n`);
      } else {
        term.write(new Uint8Array(e.data));
      }
    };
    ws.onclose = () => setState("closed");
    const d1 = term.onData((data) => send({ type: "stdin", data }));
    const d2 = term.onResize(({ cols, rows }) => send({ type: "resize", cols, rows }));
    const ro = new ResizeObserver(() => fit.fit());
    ro.observe(host.current);
    return () => {
      ro.disconnect();
      d1.dispose();
      d2.dispose();
      ws.close();
      term.dispose();
    };
    // Settings are read when a session starts; changing them needs reconnect.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session, hostId, containerId]);

  return (
    <div className="panel flex h-[calc(100vh-300px)] min-h-[420px] flex-col">
      <div className="flex flex-wrap items-center gap-3 border-b border-rule px-3 py-2">
        <label className="label flex items-center gap-1.5">
          powłoka
          <select className="field !w-auto !py-0.5 !text-xs" value={shell} onChange={(e) => setShell(e.target.value)}>
            {SHELLS.map((s) => (
              <option key={s.label} value={s.cmd}>
                {s.label}
              </option>
            ))}
            <option value="custom">własne polecenie…</option>
          </select>
        </label>
        {shell === "custom" && <input className="field readout !w-56 !py-1 text-xs" placeholder="np. python3" value={custom} onChange={(e) => setCustom(e.target.value)} />}
        <label className="label flex items-center gap-1.5">
          użytkownik
          <input className="field readout !w-28 !py-1 text-xs" placeholder="domyślny" value={user} onChange={(e) => setUser(e.target.value)} />
        </label>
        <button className="btn btn-primary !px-3 !py-1 !text-[10px]" disabled={!running || state === "connecting"} onClick={() => setSession((s) => s + 1)}>
          {state === "open" ? "nowa sesja" : "połącz"}
        </button>
        <span className="label ml-auto">
          {state === "open" ? <span className="text-signal">sesja aktywna · zapisana w audycie</span> : state === "closed" ? "rozłączono" : !running ? "kontener nie działa" : ""}
        </span>
      </div>
      <div className="relative flex-1 bg-chassis p-2">
        {session === 0 && (
          <div className="absolute inset-0 grid place-items-center text-center">
            <div>
              <div className="font-display tracking-wider text-ink-2">Konsola · {name}</div>
              <p className="mt-1 text-sm text-ink-3">Sesja uruchamia proces w kontenerze przez Docker exec. Każde połączenie jest odnotowywane w audycie.</p>
            </div>
          </div>
        )}
        <div ref={host} className="h-full w-full" />
      </div>
    </div>
  );
}
