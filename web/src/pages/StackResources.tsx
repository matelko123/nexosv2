import { useMemo, useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { observability, wsUrl } from "../lib/api";
import { useJsonSocket } from "../lib/ws";
import { bytes, pct, rate } from "../lib/format";
import type { Container, ContainerStats, ImageUpdateInfo, StatsMessage } from "../lib/types";
import { Chart } from "../components/Chart";
import { PanelHeader } from "../components/ui";
import { StateBadge } from "../components/containers";
import { UpdateBadge } from "../components/updates";
import { StorageList, StorageStatus, storageOf, useDiskUsage } from "../components/storage";

/** Live stats (2 s) for the given containers, or for all running ones when ids is undefined. */
export function useContainerStats(hostId: string, ids?: string[], enabled = true) {
  const [stats, setStats] = useState<Record<string, ContainerStats>>({});
  const key = ids?.slice().sort().join(",");
  const url = !enabled || key === "" ? null : wsUrl(`/hosts/${hostId}/containers/stats`, { ids: key });
  useJsonSocket<StatsMessage>(url, (m) => {
    if (!m.stats) return;
    const next: Record<string, ContainerStats> = {};
    for (const s of m.stats) next[s.id] = s;
    setStats(next);
  });
  return stats;
}

/** Sums a numeric stat over containers that have a live sample. */
export function sumStats(list: Container[], stats: Record<string, ContainerStats>, f: (s: ContainerStats) => number | undefined) {
  return list.reduce((a, c) => a + (stats[c.id] ? (f(stats[c.id]) ?? 0) : 0), 0);
}

const RANGES = ["1h", "6h", "24h", "7d"] as const;
type Range = (typeof RANGES)[number];
const rangeLabel: Record<Range, string> = { "1h": "ostatnia godzina", "6h": "ostatnie 6 h", "24h": "ostatnia doba", "7d": "ostatnie 7 dni" };

function Tile({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="panel min-w-0 px-4 py-3.5">
      <div className="label">{label}</div>
      <div className="readout mt-1 flex flex-wrap items-baseline gap-x-3 text-[1.0625rem] font-medium">{value}</div>
      {sub && <div className="mt-0.5 text-xs text-pretty text-ink-3">{sub}</div>}
    </div>
  );
}

/** Resource overview of a compose project: storage, traffic, history and services. */
export function StackResources({ hostId, members, updates }: { hostId: string; members: Container[]; updates: Map<string, ImageUpdateInfo> }) {
  const running = members.filter((c) => c.state === "running");
  const stats = useContainerStats(hostId, running.map((c) => c.id));
  const du = useDiskUsage(hostId);
  const storage = useMemo(() => storageOf(members, du.data), [members, du.data]);

  const [range, setRange] = useState<Range>("24h");
  const names = useMemo(() => members.map((c) => c.name).sort(), [members]);
  const hist = useQuery({
    queryKey: ["stack-history", hostId, names.join(","), range],
    queryFn: () => observability.containersHistory(hostId, names, range),
    enabled: names.length > 0,
    refetchInterval: 60_000,
  });
  const charts = useMemo(() => {
    const pts = hist.data?.points ?? [];
    return {
      x: pts.map((p) => p.ts / 1000),
      net: [
        { label: "pobrane ↓", color: "var(--color-cyan)", values: pts.map((p) => p.net_rx) },
        { label: "wysłane ↑", color: "var(--color-violet)", values: pts.map((p) => p.net_tx) },
      ],
      cpu: [{ label: "cpu %", color: "var(--color-accent)", values: pts.map((p) => p.cpu) }],
      mem: [{ label: "pamięć", color: "var(--color-signal)", values: pts.map((p) => p.mem) }],
    };
  }, [hist.data]);

  const rx = sumStats(running, stats, (s) => s.network_rx_bytes_per_second);
  const tx = sumStats(running, stats, (s) => s.network_tx_bytes_per_second);
  // Containers on the host network share the host's interfaces; Docker reports no traffic for them.
  const hostNet = members.filter((c) => c.networks?.includes("host"));
  const measured = du.data && !(du.data.computing && !du.data.computed_unix);
  // An older hub or agent has no disk usage / summed history endpoints.
  const duMissing = du.isError && !du.data;

  return (
    <section className="rise space-y-4">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Tile
          label="Na dysku"
          value={measured ? bytes(storage.total) : <span className="text-ink-3">{duMissing ? "niedostępne" : "liczenie…"}</span>}
          sub={
            measured
              ? `wolumeny ${bytes(storage.volumes)} · katalogi ${bytes(storage.binds)} · warstwy ${bytes(storage.writable)}`
              : duMissing
                ? "wymaga aktualizacji huba i agenta"
                : "pierwszy pomiar może potrwać"
          }
        />
        <Tile
          label="Sieć teraz"
          value={
            <>
              <span className="text-cyan">↓ {rate(rx)}</span>
              <span className="text-violet">↑ {rate(tx)}</span>
            </>
          }
          sub={
            hostNet.length
              ? `${hostNet.map((c) => c.compose_service || c.name).join(", ")}: sieć hosta, nie wliczane`
              : `od startu: ↓ ${bytes(sumStats(running, stats, (s) => s.network_rx_bytes))} ↑ ${bytes(sumStats(running, stats, (s) => s.network_tx_bytes))}`
          }
        />
        <Tile
          label={`Przesłano · ${rangeLabel[range]}`}
          value={
            hist.data ? (
              <>
                <span className="text-cyan">↓ {bytes(hist.data.rx_bytes)}</span>
                <span className="text-violet">↑ {bytes(hist.data.tx_bytes)}</span>
              </>
            ) : (
              <span className="text-ink-3">{hist.isError ? "niedostępne" : "…"}</span>
            )
          }
          sub={hist.isError ? "wymaga aktualizacji huba" : "z minutowych średnich zapisanych przez hub"}
        />
        <Tile
          label="Procesor i pamięć"
          value={
            <>
              <span>{pct(sumStats(running, stats, (s) => s.cpu_percent))}</span>
              <span>{bytes(sumStats(running, stats, (s) => s.memory_usage_bytes))}</span>
            </>
          }
          sub={`${running.length} z ${members.length} usług działa`}
        />
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <div className="panel">
          <PanelHeader title="Historia">
            <div className="tabs" role="tablist">
              {RANGES.map((r) => (
                <button key={r} onClick={() => setRange(r)} role="tab" aria-selected={r === range} className="tab readout">
                  {r}
                </button>
              ))}
            </div>
          </PanelHeader>
          {hist.isError ? (
            <div className="p-6 text-sm text-ink-3">Historia stacka jest niedostępna — hub nie obsługuje jeszcze tego widoku. Zaktualizuj hub.</div>
          ) : hist.data && hist.data.points.length === 0 ? (
            <div className="p-6 text-sm text-ink-3">Brak zapisanej historii — punkty pojawiają się co minutę, gdy usługi działają.</div>
          ) : (
            <div className="space-y-4 p-3">
              <div>
                <div className="mb-1 px-1 text-[13px] font-medium text-ink-2">Ruch sieciowy</div>
                <Chart x={charts.x} series={charts.net} format={(v) => rate(v)} height={190} />
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <div>
                  <div className="mb-1 px-1 text-[13px] font-medium text-ink-2">Procesor</div>
                  <Chart x={charts.x} series={charts.cpu} format={(v) => `${v.toFixed(v > 0 && v < 10 ? 1 : 0)}%`} height={130} />
                </div>
                <div>
                  <div className="mb-1 px-1 text-[13px] font-medium text-ink-2">Pamięć</div>
                  <Chart x={charts.x} series={charts.mem} format={(v) => bytes(v, 0)} height={130} />
                </div>
              </div>
            </div>
          )}
        </div>

        <div className="panel self-start">
          <PanelHeader title="Dane">
            <StorageStatus du={du} />
          </PanelHeader>
          {du.data?.error && <p className="px-4 pt-3 text-xs text-warn">Docker nie podał rozmiarów wolumenów: {du.data.error}</p>}
          <StorageList storage={storage} unavailable={duMissing} />
        </div>
      </div>

      <div className="panel">
        <PanelHeader title={`Usługi (${members.length})`} />
        <div className="overflow-x-auto">
          <table className="w-full min-w-[820px] text-sm">
            <thead>
              <tr className="text-left">
                {["usługa", "stan", "procesor", "pamięć", "sieć teraz", "przesłano od startu", "na dysku"].map((h) => (
                  <th key={h} className="label px-4 py-2 font-normal">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-rule">
              {members.map((c) => {
                const s = c.state === "running" ? stats[c.id] : undefined;
                const own = storageOf([c], du.data);
                return (
                  <tr key={c.id} className="group hover:bg-panel-2">
                    <td className="px-4 py-2.5">
                      <Link to="/hosts/$id/containers/$cid" params={{ id: hostId, cid: c.id }} className="block min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="truncate font-medium group-hover:text-accent">{c.compose_service || c.name}</span>
                          <UpdateBadge u={updates.get(c.name)} />
                        </div>
                        <div className="readout truncate text-[11px] text-ink-3">{c.image}</div>
                      </Link>
                    </td>
                    <td className="px-4 py-2.5">
                      <StateBadge c={c} />
                    </td>
                    <td className="readout px-4 py-2.5 text-xs">{s ? pct(s.cpu_percent) : "—"}</td>
                    <td className="readout px-4 py-2.5 text-xs">{s ? bytes(s.memory_usage_bytes) : "—"}</td>
                    <td className="readout px-4 py-2.5 text-[11px] leading-tight whitespace-nowrap">
                      {c.networks?.includes("host") ? (
                        <span className="text-ink-3">sieć hosta</span>
                      ) : s ? (
                        <>
                          <div className="text-cyan">↓ {rate(s.network_rx_bytes_per_second)}</div>
                          <div className="text-violet">↑ {rate(s.network_tx_bytes_per_second)}</div>
                        </>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td className="readout px-4 py-2.5 text-[11px] leading-tight whitespace-nowrap">
                      {s && !c.networks?.includes("host") ? (
                        <>
                          <div>↓ {bytes(s.network_rx_bytes)}</div>
                          <div>↑ {bytes(s.network_tx_bytes)}</div>
                        </>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td className="readout px-4 py-2.5 text-xs">{measured ? bytes(own.total) : duMissing ? "—" : "…"}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  );
}
