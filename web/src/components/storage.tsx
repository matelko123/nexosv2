import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { observability } from "../lib/api";
import { ago, bytes } from "../lib/format";
import type { Container, DiskUsage } from "../lib/types";

/**
 * Sizes of volumes, bind mounts and writable layers on a host. The agent
 * measures them in the background, so while a pass runs we poll briefly.
 */
export function useDiskUsage(hostId: string, enabled = true) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["disk-usage", hostId],
    queryFn: () => observability.diskUsage(hostId),
    enabled,
    staleTime: 60_000,
    refetchInterval: (query) => (query.state.data?.computing ? 4_000 : 5 * 60_000),
  });
  const refresh = useMutation({
    mutationFn: () => observability.diskUsage(hostId, true),
    onSuccess: (d) => qc.setQueryData(["disk-usage", hostId], d),
  });
  return { ...q, refresh };
}

export interface StorageUser {
  container: string;
  destination?: string;
  readOnly?: boolean;
}

export interface StorageEntry {
  key: string;
  kind: "volume" | "bind";
  label: string;
  /** Full name or path, shown on hover. */
  title: string;
  /** Undefined until measured; -1 when the driver gives no size. */
  size?: number;
  partial?: boolean;
  error?: string;
  users: StorageUser[];
}

export interface Storage {
  entries: StorageEntry[];
  /** Containers' writable layers (files written outside any mount). */
  writable: number;
  volumes: number;
  binds: number;
  total: number;
}

const agentErrors: [RegExp, string][] = [
  [/system path/, "ścieżka systemowa — nie mierzę"],
  [/time budget/, "pominięto — przekroczony limit czasu pomiaru"],
  [/no such file|not exist/i, "ścieżka nie istnieje na hoście"],
];
const errorText = (e: string) => agentErrors.find(([re]) => re.test(e))?.[1] ?? "nie można zmierzyć";

/** Groups the mounts of the given containers into measured storage entries. */
export function storageOf(containers: Container[], du?: DiskUsage): Storage {
  const vols = new Map(du?.volumes?.map((v) => [v.name, v]));
  const binds = new Map(du?.binds?.map((b) => [b.source, b]));
  const layers = new Map(du?.containers?.map((c) => [c.id, c]));
  const map = new Map<string, StorageEntry>();
  let writable = 0;
  for (const c of containers) {
    writable += layers.get(c.id)?.size_rw_bytes ?? 0;
    const service = c.compose_service || c.name;
    for (const m of c.mounts ?? []) {
      let e: StorageEntry | undefined;
      if (m.type === "volume" && m.name) {
        const key = `v:${m.name}`;
        e = map.get(key);
        if (!e) {
          const v = vols.get(m.name);
          // Anonymous volumes are named by a 64-char hash; a prefix is enough to tell them apart.
          const label = /^[a-f0-9]{64}$/.test(m.name) ? `anonimowy · ${m.name.slice(0, 12)}` : m.name;
          e = { key, kind: "volume", label, title: m.name, size: v?.size_bytes ?? (du && !du.computing ? 0 : undefined), users: [] };
        }
      } else if (m.type === "bind" && m.source) {
        const key = `b:${m.source}`;
        e = map.get(key);
        if (!e) {
          const b = binds.get(m.source);
          e = { key, kind: "bind", label: m.source, title: m.source, size: b?.size_bytes ?? (b ? 0 : undefined), partial: b?.partial, error: b?.error ? errorText(b.error) : undefined, users: [] };
        }
      }
      if (!e) continue;
      e.users.push({ container: service, destination: m.destination, readOnly: m.read_only });
      map.set(e.key, e);
    }
  }
  const entries = [...map.values()].sort((a, b) => (b.size ?? 0) - (a.size ?? 0) || a.label.localeCompare(b.label));
  const sum = (kind: StorageEntry["kind"]) => entries.filter((e) => e.kind === kind && !e.error).reduce((s, e) => s + Math.max(e.size ?? 0, 0), 0);
  const volumes = sum("volume");
  const bindsTotal = sum("bind");
  return { entries, writable, volumes, binds: bindsTotal, total: volumes + bindsTotal + writable };
}

export function sizeText(e: Pick<StorageEntry, "size" | "partial">) {
  if (e.size === undefined) return "…";
  if (e.size < 0) return "brak danych";
  return `${e.partial ? "≥ " : ""}${bytes(e.size)}`;
}

/** "Measured 3 min ago · Refresh", or progress while the agent computes. */
export function StorageStatus({ du }: { du: ReturnType<typeof useDiskUsage> }) {
  const d = du.data;
  const busy = d?.computing || du.refresh.isPending;
  if (du.error && !d) {
    return <span className="text-xs text-warn">rozmiary niedostępne — agent może wymagać aktualizacji</span>;
  }
  return (
    <span className="inline-flex items-center gap-2 text-xs text-ink-3">
      {busy ? (
        <span className="inline-flex items-center gap-1.5">
          <span className="spinner" aria-hidden />
          liczenie rozmiarów…
        </span>
      ) : d?.computed_unix ? (
        <span title={new Date(d.computed_unix * 1000).toLocaleString("pl-PL")}>zmierzono {ago(new Date(d.computed_unix * 1000).toISOString())}</span>
      ) : null}
      <button className="btn btn-sm" disabled={busy} onClick={() => du.refresh.mutate()}>
        Odśwież
      </button>
    </span>
  );
}

/** Storage entries as a list with each entry's share of the largest one. */
export function StorageList({ storage, showUsers = true }: { storage: Storage; showUsers?: boolean }) {
  const max = Math.max(...storage.entries.map((e) => e.size ?? 0), storage.writable, 1);
  if (!storage.entries.length && !storage.writable) {
    return <div className="px-4 py-4 text-sm text-ink-3">Brak wolumenów i montowań — dane żyją tylko w warstwie kontenera.</div>;
  }
  return (
    <ul className="divide-y divide-rule">
      {storage.entries.map((e) => (
        <li key={e.key} className="px-4 py-2.5">
          <div className="flex items-baseline justify-between gap-3">
            <div className="flex min-w-0 items-baseline gap-2">
              <span className={`shrink-0 rounded-md px-1.5 text-[11px] font-medium ${e.kind === "volume" ? "bg-accent/12 text-accent" : "bg-violet/12 text-violet"}`}>
                {e.kind === "volume" ? "wolumen" : "katalog"}
              </span>
              <span className="readout truncate text-[13px]" title={e.title}>
                {e.label}
              </span>
            </div>
            <span className={`readout shrink-0 text-[13px] ${e.error ? "text-ink-3" : "font-medium"}`}>{e.error ? "—" : sizeText(e)}</span>
          </div>
          {!e.error && (e.size ?? 0) > 0 && (
            <div className="meter mt-1.5 !h-1">
              <span style={{ width: `${((e.size ?? 0) / max) * 100}%`, background: e.kind === "volume" ? "var(--color-accent)" : "var(--color-violet)" }} />
            </div>
          )}
          {(showUsers || e.error || e.partial) && (
            <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-ink-3">
              {showUsers &&
                e.users.map((u, i) => (
                  <span key={i} className="readout">
                    {u.container} → {u.destination}
                    {u.readOnly ? " (ro)" : ""}
                  </span>
                ))}
              {e.error && <span className="text-warn">{e.error}</span>}
              {e.partial && <span className="text-warn">pomiar przerwany po limicie czasu — rozmiar co najmniej tyle</span>}
            </div>
          )}
        </li>
      ))}
      {storage.writable > 0 && (
        <li className="px-4 py-2.5">
          <div className="flex items-baseline justify-between gap-3">
            <div className="flex min-w-0 items-baseline gap-2">
              <span className="shrink-0 rounded-md bg-ink-3/15 px-1.5 text-[11px] font-medium text-ink-2">warstwa</span>
              <span className="truncate text-[13px] text-ink-2">Pliki zapisane wewnątrz kontenerów</span>
            </div>
            <span className="readout shrink-0 text-[13px] font-medium">{bytes(storage.writable)}</span>
          </div>
          <div className="meter mt-1.5 !h-1">
            <span style={{ width: `${(storage.writable / max) * 100}%`, background: "var(--color-ink-3)" }} />
          </div>
          <div className="mt-1 text-xs text-ink-3">Znikają przy odtworzeniu kontenera — trwałe dane powinny leżeć w wolumenie.</div>
        </li>
      )}
    </ul>
  );
}
