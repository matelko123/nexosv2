import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "../lib/api";
import { dateTime } from "../lib/format";
import { ErrorText } from "../components/ui";

export function AuditPage() {
  const audit = useQuery({ queryKey: ["audit"], queryFn: api.audit, refetchInterval: 20_000 });
  const [filter, setFilter] = useState("");
  const [failedOnly, setFailedOnly] = useState(false);
  const q = filter.toLowerCase();
  const rows = (audit.data ?? []).filter(
    (e) =>
      (!failedOnly || !e.success) &&
      (!q || [e.action, e.username, e.ip, e.target, e.agent_id, e.detail].some((v) => v?.toLowerCase().includes(q))),
  );

  return (
    <div className="space-y-6">
      <header className="rise flex flex-wrap items-end justify-between gap-4">
        <div>
          <div className="label">03 / bezpieczeństwo</div>
          <h1 className="font-display text-2xl font-semibold tracking-wider">Dziennik audytu</h1>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <label className="label flex cursor-pointer items-center gap-2">
            <input type="checkbox" className="accent-amber" checked={failedOnly} onChange={(e) => setFailedOnly(e.target.checked)} />
            tylko błędy
          </label>
          <input className="field w-64 !py-1.5" placeholder="filtruj…" value={filter} onChange={(e) => setFilter(e.target.value)} />
        </div>
      </header>
      <ErrorText error={audit.error} />
      <div className="panel rise overflow-x-auto" style={{ animationDelay: "60ms" }}>
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left">
              {["czas", "", "akcja", "użytkownik", "ip", "cel", "szczegóły"].map((c, i) => (
                <th key={i} className="label px-4 py-2.5 font-normal">
                  {c}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-rule">
            {rows.map((e) => (
              <tr key={e.id} className="hover:bg-panel-2">
                <td className="readout px-4 py-2 text-xs whitespace-nowrap text-ink-3">{dateTime(e.time)}</td>
                <td className="px-1 py-2">
                  <span className={`inline-block size-1.5 rounded-full ${e.success ? "bg-signal" : "bg-alarm"}`} title={e.success ? "ok" : "odmowa / błąd"} />
                </td>
                <td className={`readout px-4 py-2 whitespace-nowrap ${e.success ? "" : "text-alarm"}`}>{e.action}</td>
                <td className="px-4 py-2">{e.username || <span className="text-ink-3">system</span>}</td>
                <td className="readout px-4 py-2 text-xs text-ink-2">{e.ip}</td>
                <td className="readout px-4 py-2 text-xs text-ink-2">{[e.target, e.agent_id].filter(Boolean).join(" · ")}</td>
                <td className="px-4 py-2 text-xs text-ink-3">{e.detail}</td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr>
                <td colSpan={7} className="px-4 py-8 text-center text-ink-3">
                  brak wpisów
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
