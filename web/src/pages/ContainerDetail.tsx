import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { api, observability, wsUrl } from "../lib/api";
import { useJsonSocket } from "../lib/ws";
import { bytes, dateTime, pct, rate } from "../lib/format";
import type { Container, ContainerStats, Me, StatsMessage } from "../lib/types";
import { Chart } from "../components/Chart";
import { ErrorText, PanelHeader, Stat } from "../components/ui";
import { ContainerActions, portsText, StateBadge } from "../components/containers";
import { LogViewer } from "../components/LogViewer";
import { StorageList, StorageStatus, storageOf, useDiskUsage } from "../components/storage";
import { ContainerTerminal } from "../components/Terminal";
import { useHost } from "./HostDetail";

type Tab = "logs" | "stats" | "console" | "details";

export function ContainerDetailPage({ me }: { me: Me }) {
  const { id, cid } = useParams({ from: "/hosts/$id/containers/$cid" });
  const navigate = useNavigate();
  const host = useHost(id);
  const online = host.data?.status === "online";
  const containers = useQuery({ queryKey: ["containers", id], queryFn: () => api.containers(id), enabled: online, refetchInterval: 30_000 });
  const c = containers.data?.find((x) => x.id === cid);
  const role = me.user.role;
  const [tab, setTab] = useState<Tab>("logs");

  const tabs: { key: Tab; label: string; show: boolean }[] = [
    { key: "logs", label: "Logi", show: true },
    { key: "stats", label: "Zasoby", show: true },
    { key: "console", label: "Konsola", show: role === "admin" },
    { key: "details", label: "Szczegóły", show: role !== "viewer" },
  ];

  if (host.error) return <ErrorText error={host.error} />;
  if (containers.isPending || !host.data) return <div className="label">ładowanie…</div>;
  if (!c) {
    return (
      <div className="space-y-4">
        <Link to="/hosts/$id/containers" params={{ id }} className="back-link">
          ‹ Kontenery
        </Link>
        <ErrorText error={containers.error ?? "Kontener nie istnieje (mógł zostać usunięty)."} />
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <header className="rise flex flex-wrap items-end justify-between gap-4">
        <div className="min-w-0">
          <Link to="/hosts/$id/containers" params={{ id }} className="back-link">
            ‹ {host.data.name} · Kontenery
          </Link>
          <div className="mt-1 flex flex-wrap items-center gap-4">
            <h1 className="title">{c.name}</h1>
            <StateBadge c={c} />
          </div>
          <div className="readout mt-1 text-xs text-ink-3">
            {c.image} · {c.id.slice(0, 12)} · {c.status}
            {c.compose_project && ` · compose: ${c.compose_project}/${c.compose_service}`}
          </div>
        </div>
        {role !== "viewer" && <ContainerActions hostId={id} c={c} onRemoved={() => navigate({ to: "/hosts/$id/containers", params: { id } })} />}
      </header>

      <nav className="tabs" role="tablist">
        {tabs
          .filter((t) => t.show)
          .map((t) => (
            <button
              key={t.key}
              onClick={() => setTab(t.key)}
              role="tab"
              aria-selected={tab === t.key}
              className="tab"
            >
              {t.label}
            </button>
          ))}
      </nav>

      <div className="rise">
        {tab === "logs" && <LogViewer hostId={id} containerId={c.id} name={c.name} />}
        {tab === "stats" && (
          <div className="space-y-6">
            <StatsView hostId={id} containerId={c.id} running={c.state === "running"} />
            <ContainerStorage hostId={id} c={c} />
            <ContainerHistory hostId={id} name={c.name} />
          </div>
        )}
        {tab === "console" && <ContainerTerminal hostId={id} containerId={c.id} name={c.name} running={c.state === "running"} />}
        {tab === "details" && <Details hostId={id} containerId={c.id} ports={portsText(c)} />}
      </div>
    </div>
  );
}

const WINDOW = 150;

function StatsView({ hostId, containerId, running }: { hostId: string; containerId: string; running: boolean }) {
  const [hist, setHist] = useState<{ ts: number; s: ContainerStats }[]>([]);
  useJsonSocket<StatsMessage>(running ? wsUrl(`/hosts/${hostId}/containers/stats`, { ids: containerId }) : null, (m) => {
    const s = m.stats?.find((x) => x.id === containerId);
    if (s && m.timestamp_unix_ms) setHist((h) => [...h.slice(-(WINDOW - 1)), { ts: m.timestamp_unix_ms! / 1000, s }]);
  });
  const charts = useMemo(() => {
    const x = hist.map((h) => h.ts);
    return {
      x,
      cpu: [{ label: "cpu %", color: "var(--color-accent)", values: hist.map((h) => h.s.cpu_percent ?? 0) }],
      mem: [{ label: "pamięć", color: "var(--color-cyan)", values: hist.map((h) => h.s.memory_usage_bytes ?? 0) }],
      net: [
        { label: "rx ↓", color: "var(--color-cyan)", values: hist.map((h) => h.s.network_rx_bytes_per_second ?? 0) },
        { label: "tx ↑", color: "var(--color-violet)", values: hist.map((h) => h.s.network_tx_bytes_per_second ?? 0) },
      ],
    };
  }, [hist]);
  const last = hist.at(-1)?.s;

  if (!running) return <div className="panel p-6 text-sm text-ink-2">Kontener nie działa — brak statystyk.</div>;
  return (
    <div className="space-y-4">
      <div className="panel grid grid-cols-2 gap-4 p-4 sm:grid-cols-3 lg:grid-cols-6">
        <Stat label="cpu" value={pct(last?.cpu_percent)} />
        <Stat label="pamięć" value={bytes(last?.memory_usage_bytes)} sub={last?.memory_limit_bytes ? `limit ${bytes(last.memory_limit_bytes)} · ${pct(last.memory_percent)}` : undefined} />
        <Stat label="sieć rx" value={rate(last?.network_rx_bytes_per_second)} sub={`łącznie ${bytes(last?.network_rx_bytes)}`} />
        <Stat label="sieć tx" value={rate(last?.network_tx_bytes_per_second)} sub={`łącznie ${bytes(last?.network_tx_bytes)}`} />
        <Stat label="dysk r/w" value={`${bytes(last?.block_read_bytes)} / ${bytes(last?.block_write_bytes)}`} />
        <Stat label="procesy" value={last?.pids ?? "—"} />
      </div>
      <div className="grid gap-4 xl:grid-cols-3">
        <div className="panel">
          <PanelHeader title="CPU" />
          <div className="p-3">
            <Chart x={charts.x} series={charts.cpu} format={(v) => `${v.toFixed(v > 0 && v < 10 ? 1 : 0)}%`} height={160} />
          </div>
        </div>
        <div className="panel">
          <PanelHeader title="Pamięć" />
          <div className="p-3">
            <Chart x={charts.x} series={charts.mem} format={(v) => bytes(v, 0)} height={160} />
          </div>
        </div>
        <div className="panel">
          <PanelHeader title="Sieć" />
          <div className="p-3">
            <Chart x={charts.x} series={charts.net} format={(v) => rate(v)} height={160} />
          </div>
        </div>
      </div>
      <p className="label">próbkowanie co 2 s · okno {WINDOW} próbek</p>
    </div>
  );
}

/** Volumes and bind mounts of the container with their measured sizes. */
function ContainerStorage({ hostId, c }: { hostId: string; c: Container }) {
  const du = useDiskUsage(hostId);
  const storage = useMemo(() => storageOf([c], du.data), [c, du.data]);
  const layer = du.data?.containers?.find((x) => x.id === c.id);
  const image = layer?.size_root_fs_bytes && layer.size_rw_bytes !== undefined ? layer.size_root_fs_bytes - (layer.size_rw_bytes ?? 0) : undefined;
  return (
    <div className="panel">
      <PanelHeader title="Dane">
        <StorageStatus du={du} />
      </PanelHeader>
      {du.data?.computed_unix ? (
        <div className="grid grid-cols-2 gap-4 border-b border-rule p-4 sm:grid-cols-4">
          <Stat label="łącznie" value={bytes(storage.total)} sub="wolumeny, katalogi i warstwa" />
          <Stat label="wolumeny" value={bytes(storage.volumes)} />
          <Stat label="katalogi hosta" value={bytes(storage.binds)} />
          <Stat label="warstwa kontenera" value={bytes(storage.writable)} sub={image ? `obraz ${bytes(image)} (współdzielony)` : undefined} />
        </div>
      ) : null}
      <StorageList storage={{ ...storage, writable: 0 }} showUsers />
    </div>
  );
}

const HISTORY_RANGES = ["1h", "6h", "24h", "7d"] as const;

/** Stored per-minute history; keyed by name so it survives re-creation. */
function ContainerHistory({ hostId, name }: { hostId: string; name: string }) {
  const [range, setRange] = useState<(typeof HISTORY_RANGES)[number]>("6h");
  const q = useQuery({ queryKey: ["container-history", hostId, name, range], queryFn: () => observability.containerHistory(hostId, name, range), refetchInterval: 60_000 });
  const charts = useMemo(() => {
    const pts = q.data ?? [];
    return {
      x: pts.map((p) => p.ts / 1000),
      cpu: [{ label: "cpu %", color: "var(--color-accent)", values: pts.map((p) => p.cpu) }],
      mem: [{ label: "pamięć", color: "var(--color-cyan)", values: pts.map((p) => p.mem) }],
      net: [
        { label: "rx ↓", color: "var(--color-cyan)", values: pts.map((p) => p.net_rx) },
        { label: "tx ↑", color: "var(--color-violet)", values: pts.map((p) => p.net_tx) },
      ],
    };
  }, [q.data]);
  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-[1.0625rem] font-semibold tracking-[-0.012em] first-letter:uppercase">historia (średnie minutowe)</h2>
        <div className="tabs" role="tablist">
          {HISTORY_RANGES.map((r) => (
            <button key={r} onClick={() => setRange(r)} role="tab" aria-selected={r === range} className="tab readout">
              {r}
            </button>
          ))}
        </div>
      </div>
      <ErrorText error={q.error} />
      {q.data && q.data.length === 0 ? (
        <div className="panel p-6 text-sm text-ink-3">Brak zapisanej historii — punkty pojawiają się co minutę, gdy kontener działa.</div>
      ) : (
        <div className="grid gap-4 xl:grid-cols-3">
          {(
            [
              ["CPU", charts.cpu, (v: number) => `${v.toFixed(v > 0 && v < 10 ? 1 : 0)}%`],
              ["Pamięć", charts.mem, (v: number) => bytes(v, 0)],
              ["Sieć", charts.net, (v: number) => rate(v)],
            ] as const
          ).map(([title, series, fmt]) => (
            <div key={title} className="panel">
              <PanelHeader title={title} />
              <div className="p-3">
                <Chart x={charts.x} series={[...series]} format={fmt} height={160} />
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

type Inspect = {
  Created?: string;
  Path?: string;
  Args?: string[];
  RestartCount?: number;
  State?: { StartedAt?: string; ExitCode?: number; OOMKilled?: boolean; Error?: string };
  Config?: { Env?: string[]; Labels?: Record<string, string>; User?: string; WorkingDir?: string; Entrypoint?: string[] | null; Cmd?: string[] | null };
  HostConfig?: { RestartPolicy?: { Name?: string; MaximumRetryCount?: number }; Privileged?: boolean; NetworkMode?: string; Memory?: number; NanoCpus?: number; CapAdd?: string[] | null };
  NetworkSettings?: { Networks?: Record<string, { IPAddress?: string; Gateway?: string; MacAddress?: string; Aliases?: string[] | null }> };
  Mounts?: { Type?: string; Source?: string; Destination?: string; RW?: boolean; Name?: string }[];
};

function Details({ hostId, containerId, ports }: { hostId: string; containerId: string; ports: string[] }) {
  const q = useQuery({ queryKey: ["inspect", hostId, containerId], queryFn: () => api.inspect(hostId, containerId) as Promise<Inspect> });
  const [reveal, setReveal] = useState(false);
  if (q.error) return <ErrorText error={q.error} />;
  const d = q.data;
  if (!d) return <div className="label">ładowanie…</div>;
  const env = d.Config?.Env ?? [];
  const secretLike = /pass|secret|token|key|pwd|credential/i;

  return (
    <div className="grid gap-4 xl:grid-cols-2">
      <div className="panel">
        <PanelHeader title="Konfiguracja" />
        <div className="grid grid-cols-2 gap-4 p-4">
          <Stat label="utworzony" value={dateTime(d.Created)} />
          <Stat label="uruchomiony" value={dateTime(d.State?.StartedAt)} />
          <Stat label="restart policy" value={d.HostConfig?.RestartPolicy?.Name || "no"} sub={`restartów: ${d.RestartCount ?? 0}`} />
          <Stat label="tryb sieci" value={d.HostConfig?.NetworkMode ?? "—"} />
          <Stat label="użytkownik" value={d.Config?.User || "root (domyślny)"} sub={d.Config?.WorkingDir} />
          <Stat
            label="uprzywilejowany"
            value={d.HostConfig?.Privileged ? <span className="text-alarm">TAK</span> : "nie"}
            sub={d.HostConfig?.CapAdd?.length ? `cap_add: ${d.HostConfig.CapAdd.join(", ")}` : undefined}
          />
          <div className="col-span-2">
            <Stat label="polecenie" value={<span className="text-sm">{[d.Path, ...(d.Args ?? [])].join(" ")}</span>} />
          </div>
          {d.State?.Error && (
            <div className="col-span-2">
              <ErrorText error={d.State.Error} />
            </div>
          )}
        </div>
      </div>

      <div className="panel">
        <PanelHeader title="Sieć i porty" />
        <div className="space-y-3 p-4">
          <div className="flex flex-wrap gap-1.5">
            {ports.length ? ports.map((p) => <span key={p} className="readout rounded-md border border-rule px-2 py-0.5 text-xs">{p}</span>) : <span className="text-sm text-ink-3">brak opublikowanych portów</span>}
          </div>
          <table className="w-full text-sm">
            <tbody className="readout divide-y divide-rule text-xs">
              {Object.entries(d.NetworkSettings?.Networks ?? {}).map(([n, v]) => (
                <tr key={n}>
                  <td className="py-1.5 pr-3 text-ink">{n}</td>
                  <td className="py-1.5 pr-3 text-cyan">{v.IPAddress || "—"}</td>
                  <td className="py-1.5 text-ink-3">{(v.Aliases ?? []).join(", ")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="panel">
        <PanelHeader title="Wolumeny i montowania" />
        <table className="w-full text-sm">
          <tbody className="readout divide-y divide-rule text-xs">
            {(d.Mounts ?? []).map((m, i) => (
              <tr key={i}>
                <td className="px-4 py-2 text-ink-3">{m.Type}</td>
                <td className="px-2 py-2 break-all">{m.Name || m.Source}</td>
                <td className="px-2 py-2 break-all text-ink">{m.Destination}</td>
                <td className="px-4 py-2 text-ink-3">{m.RW ? "rw" : "ro"}</td>
              </tr>
            ))}
            {!d.Mounts?.length && (
              <tr>
                <td className="px-4 py-3 text-ink-3">brak</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="panel">
        <PanelHeader title={`Zmienne środowiskowe (${env.length})`}>
          <button className="btn btn-sm" onClick={() => setReveal(!reveal)}>
            {reveal ? "Ukryj wartości" : "Pokaż wartości"}
          </button>
        </PanelHeader>
        <div className="readout max-h-80 divide-y divide-rule overflow-auto text-xs">
          {env.map((e, i) => {
            const eq = e.indexOf("=");
            const k = e.slice(0, eq), v = e.slice(eq + 1);
            const hide = !reveal;
            return (
              <div key={i} className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-3 px-4 py-1.5">
                <span className={`truncate ${secretLike.test(k) ? "text-warn" : "text-ink-2"}`}>{k}</span>
                <span className="break-all">{hide ? "••••••" : v}</span>
              </div>
            );
          })}
        </div>
      </div>

      <details className="panel xl:col-span-2">
        <summary className="label cursor-pointer px-4 py-2.5 hover:!text-ink">surowy docker inspect (może zawierać sekrety)</summary>
        <pre className="readout max-h-[480px] overflow-auto border-t border-rule bg-chassis p-4 text-[11px] text-ink-2">{JSON.stringify(d, null, 2)}</pre>
      </details>
    </div>
  );
}
