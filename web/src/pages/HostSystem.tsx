import { useLayoutEffect, useMemo, useRef, useState } from "react";
import { useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, hostApi, wsUrl } from "../lib/api";
import { useJsonSocket } from "../lib/ws";
import { ago, bytes, dateTime, pct } from "../lib/format";
import type { Host, HostProcess, Me, SystemdUnit } from "../lib/types";
import { Confirm, ErrorText, PanelHeader } from "../components/ui";
import { HostPage, Th } from "./HostPage";

type Tab = "processes" | "units" | "packages";
const tabLabels: Record<Tab, string> = { processes: "Procesy", units: "Usługi systemd", packages: "Aktualizacje pakietów" };
const featureOf: Record<Tab, string> = { processes: "processes", units: "systemd", packages: "packages" };

export function HostSystemPage({ me }: { me: Me }) {
  const { id } = useParams({ from: "/hosts/$id/system" });
  return <HostPage id={id} me={me}>{(h) => <SystemView host={h} me={me} />}</HostPage>;
}

function SystemView({ host, me }: { host: Host; me: Me }) {
  const features = host.info?.host_features ?? [];
  const tabs = (Object.keys(tabLabels) as Tab[]).filter((t) => features.includes(featureOf[t]));
  const [tab, setTab] = useState<Tab | undefined>(tabs[0]);
  if (!tabs.length) {
    return (
      <div className="panel p-6 text-sm text-ink-2">
        Funkcje hosta są wyłączone na tym agencie. Aby je włączyć, uruchom agenta z plikiem{" "}
        <code className="readout text-amber">compose.host-features.yaml</code>:
        <pre className="readout mt-3 bg-chassis p-3 text-xs text-ink">docker compose -f compose.yaml -f compose.host-features.yaml up -d</pre>
      </div>
    );
  }
  const active = tab && tabs.includes(tab) ? tab : tabs[0];
  return (
    <div className="space-y-5">
      <div className="flex border border-rule self-start w-fit">
        {tabs.map((t) => (
          <button key={t} onClick={() => setTab(t)} className={`label px-3 py-1.5 ${t === active ? "bg-amber !text-chassis" : "hover:!text-ink"}`}>
            {tabLabels[t]}
          </button>
        ))}
      </div>
      {active === "processes" && <Processes hostId={host.id} me={me} />}
      {active === "units" && <Units hostId={host.id} me={me} />}
      {active === "packages" && <Packages hostId={host.id} />}
    </div>
  );
}

// ---------------------------------------------------------------- processes

type SortKey = "cpu" | "mem" | "pid" | "name";

function Processes({ hostId, me }: { hostId: string; me: Me }) {
  const [live, setLive] = useState(true);
  const q = useQuery({ queryKey: ["processes", hostId], queryFn: () => hostApi.processes(hostId), refetchInterval: live ? 5000 : false, enabled: me.user.role !== "viewer" });
  const containers = useQuery({ queryKey: ["containers", hostId], queryFn: () => api.containers(hostId) });
  const names = useMemo(() => new Map((containers.data ?? []).map((c) => [c.id, c.name])), [containers.data]);
  const [filter, setFilter] = useState("");
  const [sort, setSort] = useState<SortKey>("cpu");
  const [target, setTarget] = useState<{ p: HostProcess; sig: "term" | "kill" } | null>(null);
  const signal = useMutation({ mutationFn: (t: { p: HostProcess; sig: "term" | "kill" }) => hostApi.signal(hostId, t.p, t.sig), onSuccess: () => (setTarget(null), q.refetch()) });
  const isAdmin = me.user.role === "admin";

  const list = useMemo(() => {
    const f = filter.toLowerCase();
    const out = (q.data?.processes ?? []).filter(
      (p) => !f || String(p.pid) === f || p.name?.toLowerCase().includes(f) || p.command?.toLowerCase().includes(f) || p.user?.toLowerCase().includes(f) || (p.container_id && names.get(p.container_id)?.toLowerCase().includes(f)),
    );
    const by: Record<SortKey, (a: HostProcess, b: HostProcess) => number> = {
      cpu: (a, b) => (b.cpu_percent ?? 0) - (a.cpu_percent ?? 0),
      mem: (a, b) => (b.memory_rss_bytes ?? 0) - (a.memory_rss_bytes ?? 0),
      pid: (a, b) => a.pid - b.pid,
      name: (a, b) => (a.name ?? "").localeCompare(b.name ?? ""),
    };
    return out.sort(by[sort]).slice(0, 300);
  }, [q.data, filter, sort, names]);

  if (me.user.role === "viewer") return <div className="panel p-6 text-sm text-ink-2">Lista procesów (z liniami poleceń) jest dostępna od roli operator.</div>;
  const total = q.data?.processes.length ?? 0;
  const cpuSum = (q.data?.processes ?? []).reduce((s, p) => s + (p.cpu_percent ?? 0), 0);
  const head = (k: SortKey, label: string) => (
    <Th>
      <button className={`label ${sort === k ? "!text-amber" : "hover:!text-ink"}`} onClick={() => setSort(k)}>
        {label}
        {sort === k && " ▾"}
      </button>
    </Th>
  );

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-4">
          <input className="field w-72 !py-1.5" placeholder="pid, nazwa, polecenie, użytkownik, kontener…" value={filter} onChange={(e) => setFilter(e.target.value)} />
          <label className="label flex cursor-pointer items-center gap-1.5">
            <input type="checkbox" className="accent-amber" checked={live} onChange={(e) => setLive(e.target.checked)} />
            odświeżaj co 5 s
          </label>
        </div>
        <span className="readout text-xs text-ink-3">
          {total} procesów · suma CPU {pct(cpuSum / Math.max(q.data?.cpu_cores ?? 1, 1))} z {q.data?.cpu_cores ?? "?"} rdzeni
        </span>
      </div>
      <ErrorText error={q.error ?? signal.error} />
      <div className="panel overflow-x-auto">
        <table className="w-full min-w-[960px] text-sm">
          <thead>
            <tr>
              {head("pid", "pid")}
              <Th>użytkownik</Th>
              {head("name", "proces")}
              {head("cpu", "cpu")}
              {head("mem", "pamięć")}
              <Th>start</Th>
              <Th />
            </tr>
          </thead>
          <tbody className="readout divide-y divide-rule text-xs">
            {list.map((p) => {
              const container = p.container_id ? names.get(p.container_id) ?? p.container_id.slice(0, 12) : "";
              return (
                <tr key={p.pid} className="hover:bg-panel-2">
                  <td className="px-4 py-1.5 text-ink-3">{p.pid}</td>
                  <td className={`px-4 py-1.5 ${p.user === "root" ? "text-warn" : "text-ink-2"}`}>{p.user}</td>
                  <td className="max-w-[520px] px-4 py-1.5">
                    <div className="flex items-center gap-2">
                      <span className="text-ink">{p.name}</span>
                      {container && <span className="label border border-rule px-1 !text-cyan">{container}</span>}
                      {p.status === "zombie" && <span className="label !text-alarm">zombie</span>}
                    </div>
                    <div className="truncate text-ink-3" title={p.command}>
                      {p.command}
                    </div>
                  </td>
                  <td className="px-4 py-1.5">
                    <span className={(p.cpu_percent ?? 0) > 50 ? "text-alarm" : (p.cpu_percent ?? 0) > 10 ? "text-amber" : ""}>{pct(p.cpu_percent)}</span>
                  </td>
                  <td className="px-4 py-1.5">{bytes(p.memory_rss_bytes)}</td>
                  <td className="px-4 py-1.5 text-ink-3">{p.started_unix ? ago(new Date(p.started_unix * 1000).toISOString()) : "—"}</td>
                  <td className="px-4 py-1.5 text-right">
                    {isAdmin && (
                      <div className="flex justify-end gap-1">
                        <button className="btn !px-1.5 !py-0 !text-[10px]" onClick={() => setTarget({ p, sig: "term" })}>
                          term
                        </button>
                        <button className="btn btn-danger !px-1.5 !py-0 !text-[10px]" onClick={() => setTarget({ p, sig: "kill" })}>
                          kill
                        </button>
                      </div>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {total > 300 && <p className="label">pokazano 300 z {total} — zawęź filtrem</p>}
      {target && (
        <Confirm
          title={`${target.sig === "kill" ? "SIGKILL" : "SIGTERM"} · ${target.p.name} (${target.p.pid})`}
          confirmLabel={target.sig}
          danger
          pending={signal.isPending}
          error={signal.error}
          onClose={() => setTarget(null)}
          onConfirm={() => signal.mutate(target)}
        >
          <p className="readout break-all text-xs">{target.p.command}</p>
          <p>{target.sig === "kill" ? "Proces zostanie natychmiast zabity, bez możliwości zapisania stanu." : "Proces otrzyma prośbę o zakończenie (SIGTERM)."}</p>
          {target.p.container_id && <p className="text-warn">To proces kontenera — lepiej zatrzymać kontener.</p>}
        </Confirm>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- systemd

type UnitFilter = "all" | "active" | "failed" | "inactive";

function stateColor(u: SystemdUnit) {
  if (u.active_state === "failed") return "var(--color-alarm)";
  if (u.active_state === "active") return "var(--color-signal)";
  if (u.active_state === "activating" || u.active_state === "deactivating" || u.active_state === "reloading") return "var(--color-warn)";
  return "var(--color-ink-3)";
}

function Units({ hostId, me }: { hostId: string; me: Me }) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["units", hostId], queryFn: () => hostApi.units(hostId), refetchInterval: 30_000 });
  const [filter, setFilter] = useState<UnitFilter>("active");
  const [search, setSearch] = useState("");
  const [journal, setJournal] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<{ unit: SystemdUnit; action: "stop" | "restart" } | null>(null);
  const action = useMutation({
    mutationFn: (v: { unit: string; action: "start" | "stop" | "restart" | "reload" }) => hostApi.unitAction(hostId, v.unit, v.action),
    onSuccess: () => (setConfirm(null), qc.invalidateQueries({ queryKey: ["units", hostId] })),
  });
  const isAdmin = me.user.role === "admin";
  const canReadJournal = me.user.role !== "viewer";

  const all = q.data ?? [];
  const failed = all.filter((u) => u.active_state === "failed").length;
  const list = all.filter((u) => {
    if (filter === "active" && u.active_state !== "active") return false;
    if (filter === "failed" && u.active_state !== "failed") return false;
    if (filter === "inactive" && u.active_state === "active") return false;
    const s = search.toLowerCase();
    return !s || u.name.toLowerCase().includes(s) || u.description?.toLowerCase().includes(s);
  });

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex border border-rule">
          {(
            [
              ["active", "aktywne"],
              ["failed", `błędy${failed ? ` (${failed})` : ""}`],
              ["inactive", "nieaktywne"],
              ["all", "wszystkie"],
            ] as [UnitFilter, string][]
          ).map(([f, l]) => (
            <button key={f} onClick={() => setFilter(f)} className={`label px-3 py-1.5 ${f === filter ? "bg-amber !text-chassis" : f === "failed" && failed ? "!text-alarm" : "hover:!text-ink"}`}>
              {l}
            </button>
          ))}
        </div>
        <input className="field w-72 !py-1.5" placeholder="szukaj usługi…" value={search} onChange={(e) => setSearch(e.target.value)} />
      </div>
      <ErrorText error={q.error ?? action.error} />
      {journal && <JournalViewer hostId={hostId} unit={journal} onClose={() => setJournal(null)} />}
      <div className="panel overflow-x-auto">
        <table className="w-full min-w-[860px] text-sm">
          <thead>
            <tr>
              <Th>usługa</Th>
              <Th>stan</Th>
              <Th>autostart</Th>
              <Th />
            </tr>
          </thead>
          <tbody className="divide-y divide-rule">
            {list.map((u) => (
              <tr key={u.name} className="hover:bg-panel-2">
                <td className="max-w-[440px] px-4 py-2">
                  <div className="readout text-xs text-ink">
                    {u.name}
                    {u.protected && <span className="label ml-2 border border-rule px-1 !text-amber-dim">chroniona</span>}
                  </div>
                  <div className="truncate text-xs text-ink-3">{u.description}</div>
                </td>
                <td className="px-4 py-2">
                  <span className="inline-flex items-center gap-2">
                    <span className="inline-block size-2 rounded-full" style={{ background: stateColor(u) }} />
                    <span className="label" style={{ color: stateColor(u) }}>
                      {u.active_state}
                    </span>
                    <span className="readout text-xs text-ink-3">{u.sub_state}</span>
                  </span>
                </td>
                <td className="readout px-4 py-2 text-xs text-ink-2">{u.unit_file_state || "—"}</td>
                <td className="px-4 py-2">
                  <div className="flex justify-end gap-1.5">
                    {canReadJournal && (
                      <button className="btn !px-2 !py-0.5 !text-[10px]" onClick={() => setJournal(u.name)}>
                        logi
                      </button>
                    )}
                    {isAdmin && u.active_state !== "active" && (
                      <button className="btn !px-2 !py-0.5 !text-[10px]" disabled={action.isPending} onClick={() => action.mutate({ unit: u.name, action: "start" })}>
                        start
                      </button>
                    )}
                    {isAdmin && !u.protected && u.active_state === "active" && (
                      <>
                        <button className="btn !px-2 !py-0.5 !text-[10px]" onClick={() => setConfirm({ unit: u, action: "restart" })}>
                          restart
                        </button>
                        <button className="btn btn-danger !px-2 !py-0.5 !text-[10px]" onClick={() => setConfirm({ unit: u, action: "stop" })}>
                          stop
                        </button>
                      </>
                    )}
                  </div>
                </td>
              </tr>
            ))}
            {q.data && list.length === 0 && (
              <tr>
                <td colSpan={4} className="px-4 py-6 text-center text-sm text-ink-3">
                  brak
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {confirm && (
        <Confirm
          title={`${confirm.action} · ${confirm.unit.name}`}
          confirmLabel={confirm.action}
          danger
          pending={action.isPending}
          error={action.error}
          onClose={() => setConfirm(null)}
          onConfirm={() => action.mutate({ unit: confirm.unit.name, action: confirm.action })}
        >
          <p>
            Usługa <span className="readout text-ink">{confirm.unit.name}</span> zostanie {confirm.action === "stop" ? "zatrzymana" : "zrestartowana"} na hoście.
          </p>
        </Confirm>
      )}
    </div>
  );
}

function JournalViewer({ hostId, unit, onClose }: { hostId: string; unit: string; onClose: () => void }) {
  const [lines, setLines] = useState<string[]>([]);
  const [status, setStatus] = useState<string | null>(null);
  const box = useRef<HTMLDivElement>(null);
  const url = useMemo(() => wsUrl(`/hosts/${hostId}/journal`, { unit, lines: 300 }), [hostId, unit]);
  useJsonSocket<{ lines?: string[]; error?: string; end?: boolean }>(
    url,
    (m) => {
      if (m.lines) setLines((p) => p.concat(m.lines!).slice(-5000));
      if (m.error) setStatus(m.error);
    },
    { reconnect: false },
  );
  useLayoutEffect(() => {
    if (box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines]);
  return (
    <section className="panel">
      <PanelHeader title={`journalctl -u ${unit}`}>
        <button className="label hover:!text-ink" onClick={onClose}>
          zamknij ✕
        </button>
      </PanelHeader>
      <div ref={box} className="readout h-80 overflow-auto bg-chassis px-3 py-2 text-[12px] leading-[1.55] break-all whitespace-pre-wrap">
        {lines.map((l, i) => (
          <div key={i} className={/error|fail|denied|fatal/i.test(l) ? "text-[#ff9a8f]" : "text-ink-2"}>
            <span className="text-ink-3">{l.slice(0, 25)}</span>
            {l.slice(25)}
          </div>
        ))}
        {status && <div className="text-alarm">{status}</div>}
        {!lines.length && !status && <div className="label">ładowanie…</div>}
      </div>
    </section>
  );
}

// ---------------------------------------------------------------- packages

function Packages({ hostId }: { hostId: string }) {
  const q = useQuery({ queryKey: ["packages", hostId], queryFn: () => hostApi.packages(hostId), staleTime: 5 * 60_000 });
  const [search, setSearch] = useState("");
  if (q.error) return <ErrorText error={q.error} />;
  const d = q.data;
  if (!d) return <div className="label">odczyt stanu pakietów hosta…</div>;
  const security = d.updates.filter((u) => u.security).length;
  const stale = d.lists_updated_unix ? Date.now() / 1000 - d.lists_updated_unix > 3 * 86400 : true;
  const list = d.updates.filter((u) => !search || u.name.includes(search.toLowerCase()));
  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-4">
        {[
          ["system", d.distribution || "—", ""],
          ["do aktualizacji", String(d.updates.length), d.updates.length ? "text-amber" : "text-signal"],
          ["bezpieczeństwa", String(security), security ? "text-alarm" : "text-signal"],
          ["listy pakietów", d.lists_updated_unix ? dateTime(d.lists_updated_unix * 1000) : "nieznane", stale ? "text-warn" : ""],
        ].map(([l, v, c]) => (
          <div key={l} className="panel px-4 py-3">
            <div className="label">{l}</div>
            <div className={`readout mt-1 truncate text-lg ${c}`}>{v}</div>
          </div>
        ))}
      </div>
      {d.reboot_required && (
        <div className="border-l-2 border-warn bg-warn/10 px-4 py-2.5 text-sm text-warn">
          Host wymaga restartu{d.reboot_packages.length ? ` (${d.reboot_packages.join(", ")})` : ""}.
        </div>
      )}
      {stale && <p className="text-xs text-warn">Listy pakietów są starsze niż 3 dni — wyniki mogą być nieaktualne (na hoście: sudo apt update).</p>}
      <p className="text-xs text-ink-3">
        Podgląd tylko do odczytu. Aktualizację wykonaj na hoście: <code className="readout text-ink-2">sudo apt upgrade</code> (lub pozwól działać unattended-upgrades).
      </p>
      <div className="flex justify-end">
        <input className="field w-64 !py-1.5" placeholder="szukaj pakietu…" value={search} onChange={(e) => setSearch(e.target.value)} />
      </div>
      <div className="panel overflow-x-auto">
        <table className="w-full min-w-[760px] text-sm">
          <thead>
            <tr>
              <Th>pakiet</Th>
              <Th>obecna</Th>
              <Th>nowa</Th>
              <Th>źródło</Th>
            </tr>
          </thead>
          <tbody className="readout divide-y divide-rule text-xs">
            {list.map((u) => (
              <tr key={u.name} className="hover:bg-panel-2">
                <td className="px-4 py-1.5">
                  {u.name}
                  {u.security && <span className="label ml-2 !text-alarm">security</span>}
                </td>
                <td className="px-4 py-1.5 text-ink-3">{u.current_version}</td>
                <td className="px-4 py-1.5 text-signal">{u.new_version}</td>
                <td className="px-4 py-1.5 text-ink-3">{u.source}</td>
              </tr>
            ))}
            {list.length === 0 && (
              <tr>
                <td colSpan={4} className="px-4 py-6 text-center text-sm text-signal">
                  wszystko aktualne
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
