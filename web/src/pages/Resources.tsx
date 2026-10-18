import { useMemo, useState } from "react";
import { useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, resources } from "../lib/api";
import { bytes, dateTime } from "../lib/format";
import type { DockerImage, DockerNetwork, DockerVolume, ImageUpdateInfo, Me, PruneResult, PullProgress } from "../lib/types";
import { CheckUpdatesButton, UpdateBadge, useHostUpdates } from "../components/updates";
import { Confirm, ErrorText, Field, Modal } from "../components/ui";
import { HostPage, Th, Toolbar } from "./HostPage";

const shortId = (id: string) => id.replace(/^sha256:/, "").slice(0, 12);
const unix = (s?: number) => (s ? dateTime(s * 1000) : "—");

function PruneNote({ r }: { r: PruneResult | undefined }) {
  if (!r) return null;
  return (
    <p className="rounded-[var(--radius-control)] bg-signal/10 px-3 py-2 text-sm text-signal">
      Usunięto {r.deleted?.length ?? 0} {r.space_reclaimed_bytes ? `· odzyskano ${bytes(r.space_reclaimed_bytes)}` : ""}
    </p>
  );
}

function Search({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder: string }) {
  return <input className="field w-72 !py-1.5" placeholder={placeholder} value={value} onChange={(e) => onChange(e.target.value)} />;
}

// ---------------------------------------------------------------- images

export function ImagesPage({ me }: { me: Me }) {
  const { id } = useParams({ from: "/hosts/$id/images" });
  return <HostPage id={id} me={me}>{() => <Images hostId={id} me={me} />}</HostPage>;
}

type LayerState = Record<string, { status: string; current?: number; total?: number }>;

function Images({ hostId, me }: { hostId: string; me: Me }) {
  const qc = useQueryClient();
  const images = useQuery({ queryKey: ["images", hostId], queryFn: () => resources.images(hostId) });
  const upd = useHostUpdates(hostId);
  const [q, setQ] = useState("");
  const [ref, setRef] = useState("");
  const [pull, setPull] = useState<{ running: boolean; layers: LayerState; status: string[]; error?: string } | null>(null);
  const [remove, setRemove] = useState<DockerImage | null>(null);
  const [force, setForce] = useState(false);
  const [prune, setPrune] = useState<null | "dangling" | "all">(null);
  const isAdmin = me.user.role === "admin";
  const canOperate = me.user.role !== "viewer";

  const doPull = async () => {
    const layers: LayerState = {};
    const status: string[] = [];
    let error: string | undefined;
    setPull({ running: true, layers, status });
    try {
      await resources.pullImage(hostId, ref.trim(), (m: PullProgress) => {
        if (m.error) error = m.error;
        else if (m.id && !m.done) layers[m.id] = { status: m.status ?? "", current: m.current, total: m.total };
        else if (m.status) status.push(m.status);
        setPull({ running: true, layers: { ...layers }, status: [...status], error });
      });
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
    setPull({ running: false, layers, status, error });
    qc.invalidateQueries({ queryKey: ["images", hostId] });
  };

  const del = useMutation({
    mutationFn: (im: DockerImage) => resources.removeImage(hostId, im.id, force, im.repo_tags?.[0]),
    onSuccess: () => {
      setRemove(null);
      qc.invalidateQueries({ queryKey: ["images", hostId] });
    },
  });
  const pruneM = useMutation({
    mutationFn: (all: boolean) => resources.pruneImages(hostId, all),
    onSuccess: () => {
      setPrune(null);
      qc.invalidateQueries({ queryKey: ["images", hostId] });
    },
  });

  const list = useMemo(() => {
    const s = q.toLowerCase();
    return (images.data ?? []).filter((im) => !s || im.id.includes(s) || im.repo_tags?.some((t) => t.toLowerCase().includes(s)));
  }, [images.data, q]);
  const total = (images.data ?? []).reduce((a, im) => a + (im.size_bytes ?? 0), 0);
  const unused = (images.data ?? []).filter((im) => !im.containers).length;

  return (
    <div className="space-y-5">
      {canOperate && (
        <form
          className="panel flex flex-wrap items-end gap-3 p-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (ref.trim()) doPull();
          }}
        >
          <div className="min-w-[280px] flex-1">
            <Field label="Pobierz obraz">
              <input className="field readout" placeholder="np. nginx:1.27-alpine, ghcr.io/org/app:tag" value={ref} onChange={(e) => setRef(e.target.value)} />
            </Field>
          </div>
          <button className="btn btn-primary" disabled={pull?.running || !ref.trim()}>
            {pull?.running ? "Pobieranie…" : "Pull"}
          </button>
          {pull && (
            <div className="readout basis-full space-y-1 text-xs">
              {Object.entries(pull.layers).map(([lid, l]) => (
                <div key={lid} className="grid grid-cols-[110px_160px_1fr] items-center gap-3">
                  <span className="text-ink-3">{lid}</span>
                  <span className="text-ink-2">{l.status}</span>
                  {l.total ? (
                    <div className="h-1 overflow-hidden rounded-full bg-rule">
                      <div className="h-full bg-accent" style={{ width: `${Math.min(((l.current ?? 0) / l.total) * 100, 100)}%` }} />
                    </div>
                  ) : (
                    <span />
                  )}
                </div>
              ))}
              {pull.status.map((s, i) => (
                <div key={i} className="text-signal">
                  {s}
                </div>
              ))}
              {pull.error && <div className="text-alarm">{pull.error}</div>}
            </div>
          )}
        </form>
      )}

      <Toolbar>
        <div className="flex flex-wrap items-center gap-4">
          <Search value={q} onChange={setQ} placeholder="szukaj tagu lub id…" />
          <CheckUpdatesButton hostId={hostId} checkedAt={upd.checkedAt} canOperate={canOperate} />
          <span className="readout text-xs text-ink-3">
            {images.data?.length ?? 0} obrazów · {bytes(total)} · {unused} nieużywanych
          </span>
        </div>
        {isAdmin && (
          <div className="flex gap-2">
            <button className="btn" onClick={() => setPrune("dangling")}>
              Usuń wiszące
            </button>
            <button className="btn btn-danger" onClick={() => setPrune("all")}>
              Usuń nieużywane
            </button>
          </div>
        )}
      </Toolbar>
      <PruneNote r={pruneM.data} />
      <ErrorText error={images.error} />
      {upd.available.length > 0 && (
        <div className="panel p-4 shadow-[inset_3px_0_0_var(--color-cyan),var(--card-shadow)]">
          <div className="label mb-2 !text-cyan">dostępne aktualizacje ({upd.available.length})</div>
          <ul className="space-y-1 text-sm">
            {upd.available.map((u) => (
              <li key={u.reference} className="flex flex-wrap gap-x-3">
                <span className="readout">{u.display}</span>
                <span className="text-ink-3">używają: {u.containers.join(", ") || "—"}</span>
              </li>
            ))}
          </ul>
          <p className="mt-2 text-xs text-ink-3">Aby zaktualizować, otwórz stack i wykonaj „pull”, a potem „up” — albo „up” z opcją pobrania obrazów.</p>
        </div>
      )}

      <div className="panel overflow-x-auto">
        <table className="w-full min-w-[760px] text-sm">
          <thead>
            <tr>
              <Th>repozytorium:tag</Th>
              <Th>id</Th>
              <Th>rozmiar</Th>
              <Th>utworzony</Th>
              <Th>użycie</Th>
              <Th>wersja</Th>
              <Th />
            </tr>
          </thead>
          <tbody className="divide-y divide-rule">
            {list.map((im) => (
              <tr key={im.id} className="hover:bg-panel-2">
                <td className="px-4 py-2">
                  {im.repo_tags?.length ? (
                    im.repo_tags.map((t) => (
                      <div key={t} className="readout text-xs">
                        {t}
                      </div>
                    ))
                  ) : (
                    <span className="label !text-warn">&lt;none&gt; · wiszący</span>
                  )}
                </td>
                <td className="readout px-4 py-2 text-xs text-ink-3">{shortId(im.id)}</td>
                <td className="readout px-4 py-2 text-xs">{bytes(im.size_bytes)}</td>
                <td className="readout px-4 py-2 text-xs text-ink-3">{unix(im.created_unix)}</td>
                <td className="px-4 py-2">
                  {im.containers ? <span className="label !text-signal">{im.containers} kont.</span> : <span className="label">nieużywany</span>}
                </td>
                <td className="px-4 py-2">
                  <VersionCell u={upd.byImage.get(im.id)} />
                </td>
                <td className="px-4 py-2 text-right">
                  {isAdmin && (
                    <button className="btn btn-danger btn-sm" onClick={() => (setForce(false), setRemove(im))}>
                      Usuń
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {remove && (
        <Confirm title="Usuń obraz" confirmLabel="Usuń" danger pending={del.isPending} error={del.error} onClose={() => setRemove(null)} onConfirm={() => del.mutate(remove)}>
          <p className="readout">{remove.repo_tags?.join(", ") || shortId(remove.id)}</p>
          {!!remove.containers && <p className="text-warn">Obraz jest używany przez {remove.containers} kontener(y) — usunięcie wymaga wymuszenia.</p>}
          <label className="flex items-center gap-2">
            <input type="checkbox" className="accent-accent" checked={force} onChange={(e) => setForce(e.target.checked)} />
            wymuś (usuń wszystkie tagi)
          </label>
        </Confirm>
      )}
      {prune && (
        <Confirm
          title={prune === "all" ? "Usuń wszystkie nieużywane obrazy" : "Usuń wiszące obrazy"}
          confirmLabel="Usuń"
          danger
          typeToConfirm={prune === "all" ? "prune" : undefined}
          pending={pruneM.isPending}
          error={pruneM.error}
          onClose={() => setPrune(null)}
          onConfirm={() => pruneM.mutate(prune === "all")}
        >
          {prune === "all" ? (
            <p>Usunę <strong className="text-ink">każdy obraz, którego nie używa żaden kontener</strong> (również zatrzymany). Trzeba je będzie pobrać ponownie.</p>
          ) : (
            <p>Usunę obrazy bez tagu (&lt;none&gt;), zwykle pozostałości po przebudowach.</p>
          )}
        </Confirm>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- networks

export function NetworksPage({ me }: { me: Me }) {
  const { id } = useParams({ from: "/hosts/$id/networks" });
  return <HostPage id={id} me={me}>{() => <Networks hostId={id} me={me} />}</HostPage>;
}

function Networks({ hostId, me }: { hostId: string; me: Me }) {
  const qc = useQueryClient();
  const networks = useQuery({ queryKey: ["networks", hostId], queryFn: () => resources.networks(hostId) });
  const containers = useQuery({ queryKey: ["containers", hostId], queryFn: () => api.containers(hostId) });
  const [open, setOpen] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [remove, setRemove] = useState<DockerNetwork | null>(null);
  const [prune, setPrune] = useState(false);
  const [q, setQ] = useState("");
  const isAdmin = me.user.role === "admin";
  const canOperate = me.user.role !== "viewer";
  const invalidate = () => qc.invalidateQueries({ queryKey: ["networks", hostId] });

  const del = useMutation({ mutationFn: (n: DockerNetwork) => resources.removeNetwork(hostId, n.id, n.name), onSuccess: () => (setRemove(null), invalidate()) });
  const pruneM = useMutation({ mutationFn: () => resources.pruneNetworks(hostId), onSuccess: () => (setPrune(false), invalidate()) });
  const disconnect = useMutation({ mutationFn: (v: { n: string; c: string }) => resources.disconnectNetwork(hostId, v.n, v.c), onSuccess: invalidate });

  const list = (networks.data ?? []).filter((n) => !q || n.name.toLowerCase().includes(q.toLowerCase()));

  return (
    <div className="space-y-5">
      <Toolbar>
        <Search value={q} onChange={setQ} placeholder="szukaj sieci…" />
        {isAdmin && (
          <div className="flex gap-2">
            <button className="btn" onClick={() => setPrune(true)}>
              Usuń nieużywane
            </button>
            <button className="btn btn-primary" onClick={() => setCreating(true)}>
              Nowa sieć
            </button>
          </div>
        )}
      </Toolbar>
      <PruneNote r={pruneM.data} />
      <ErrorText error={networks.error ?? disconnect.error} />

      <div className="panel overflow-x-auto">
        <table className="w-full min-w-[800px] text-sm">
          <thead>
            <tr>
              <Th>nazwa</Th>
              <Th>sterownik</Th>
              <Th>podsieć</Th>
              <Th>kontenery</Th>
              <Th>właściwości</Th>
              <Th />
            </tr>
          </thead>
          <tbody className="divide-y divide-rule">
            {list.map((n) => (
              <NetworkRow
                key={n.id}
                n={n}
                open={open === n.id}
                onToggle={() => setOpen(open === n.id ? null : n.id)}
                hostId={hostId}
                isAdmin={isAdmin}
                canOperate={canOperate}
                containers={containers.data ?? []}
                onRemove={() => setRemove(n)}
                onDisconnect={(c) => disconnect.mutate({ n: n.id, c })}
              />
            ))}
          </tbody>
        </table>
      </div>

      {creating && <CreateNetwork hostId={hostId} onClose={() => setCreating(false)} />}
      {remove && (
        <Confirm title="Usuń sieć" confirmLabel="Usuń" danger pending={del.isPending} error={del.error} onClose={() => setRemove(null)} onConfirm={() => del.mutate(remove)}>
          <p className="readout">{remove.name}</p>
        </Confirm>
      )}
      {prune && (
        <Confirm title="Usuń nieużywane sieci" confirmLabel="Usuń" danger pending={pruneM.isPending} error={pruneM.error} onClose={() => setPrune(false)} onConfirm={() => pruneM.mutate()}>
          <p>Usunę wszystkie sieci użytkownika, do których nie jest podłączony żaden kontener. Zatrzymane stacki compose odtworzą swoje sieci przy „up”.</p>
        </Confirm>
      )}
    </div>
  );
}

function NetworkRow({
  n,
  open,
  onToggle,
  hostId,
  isAdmin,
  canOperate,
  containers,
  onRemove,
  onDisconnect,
}: {
  n: DockerNetwork;
  open: boolean;
  onToggle: () => void;
  hostId: string;
  isAdmin: boolean;
  canOperate: boolean;
  containers: { id: string; name: string; protected?: boolean }[];
  onRemove: () => void;
  onDisconnect: (container: string) => void;
}) {
  const qc = useQueryClient();
  const [target, setTarget] = useState("");
  const [alias, setAlias] = useState("");
  const connect = useMutation({
    mutationFn: () => resources.connectNetwork(hostId, n.id, target, alias ? [alias] : []),
    onSuccess: () => {
      setTarget("");
      setAlias("");
      qc.invalidateQueries({ queryKey: ["networks", hostId] });
    },
  });
  const eps = n.endpoints ?? [];
  const attached = new Set(eps.map((e) => e.container_id));
  const flags = [n.internal && "internal", n.attachable && "attachable", n.ipv6 && "ipv6", n.builtin && "systemowa", n.compose_project && `compose: ${n.compose_project}`].filter(Boolean);
  return (
    <>
      <tr className="cursor-pointer hover:bg-panel-2" onClick={onToggle}>
        <td className="px-4 py-2">
          <span className="mr-2 text-ink-3">{open ? "▾" : "▸"}</span>
          <span className="font-medium">{n.name}</span>
        </td>
        <td className="readout px-4 py-2 text-xs">{n.driver}</td>
        <td className="readout px-4 py-2 text-xs text-cyan">{n.subnets?.join(", ") || "—"}</td>
        <td className="readout px-4 py-2 text-xs">{eps.length}</td>
        <td className="px-4 py-2">
          <div className="flex flex-wrap gap-1">
            {flags.map((f) => (
              <span key={String(f)} className="label rounded-md border border-rule px-1.5">
                {f}
              </span>
            ))}
          </div>
        </td>
        <td className="px-4 py-2 text-right" onClick={(e) => e.stopPropagation()}>
          {isAdmin && !n.builtin && (
            <button className="btn btn-danger btn-sm" disabled={eps.length > 0} title={eps.length ? "Najpierw odłącz kontenery" : ""} onClick={onRemove}>
              Usuń
            </button>
          )}
        </td>
      </tr>
      {open && (
        <tr className="bg-chassis">
          <td colSpan={6} className="px-6 py-3">
            <div className="readout mb-2 text-xs text-ink-3">
              id {shortId(n.id)} · zakres {n.scope} · brama {n.gateways?.join(", ") || "—"} · utworzona {unix(n.created_unix)}
            </div>
            <table className="w-full text-xs">
              <tbody className="readout divide-y divide-rule">
                {eps.map((e) => (
                  <tr key={e.container_id}>
                    <td className="py-1.5 pr-4 text-ink">{e.container_name}</td>
                    <td className="py-1.5 pr-4 text-cyan">{e.ipv4 || "—"}</td>
                    <td className="py-1.5 pr-4 text-ink-3">{e.mac}</td>
                    <td className="py-1.5 text-right">
                      {canOperate && !n.builtin && (
                        <button className="btn btn-sm" onClick={() => onDisconnect(e.container_id!)}>
                          Odłącz
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
                {eps.length === 0 && (
                  <tr>
                    <td className="py-1.5 text-ink-3">brak podłączonych kontenerów</td>
                  </tr>
                )}
              </tbody>
            </table>
            {canOperate && !["host", "none"].includes(n.name) && (
              <div className="mt-3 flex flex-wrap items-center gap-2">
                <select className="field !w-64 !py-1 text-xs" value={target} onChange={(e) => setTarget(e.target.value)}>
                  <option value="">podłącz kontener…</option>
                  {containers
                    .filter((c) => !attached.has(c.id))
                    .map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name}
                      </option>
                    ))}
                </select>
                <input className="field readout !w-40 !py-1 text-xs" placeholder="alias (opcjonalnie)" value={alias} onChange={(e) => setAlias(e.target.value)} />
                <button className="btn btn-sm" disabled={!target || connect.isPending} onClick={() => connect.mutate()}>
                  Podłącz
                </button>
                <ErrorText error={connect.error} />
              </div>
            )}
          </td>
        </tr>
      )}
    </>
  );
}

function CreateNetwork({ hostId, onClose }: { hostId: string; onClose: () => void }) {
  const qc = useQueryClient();
  const [f, setF] = useState({ name: "", driver: "bridge", internal: false, attachable: false, ipv6: false, subnet: "", gateway: "" });
  const create = useMutation({
    mutationFn: () => resources.createNetwork(hostId, f),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["networks", hostId] });
      onClose();
    },
  });
  return (
    <Modal title="Nowa sieć" onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Nazwa">
            <input className="field readout" required autoFocus value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />
          </Field>
          <Field label="Sterownik">
            <select className="field" value={f.driver} onChange={(e) => setF({ ...f, driver: e.target.value })}>
              {["bridge", "macvlan", "ipvlan"].map((d) => (
                <option key={d}>{d}</option>
              ))}
            </select>
          </Field>
          <Field label="Podsieć (opcjonalnie)">
            <input className="field readout" placeholder="172.40.0.0/24" value={f.subnet} onChange={(e) => setF({ ...f, subnet: e.target.value })} />
          </Field>
          <Field label="Brama (opcjonalnie)">
            <input className="field readout" placeholder="172.40.0.1" value={f.gateway} onChange={(e) => setF({ ...f, gateway: e.target.value })} />
          </Field>
        </div>
        <div className="flex flex-wrap gap-4 text-sm">
          {(
            [
              ["internal", "wewnętrzna (bez wyjścia na zewnątrz)"],
              ["attachable", "attachable"],
              ["ipv6", "IPv6"],
            ] as const
          ).map(([k, l]) => (
            <label key={k} className="flex items-center gap-2">
              <input type="checkbox" className="accent-accent" checked={f[k]} onChange={(e) => setF({ ...f, [k]: e.target.checked })} />
              {l}
            </label>
          ))}
        </div>
        <ErrorText error={create.error} />
        <div className="flex justify-end gap-2">
          <button type="button" className="btn" onClick={onClose}>
            Anuluj
          </button>
          <button className="btn btn-primary" disabled={create.isPending}>
            Utwórz
          </button>
        </div>
      </form>
    </Modal>
  );
}

// ---------------------------------------------------------------- volumes

export function VolumesPage({ me }: { me: Me }) {
  const { id } = useParams({ from: "/hosts/$id/volumes" });
  return <HostPage id={id} me={me}>{() => <Volumes hostId={id} me={me} />}</HostPage>;
}

function Volumes({ hostId, me }: { hostId: string; me: Me }) {
  const qc = useQueryClient();
  const volumes = useQuery({ queryKey: ["volumes", hostId], queryFn: () => resources.volumes(hostId) });
  const [q, setQ] = useState("");
  const [showAnon, setShowAnon] = useState(false);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [remove, setRemove] = useState<DockerVolume | null>(null);
  const [prune, setPrune] = useState<null | "anon" | "all">(null);
  const isAdmin = me.user.role === "admin";
  const invalidate = () => qc.invalidateQueries({ queryKey: ["volumes", hostId] });

  const create = useMutation({ mutationFn: () => resources.createVolume(hostId, name), onSuccess: () => (setCreating(false), setName(""), invalidate()) });
  const del = useMutation({ mutationFn: (v: DockerVolume) => resources.removeVolume(hostId, v.name), onSuccess: () => (setRemove(null), invalidate()) });
  const pruneM = useMutation({ mutationFn: (all: boolean) => resources.pruneVolumes(hostId, all), onSuccess: () => (setPrune(null), invalidate()) });

  const all = volumes.data ?? [];
  const anonCount = all.filter((v) => v.anonymous).length;
  const list = all.filter((v) => (showAnon || !v.anonymous) && (!q || v.name.toLowerCase().includes(q.toLowerCase()) || v.compose_project?.includes(q)));
  const unused = all.filter((v) => !v.used_by?.length).length;

  return (
    <div className="space-y-5">
      <Toolbar>
        <div className="flex flex-wrap items-center gap-4">
          <Search value={q} onChange={setQ} placeholder="szukaj wolumenu lub projektu…" />
          <label className="label flex cursor-pointer items-center gap-1.5">
            <input type="checkbox" className="accent-accent" checked={showAnon} onChange={(e) => setShowAnon(e.target.checked)} />
            anonimowe ({anonCount})
          </label>
          <span className="readout text-xs text-ink-3">
            {all.length} wolumenów · {unused} nieużywanych
          </span>
        </div>
        {isAdmin && (
          <div className="flex gap-2">
            <button className="btn" onClick={() => setPrune("anon")}>
              Usuń anonimowe nieużywane
            </button>
            <button className="btn btn-danger" onClick={() => setPrune("all")}>
              Usuń wszystkie nieużywane
            </button>
            <button className="btn btn-primary" onClick={() => setCreating(true)}>
              Nowy
            </button>
          </div>
        )}
      </Toolbar>
      <PruneNote r={pruneM.data} />
      <ErrorText error={volumes.error} />

      <div className="panel overflow-x-auto">
        <table className="w-full min-w-[800px] text-sm">
          <thead>
            <tr>
              <Th>nazwa</Th>
              <Th>projekt</Th>
              <Th>używany przez</Th>
              <Th>utworzony</Th>
              <Th>ścieżka na hoście</Th>
              <Th />
            </tr>
          </thead>
          <tbody className="divide-y divide-rule">
            {list.map((v) => (
              <tr key={v.name} className="hover:bg-panel-2">
                <td className="readout max-w-[260px] truncate px-4 py-2 text-xs" title={v.name}>
                  {v.anonymous ? <span className="text-ink-3">{v.name.slice(0, 16)}… · anon</span> : v.name}
                </td>
                <td className="readout px-4 py-2 text-xs whitespace-nowrap text-ink-2">{v.compose_project || "—"}</td>
                <td className="px-4 py-2 text-xs">
                  {v.used_by?.length ? <span className="text-signal">{v.used_by.join(", ")}</span> : <span className="label">nieużywany</span>}
                </td>
                <td className="readout px-4 py-2 text-xs text-ink-3">{unix(v.created_unix)}</td>
                <td className="readout max-w-[280px] truncate px-4 py-2 text-[11px] text-ink-3" title={v.mountpoint}>
                  {v.mountpoint}
                </td>
                <td className="px-4 py-2 text-right">
                  {isAdmin && (
                    <button className="btn btn-danger btn-sm" disabled={!!v.used_by?.length} title={v.used_by?.length ? "Wolumen jest używany" : ""} onClick={() => setRemove(v)}>
                      Usuń
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {creating && (
        <Modal title="Nowy wolumen" onClose={() => setCreating(false)}>
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              create.mutate();
            }}
          >
            <Field label="Nazwa">
              <input className="field readout" required autoFocus value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
            <ErrorText error={create.error} />
            <div className="flex justify-end">
              <button className="btn btn-primary" disabled={create.isPending}>
                Utwórz
              </button>
            </div>
          </form>
        </Modal>
      )}
      {remove && (
        <Confirm title="Usuń wolumen" confirmLabel="Usuń dane" danger typeToConfirm={remove.anonymous ? undefined : remove.name} pending={del.isPending} error={del.error} onClose={() => setRemove(null)} onConfirm={() => del.mutate(remove)}>
          <p>
            Dane wolumenu <span className="readout text-ink">{remove.name}</span> zostaną <strong className="text-alarm">trwale usunięte</strong>.
          </p>
        </Confirm>
      )}
      {prune && (
        <Confirm
          title={prune === "all" ? "Usuń wszystkie nieużywane wolumeny" : "Usuń anonimowe nieużywane wolumeny"}
          confirmLabel="Usuń dane"
          danger
          typeToConfirm={prune === "all" ? "usuń dane" : undefined}
          pending={pruneM.isPending}
          error={pruneM.error}
          onClose={() => setPrune(null)}
          onConfirm={() => pruneM.mutate(prune === "all")}
        >
          {prune === "all" ? (
            <p>
              Usunę <strong className="text-alarm">każdy wolumen, którego nie montuje żaden kontener</strong> — także nazwane wolumeny zatrzymanych lub usuniętych stacków
              (np. bazy danych). Tej operacji nie da się cofnąć.
            </p>
          ) : (
            <p>Usunę nieużywane wolumeny anonimowe (bez nazwy), zwykle pozostałości po usuniętych kontenerach.</p>
          )}
        </Confirm>
      )}
    </div>
  );
}

function VersionCell({ u }: { u?: ImageUpdateInfo }) {
  if (!u) return <span className="label">—</span>;
  if (u.update_available) return <UpdateBadge u={u} />;
  if (u.error) {
    return (
      <span className="label" title={u.error}>
        {/not found in registry/.test(u.error) ? "lokalny / prywatny" : "nie sprawdzono"}
      </span>
    );
  }
  return <span className="label !text-signal">aktualny</span>;
}
