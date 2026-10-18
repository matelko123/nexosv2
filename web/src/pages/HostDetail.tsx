import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { ago, bytes, dateTime, duration, pct, rate } from "../lib/format";
import type { Enrollment, Host, Me } from "../lib/types";
import { Chart } from "../components/Chart";
import { ErrorText, Field, Modal, PanelHeader, SegmentMeter, Stat, StatusLamp } from "../components/ui";
import { EnrollInstructions } from "./Hosts";

const RANGES = ["15m", "1h", "6h", "24h", "7d", "30d", "90d"] as const;
const C = { amber: "#ffb000", cyan: "#5ccfe6", signal: "#4ade80", alarm: "#ff5a4e" };

export function HostDetailPage({ me }: { me: Me }) {
  const { id } = useParams({ from: "/hosts/$id" });
  const isAdmin = me.user.role === "admin";
  const host = useQuery({ queryKey: ["host", id], queryFn: () => api.host(id) });
  const [range, setRange] = useState<(typeof RANGES)[number]>("1h");
  const series = useQuery({
    queryKey: ["metrics", id, range],
    queryFn: () => api.metrics(id, range),
    refetchInterval: 15_000,
    enabled: host.data?.status !== "pending",
  });

  const charts = useMemo(() => {
    const pts = series.data ?? [];
    return {
      x: pts.map((p) => p.ts / 1000),
      cpu: [{ label: "cpu %", color: C.amber, values: pts.map((p) => p.cpu) }],
      mem: [
        { label: "ram", color: C.cyan, values: pts.map((p) => p.mem_used) },
        { label: "swap", color: C.alarm, values: pts.map((p) => p.swap_used) },
      ],
      net: [
        { label: "rx ↓", color: C.cyan, values: pts.map((p) => p.net_rx) },
        { label: "tx ↑", color: C.amber, values: pts.map((p) => p.net_tx) },
      ],
      disk: [
        { label: "odczyt", color: C.signal, values: pts.map((p) => p.disk_read) },
        { label: "zapis", color: C.amber, values: pts.map((p) => p.disk_write) },
      ],
    };
  }, [series.data]);

  if (host.error) return <ErrorText error={host.error} />;
  const h = host.data;
  if (!h) return <div className="label">ładowanie…</div>;
  const m = h.metrics;
  const info = h.info;
  const memPct = m?.memory_total_bytes ? ((m.memory_used_bytes ?? 0) / m.memory_total_bytes) * 100 : 0;

  return (
    <div className="space-y-6">
      <HostHeader host={h} isAdmin={isAdmin} />

      {h.status === "pending" ? (
        <div className="panel p-6 text-sm text-ink-2">Host czeka na rejestrację agenta. Wygeneruj nowy pakiet, jeśli poprzedni wygasł.</div>
      ) : (
        <>
          <section className="rise grid gap-4 lg:grid-cols-3" style={{ animationDelay: "60ms" }}>
            <div className="panel lg:col-span-2">
              <PanelHeader title="Odczyt na żywo">
                <span className="readout text-xs text-ink-3">{m?.timestamp_unix_ms ? dateTime(m.timestamp_unix_ms) : "brak danych"}</span>
              </PanelHeader>
              <div className="grid gap-5 p-4 sm:grid-cols-2">
                <div>
                  <div className="mb-1.5 flex items-baseline justify-between">
                    <span className="label">cpu</span>
                    <span className="readout text-2xl text-amber">{pct(m?.cpu_percent)}</span>
                  </div>
                  <SegmentMeter value={m?.cpu_percent ?? 0} segments={32} />
                  <div className="readout mt-2 text-xs text-ink-3">
                    load {m?.load1?.toFixed(2) ?? "—"} · {m?.load5?.toFixed(2) ?? "—"} · {m?.load15?.toFixed(2) ?? "—"}
                  </div>
                </div>
                <div>
                  <div className="mb-1.5 flex items-baseline justify-between">
                    <span className="label">ram</span>
                    <span className="readout text-2xl text-cyan">{pct(memPct)}</span>
                  </div>
                  <SegmentMeter value={memPct} segments={32} />
                  <div className="readout mt-2 text-xs text-ink-3">
                    {bytes(m?.memory_used_bytes)} / {bytes(m?.memory_total_bytes)} · swap {bytes(m?.swap_used_bytes)}
                  </div>
                </div>
                <div className="sm:col-span-2">
                  <div className="label mb-2">rdzenie</div>
                  <div className="grid gap-1.5 [grid-template-columns:repeat(auto-fill,minmax(44px,1fr))]">
                    {(m?.cpu_per_core_percent ?? []).map((v, i) => (
                      <div key={i} className="border border-rule bg-chassis p-1.5" title={`rdzeń ${i}: ${pct(v)}`}>
                        <div className="relative h-8 bg-rule">
                          <div
                            className="absolute inset-x-0 bottom-0 transition-[height] duration-500"
                            style={{ height: `${Math.min(v, 100)}%`, background: v > 90 ? C.alarm : C.amber }}
                          />
                        </div>
                        <div className="readout mt-1 text-center text-[10px] text-ink-3">{i}</div>
                      </div>
                    ))}
                  </div>
                </div>
              </div>
            </div>

            <div className="panel">
              <PanelHeader title="System" />
              <div className="grid grid-cols-2 gap-4 p-4">
                <Stat label="system" value={info?.platform ? `${info.platform} ${info.platform_version ?? ""}` : info?.os ?? "—"} sub={info?.arch} />
                <Stat label="jądro" value={info?.kernel_version ?? "—"} />
                <Stat label="cpu" value={`${info?.cpu_cores_logical ?? "—"} wątków`} sub={info?.cpu_model} />
                <Stat label="uptime" value={duration(m?.uptime_seconds ?? info?.uptime_seconds)} />
                <Stat
                  label="docker"
                  value={info?.docker?.available ? info.docker.server_version : "niedostępny"}
                  sub={info?.docker?.available ? `${info.docker.containers_running ?? 0}/${info.docker.containers_total ?? 0} kontenerów · ${info.docker.images ?? 0} obrazów` : info?.docker?.error}
                />
                <Stat label="agent" value={info?.agent_version ?? "—"} sub={`widziany ${ago(h.last_seen_at)}`} />
                <div className="col-span-2">
                  <Stat
                    label="certyfikat mTLS"
                    value={h.cert_not_after ? `ważny do ${new Date(h.cert_not_after).toLocaleDateString("pl-PL")}` : "—"}
                    sub="rotacja automatyczna 10 dni przed wygaśnięciem"
                  />
                </div>
              </div>
            </div>
          </section>

          <section className="rise space-y-4" style={{ animationDelay: "120ms" }}>
            <div className="flex items-center justify-between">
              <h2 className="label !text-ink-2">historia</h2>
              <div className="flex border border-rule">
                {RANGES.map((r) => (
                  <button
                    key={r}
                    onClick={() => setRange(r)}
                    className={`readout px-3 py-1 text-xs ${r === range ? "bg-amber text-chassis" : "text-ink-2 hover:text-ink"}`}
                  >
                    {r}
                  </button>
                ))}
              </div>
            </div>
            <div className="grid gap-4 xl:grid-cols-2">
              <div className="panel">
                <PanelHeader title="CPU" />
                <div className="p-3">
                  <Chart x={charts.x} series={charts.cpu} format={(v) => `${v.toFixed(v > 0 && v < 10 ? 1 : 0)}%`} yMax={100} />
                </div>
              </div>
              <div className="panel">
                <PanelHeader title="Pamięć" />
                <div className="p-3">
                  <Chart x={charts.x} series={charts.mem} format={(v) => bytes(v, 0)} yMax={m?.memory_total_bytes} />
                </div>
              </div>
              <div className="panel">
                <PanelHeader title="Ruch sieciowy (fizyczne interfejsy)" />
                <div className="p-3">
                  <Chart x={charts.x} series={charts.net} format={(v) => rate(v)} />
                </div>
              </div>
              <div className="panel">
                <PanelHeader title="Dyski I/O" />
                <div className="p-3">
                  <Chart x={charts.x} series={charts.disk} format={(v) => rate(v)} />
                </div>
              </div>
            </div>
          </section>

          <section className="rise grid gap-4 xl:grid-cols-2" style={{ animationDelay: "180ms" }}>
            <div className="panel">
              <PanelHeader title="Systemy plików" />
              <div className="divide-y divide-rule">
                {(m?.disks ?? []).map((d) => (
                  <div key={`${d.device}${d.mountpoint}`} className="grid grid-cols-[1fr_auto] items-center gap-x-4 gap-y-1.5 px-4 py-3">
                    <div className="min-w-0">
                      <span className="readout text-sm">{d.mountpoint}</span>{" "}
                      <span className="readout text-xs text-ink-3">
                        {d.device} · {d.fstype}
                      </span>
                    </div>
                    <span className="readout text-xs text-ink-2">
                      {bytes(d.used_bytes)} / {bytes(d.total_bytes)}
                    </span>
                    <div className="col-span-2">
                      <SegmentMeter value={d.used_percent ?? 0} segments={40} />
                    </div>
                  </div>
                ))}
                {!m?.disks?.length && <div className="px-4 py-3 text-sm text-ink-3">brak danych</div>}
              </div>
            </div>
            <div className="panel">
              <PanelHeader title="Interfejsy sieciowe" />
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="text-left">
                      {["interfejs", "rx/s", "tx/s", "rx łącznie", "tx łącznie", "błędy"].map((c) => (
                        <th key={c} className="label px-4 py-2 font-normal">
                          {c}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="readout divide-y divide-rule">
                    {(m?.network ?? []).map((n) => (
                      <tr key={n.name} className={n.virtual ? "text-ink-3" : ""}>
                        <td className="px-4 py-2">
                          {n.name}
                          {n.virtual && <span className="label ml-2">virt</span>}
                        </td>
                        <td className="px-4 py-2 text-cyan">{rate(n.rx_bytes_per_second)}</td>
                        <td className="px-4 py-2 text-amber">{rate(n.tx_bytes_per_second)}</td>
                        <td className="px-4 py-2">{bytes(n.rx_bytes_total)}</td>
                        <td className="px-4 py-2">{bytes(n.tx_bytes_total)}</td>
                        <td className={`px-4 py-2 ${(n.rx_errors ?? 0) + (n.tx_errors ?? 0) > 0 ? "text-alarm" : ""}`}>{(n.rx_errors ?? 0) + (n.tx_errors ?? 0)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {!!m?.temperatures?.length && (
                <div className="border-t border-rule p-4">
                  <div className="label mb-2">temperatury</div>
                  <div className="flex flex-wrap gap-2">
                    {m.temperatures.map((t) => (
                      <span key={t.sensor} className="readout border border-rule px-2 py-1 text-xs">
                        {t.sensor} <span className={(t.celsius ?? 0) > 80 ? "text-alarm" : "text-amber"}>{t.celsius?.toFixed(0)}°C</span>
                      </span>
                    ))}
                  </div>
                </div>
              )}
            </div>
          </section>
        </>
      )}
    </div>
  );
}

/** Shared header of all host pages: identity, status, admin actions, tabs. */
export function HostHeader({ host: h, isAdmin }: { host: Host; isAdmin: boolean }) {
  const tabs = [
    { to: "/hosts/$id", label: "Przegląd" },
    { to: "/hosts/$id/containers", label: "Kontenery", count: h.info?.docker?.containers_total },
    { to: "/hosts/$id/stacks", label: "Stacki" },
    { to: "/hosts/$id/images", label: "Obrazy", count: h.info?.docker?.images },
    { to: "/hosts/$id/networks", label: "Sieci" },
    { to: "/hosts/$id/volumes", label: "Wolumeny" },
    ...(h.info?.host_features?.length ? [{ to: "/hosts/$id/system", label: "System" } as const] : []),
  ] as const;
  return (
    <>
      <header className="rise flex flex-wrap items-end justify-between gap-4">
        <div className="min-w-0">
          <Link to="/" className="label hover:!text-amber">
            ← 01 / hosty
          </Link>
          <div className="mt-1 flex flex-wrap items-center gap-4">
            <h1 className="font-display text-2xl font-semibold tracking-wider">{h.name}</h1>
            <StatusLamp status={h.status} />
          </div>
          <div className="readout mt-1 text-xs text-ink-3">
            {h.info?.hostname ?? h.hostname ?? "—"} · {h.address} · id {h.id}
          </div>
        </div>
        {isAdmin && <HostActions hostId={h.id} name={h.name} address={h.address} online={h.status === "online"} />}
      </header>
      {h.error && h.status !== "online" && <ErrorText error={`Ostatni błąd połączenia: ${h.error}`} />}
      {h.status !== "pending" && (
        <nav className="flex gap-1 overflow-x-auto overflow-y-hidden border-b border-rule">
          {tabs.map((t) => (
            <Link
              key={t.to}
              to={t.to}
              params={{ id: h.id }}
              activeOptions={{ exact: t.to === "/hosts/$id" }}
              className="-mb-px border-b-2 border-transparent px-4 py-2 font-display text-xs font-semibold tracking-[0.14em] text-ink-3 uppercase transition-colors hover:text-ink data-[status=active]:border-amber data-[status=active]:text-ink"
            >
              {t.label}
              {"count" in t && t.count !== undefined && <span className="readout ml-2 text-ink-3">{t.count}</span>}
            </Link>
          ))}
        </nav>
      )}
    </>
  );
}

/** Loads the host for nested host pages and renders the shared header. */
export function useHost(id: string) {
  return useQuery({ queryKey: ["host", id], queryFn: () => api.host(id) });
}

function HostActions({ hostId, name, address, online }: { hostId: string; name: string; address: string; online: boolean }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [modal, setModal] = useState<null | "edit" | "delete" | "enroll">(null);
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const [form, setForm] = useState({ name, address });
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["hosts"] });
    qc.invalidateQueries({ queryKey: ["host", hostId] });
  };
  const rotate = useMutation({ mutationFn: () => api.rotateCert(hostId), onSuccess: refresh });
  const update = useMutation({ mutationFn: () => api.updateHost(hostId, form), onSuccess: () => (refresh(), setModal(null)) });
  const enroll = useMutation({ mutationFn: () => api.newEnrollment(hostId), onSuccess: (e) => (setEnrollment(e), setModal("enroll"), refresh()) });
  const del = useMutation({
    mutationFn: () => api.deleteHost(hostId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["hosts"] });
      navigate({ to: "/" });
    },
  });

  return (
    <div className="flex flex-wrap items-center gap-2">
      <button className="btn" onClick={() => rotate.mutate()} disabled={!online || rotate.isPending} title="Wymusza wymianę certyfikatu mTLS agenta">
        {rotate.isPending ? "rotacja…" : rotate.isSuccess ? "cert ✓" : "rotuj cert"}
      </button>
      <button className="btn" onClick={() => enroll.mutate()} disabled={enroll.isPending}>
        nowy pakiet
      </button>
      <button className="btn" onClick={() => setModal("edit")}>
        edytuj
      </button>
      <button className="btn btn-danger" onClick={() => setModal("delete")}>
        usuń
      </button>
      <div className="basis-full">
        <ErrorText error={rotate.error ?? enroll.error} />
      </div>

      {modal === "edit" && (
        <Modal title="Edycja hosta" onClose={() => setModal(null)}>
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              update.mutate();
            }}
          >
            <Field label="Nazwa">
              <input className="field" required value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </Field>
            <Field label="Adres agenta">
              <input className="field readout" required value={form.address} onChange={(e) => setForm({ ...form, address: e.target.value })} />
            </Field>
            <ErrorText error={update.error} />
            <div className="flex justify-end">
              <button className="btn btn-primary" disabled={update.isPending}>
                zapisz
              </button>
            </div>
          </form>
        </Modal>
      )}
      {modal === "delete" && (
        <Modal title="Usunąć host?" onClose={() => setModal(null)}>
          <p className="text-sm text-ink-2">
            Hub rozłączy agenta <strong className="text-ink">{name}</strong> i unieważni wszystkie jego certyfikaty. Ponowne podłączenie wymaga nowej
            rejestracji. Kontenery na serwerze nie zostaną naruszone.
          </p>
          <ErrorText error={del.error} />
          <div className="mt-5 flex justify-end gap-2">
            <button className="btn" onClick={() => setModal(null)}>
              anuluj
            </button>
            <button className="btn btn-danger" onClick={() => del.mutate()} disabled={del.isPending}>
              usuń i unieważnij
            </button>
          </div>
        </Modal>
      )}
      {modal === "enroll" && enrollment && (
        <Modal title={`Rejestracja · ${name}`} onClose={() => setModal(null)}>
          <p className="mb-4 border-l-2 border-warn bg-warn/10 px-3 py-2 text-sm text-warn">
            Po rejestracji z nowym pakietem dotychczasowe certyfikaty agenta zostaną unieważnione. Usuń wolumen danych agenta przed ponowną rejestracją.
          </p>
          <EnrollInstructions host={{ id: hostId, name, address, status: "pending", created_at: "" }} enrollment={enrollment} />
        </Modal>
      )}
    </div>
  );
}
