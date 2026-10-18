import { useMemo, useState } from "react";
import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { api, wsUrl } from "../lib/api";
import { useJsonSocket } from "../lib/ws";
import { bytes, pct, rate } from "../lib/format";
import type { Container, ContainerStats, ImageUpdateInfo, Me, StatsMessage } from "../lib/types";
import { UpdateBadge, useHostUpdates } from "../components/updates";
import { ErrorText } from "../components/ui";
import { ContainerActions, exitedWithError, portsText, StateBadge } from "../components/containers";
import { HostHeader, useHost } from "./HostDetail";

type Filter = "all" | "running" | "stopped" | "problems";

export function ContainersPage({ me }: { me: Me }) {
  const { id } = useParams({ from: "/hosts/$id/containers" });
  const host = useHost(id);
  const online = host.data?.status === "online";
  const containers = useQuery({
    queryKey: ["containers", id],
    queryFn: () => api.containers(id),
    enabled: online,
    refetchInterval: 30_000,
  });
  const [stats, setStats] = useState<Record<string, ContainerStats>>({});
  useJsonSocket<StatsMessage>(online ? wsUrl(`/hosts/${id}/containers/stats`) : null, (m) => {
    if (!m.stats) return;
    const next: Record<string, ContainerStats> = {};
    for (const s of m.stats) next[s.id] = s;
    setStats(next);
  });

  const upd = useHostUpdates(id);
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const canOperate = me.user.role !== "viewer";

  const groups = useMemo(() => {
    const q = query.toLowerCase();
    const list = (containers.data ?? []).filter((c) => {
      if (filter === "running" && c.state !== "running") return false;
      if (filter === "stopped" && c.state === "running") return false;
      if (filter === "problems" && !(exitedWithError(c) || c.health === "unhealthy" || c.state === "restarting" || c.state === "dead")) return false;
      return !q || [c.name, c.image, c.compose_project, c.compose_service].some((v) => v?.toLowerCase().includes(q));
    });
    const map = new Map<string, Container[]>();
    for (const c of list) {
      const k = c.compose_project || "";
      map.set(k, [...(map.get(k) ?? []), c]);
    }
    return [...map.entries()].sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b)));
  }, [containers.data, query, filter]);

  const all = containers.data ?? [];
  const running = all.filter((c) => c.state === "running").length;
  const problems = all.filter((c) => exitedWithError(c) || c.health === "unhealthy" || c.state === "restarting" || c.state === "dead").length;
  const totalCpu = Object.values(stats).reduce((s, x) => s + (x.cpu_percent ?? 0), 0);
  const totalMem = Object.values(stats).reduce((s, x) => s + (x.memory_usage_bytes ?? 0), 0);

  if (host.error) return <ErrorText error={host.error} />;
  if (!host.data) return <div className="label">ładowanie…</div>;

  return (
    <div className="space-y-6">
      <HostHeader host={host.data} isAdmin={me.user.role === "admin"} />
      {!online ? (
        <div className="panel p-6 text-sm text-ink-2">Host jest {host.data.status} — lista kontenerów jest dostępna tylko dla połączonych hostów.</div>
      ) : (
        <>
          <div className="rise grid grid-cols-2 gap-3 sm:grid-cols-4">
            {[
              ["działające", `${running}/${all.length}`],
              ["problemy", String(problems)],
              ["cpu kontenerów", pct(totalCpu)],
              ["ram kontenerów", bytes(totalMem)],
            ].map(([l, v]) => (
              <div key={l} className="panel px-4 py-3">
                <div className="label">{l}</div>
                <div className={`readout mt-1 text-xl ${l === "problemy" && problems > 0 ? "text-alarm" : ""}`}>{v}</div>
              </div>
            ))}
          </div>

          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex border border-rule">
              {(
                [
                  ["all", "wszystkie"],
                  ["running", "działające"],
                  ["stopped", "zatrzymane"],
                  ["problems", "problemy"],
                ] as [Filter, string][]
              ).map(([f, l]) => (
                <button key={f} onClick={() => setFilter(f)} className={`label px-3 py-1.5 ${f === filter ? "bg-amber !text-chassis" : "hover:!text-ink"}`}>
                  {l}
                </button>
              ))}
            </div>
            <input className="field w-72 !py-1.5" placeholder="szukaj: nazwa, obraz, projekt…" value={query} onChange={(e) => setQuery(e.target.value)} />
          </div>

          <ErrorText error={containers.error} />

          <div className="space-y-5">
            {groups.map(([project, list], gi) => (
              <section key={project || "_"} className="panel rise" style={{ animationDelay: `${gi * 40}ms` }}>
                <div className="flex items-center justify-between border-b border-rule px-4 py-2">
                  <h2 className="label !text-ink-2">
                    {project ? (
                      <>
                        <span className="text-amber">◆</span> {project}
                      </>
                    ) : (
                      "pojedyncze kontenery"
                    )}
                  </h2>
                  <span className="readout text-xs text-ink-3">
                    {list.filter((c) => c.state === "running").length}/{list.length}
                  </span>
                </div>
                <div className="overflow-x-auto">
                <table className="w-full min-w-[900px] text-sm">
                  <tbody className="divide-y divide-rule">
                    {list.map((c) => (
                      <Row key={c.id} hostId={id} c={c} s={stats[c.id]} canOperate={canOperate} update={upd.byContainer.get(c.name)} />
                    ))}
                  </tbody>
                </table>
                </div>
              </section>
            ))}
            {containers.data && groups.length === 0 && <div className="panel p-6 text-center text-sm text-ink-3">brak kontenerów pasujących do filtra</div>}
          </div>
        </>
      )}
    </div>
  );
}

function Row({ hostId, c, s, canOperate, update }: { hostId: string; c: Container; s?: ContainerStats; canOperate: boolean; update?: ImageUpdateInfo }) {
  const ports = portsText(c);
  const running = c.state === "running";
  return (
    <tr className="group hover:bg-panel-2">
      <td className="w-[26%] px-4 py-2.5">
        <Link to="/hosts/$id/containers/$cid" params={{ id: hostId, cid: c.id }} className="block min-w-0">
          <div className="flex items-center gap-2">
            <span className="truncate font-medium group-hover:text-amber">{c.name}</span>
            <UpdateBadge u={update} />
          </div>
          <div className="readout truncate text-[11px] text-ink-3">{c.image}</div>
        </Link>
      </td>
      <td className="w-[16%] px-3 py-2.5">
        <StateBadge c={c} />
        <div className="readout mt-0.5 truncate text-[11px] text-ink-3">{c.status}</div>
      </td>
      <td className="w-[14%] px-3 py-2.5">
        <div className="flex flex-wrap gap-1">
          {ports.slice(0, 3).map((p) => (
            <span key={p} className="readout border border-rule px-1.5 text-[11px] text-ink-2">
              {p}
            </span>
          ))}
          {ports.length > 3 && <span className="readout text-[11px] text-ink-3">+{ports.length - 3}</span>}
        </div>
      </td>
      <td className="w-[9%] px-3 py-2.5">
        {running && s ? (
          <>
            <div className="readout text-xs">{pct(s.cpu_percent)}</div>
            <div className="mt-1 h-1 bg-rule">
              <div className="h-full bg-amber transition-[width] duration-500" style={{ width: `${Math.min(s.cpu_percent ?? 0, 100)}%` }} />
            </div>
          </>
        ) : (
          <span className="text-ink-3">—</span>
        )}
      </td>
      <td className="w-[10%] px-3 py-2.5">
        {running && s ? <span className="readout text-xs">{bytes(s.memory_usage_bytes)}</span> : <span className="text-ink-3">—</span>}
      </td>
      <td className="w-[11%] px-3 py-2.5">
        {running && s ? (
          <div className="readout text-[11px] leading-tight">
            <div className="text-cyan">↓ {rate(s.network_rx_bytes_per_second)}</div>
            <div className="text-amber">↑ {rate(s.network_tx_bytes_per_second)}</div>
          </div>
        ) : (
          <span className="text-ink-3">—</span>
        )}
      </td>
      <td className="px-4 py-2.5 text-right">{canOperate && <div className="flex justify-end"><ContainerActions hostId={hostId} c={c} compact /></div>}</td>
    </tr>
  );
}
