import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useLiveHistory } from "../lib/live";
import { bytes, duration, pct, rate } from "../lib/format";
import type { Enrollment, Host, Me } from "../lib/types";
import { CopyBox, ErrorText, Field, Modal, SegmentMeter, Sparkline, StatusLamp } from "../components/ui";

export function HostsPage({ me }: { me: Me }) {
  const hosts = useQuery({ queryKey: ["hosts"], queryFn: api.hosts });
  const [adding, setAdding] = useState(false);
  const isAdmin = me.user.role === "admin";

  return (
    <div className="space-y-6">
      <header className="rise flex flex-wrap items-end justify-between gap-4">
        <div>
          <div className="label">01 / flota</div>
          <h1 className="font-display text-2xl font-semibold tracking-wider">Hosty</h1>
        </div>
        {isAdmin && (
          <button className="btn btn-primary" onClick={() => setAdding(true)}>
            + dodaj host
          </button>
        )}
      </header>

      <ErrorText error={hosts.error} />

      {hosts.data && hosts.data.length === 0 && (
        <div className="panel rise p-10 text-center">
          <p className="font-display text-lg tracking-wider">Brak podłączonych serwerów</p>
          <p className="mx-auto mt-2 max-w-md text-sm text-ink-2">
            Dodaj host, a następnie uruchom na nim agenta z wygenerowanym pakietem rejestracyjnym. Agent nasłuchuje tylko na adresie WireGuard.
          </p>
        </div>
      )}

      <div className="grid gap-4 [grid-template-columns:repeat(auto-fill,minmax(min(100%,340px),1fr))]">
        {hosts.data?.map((h, i) => (
          <div key={h.id} className="rise" style={{ animationDelay: `${60 + i * 50}ms` }}>
            {h.status === "pending" ? <PendingCard host={h} isAdmin={isAdmin} /> : <HostCard host={h} />}
          </div>
        ))}
      </div>

      {adding && <AddHostModal onClose={() => setAdding(false)} />}
    </div>
  );
}

function HostCard({ host }: { host: Host }) {
  const m = host.metrics;
  const hist = useLiveHistory(host.id);
  const memPct = m?.memory_total_bytes ? ((m.memory_used_bytes ?? 0) / m.memory_total_bytes) * 100 : 0;
  const rootDisk = m?.disks?.find((d) => d.mountpoint === "/") ?? m?.disks?.[0];
  const d = host.info?.docker;
  const off = host.status !== "online";

  return (
    <Link to="/hosts/$id" params={{ id: host.id }} className="panel group block transition-colors hover:border-rule-2">
      <div className="flex items-start justify-between gap-3 px-4 pt-4">
        <div className="min-w-0">
          <div className="truncate font-display text-lg font-semibold tracking-wide group-hover:text-amber">{host.name}</div>
          <div className="readout truncate text-xs text-ink-3">
            {host.info?.hostname ?? host.hostname} · {host.address}
          </div>
        </div>
        <StatusLamp status={host.status} />
      </div>

      <div className={`space-y-4 p-4 ${off ? "opacity-40" : ""}`}>
        <div>
          <div className="mb-1.5 flex items-baseline justify-between">
            <span className="label">cpu</span>
            <span className="readout text-sm">{pct(m?.cpu_percent)}</span>
          </div>
          <SegmentMeter value={m?.cpu_percent ?? 0} />
        </div>
        <div>
          <div className="mb-1.5 flex items-baseline justify-between">
            <span className="label">ram</span>
            <span className="readout text-sm">
              {bytes(m?.memory_used_bytes)} <span className="text-ink-3">/ {bytes(m?.memory_total_bytes)}</span>
            </span>
          </div>
          <SegmentMeter value={memPct} />
        </div>
        {rootDisk && (
          <div>
            <div className="mb-1.5 flex items-baseline justify-between">
              <span className="label">dysk {rootDisk.mountpoint}</span>
              <span className="readout text-sm">{pct(rootDisk.used_percent, 0)}</span>
            </div>
            <SegmentMeter value={rootDisk.used_percent ?? 0} />
          </div>
        )}

        <div className="grid grid-cols-2 gap-3 border-t border-rule pt-3">
          <div>
            <div className="label">rx ↓</div>
            <div className="readout text-sm text-cyan">{rate(m?.network_rx_bytes_per_second)}</div>
            <Sparkline values={hist.map((x) => x.network_rx_bytes_per_second ?? 0)} color="var(--color-cyan)" height={24} />
          </div>
          <div>
            <div className="label">tx ↑</div>
            <div className="readout text-sm text-amber">{rate(m?.network_tx_bytes_per_second)}</div>
            <Sparkline values={hist.map((x) => x.network_tx_bytes_per_second ?? 0)} height={24} />
          </div>
        </div>
      </div>

      <div className="flex items-center justify-between gap-2 border-t border-rule px-4 py-2.5 text-xs">
        <span className="readout text-ink-2">
          {d?.available ? (
            <>
              <span className="text-signal">{d.containers_running ?? 0}</span>/{d.containers_total ?? 0} kontenerów
            </>
          ) : (
            <span className="text-ink-3">docker niedostępny</span>
          )}
        </span>
        <span className="readout text-ink-3">{off ? (host.error ? "błąd połączenia" : "—") : `up ${duration(m?.uptime_seconds)}`}</span>
      </div>
    </Link>
  );
}

function PendingCard({ host, isAdmin }: { host: Host; isAdmin: boolean }) {
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null);
  const gen = useMutation({ mutationFn: () => api.newEnrollment(host.id), onSuccess: setEnrollment });
  const expired = !host.enrollment_expires_at || new Date(host.enrollment_expires_at) < new Date();
  return (
    <div className="panel border-dashed p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="truncate font-display text-lg font-semibold tracking-wide">{host.name}</div>
          <div className="readout truncate text-xs text-ink-3">{host.address}</div>
        </div>
        <StatusLamp status="pending" />
      </div>
      <p className="mt-4 text-sm text-ink-2">
        Czeka na rejestrację agenta.{" "}
        {expired ? <span className="text-warn">Pakiet wygasł.</span> : <span>Pakiet ważny do {new Date(host.enrollment_expires_at!).toLocaleTimeString("pl-PL")}.</span>}
      </p>
      {isAdmin && (
        <div className="mt-4 flex gap-2">
          <button className="btn" onClick={() => gen.mutate()} disabled={gen.isPending}>
            nowy pakiet
          </button>
          <Link to="/hosts/$id" params={{ id: host.id }} className="btn">
            szczegóły
          </Link>
        </div>
      )}
      <ErrorText error={gen.error} />
      {enrollment && (
        <Modal title={`Rejestracja · ${host.name}`} onClose={() => setEnrollment(null)}>
          <EnrollInstructions host={host} enrollment={enrollment} />
        </Modal>
      )}
    </div>
  );
}

function AddHostModal({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const [form, setForm] = useState({ name: "", address: "" });
  const create = useMutation({
    mutationFn: api.createHost,
    onSuccess: () => qc.invalidateQueries({ queryKey: ["hosts"] }),
  });

  return (
    <Modal title="Nowy host" onClose={onClose}>
      {create.data ? (
        <EnrollInstructions host={create.data.host} enrollment={create.data} />
      ) : (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate(form);
          }}
        >
          <Field label="Nazwa">
            <input className="field" required maxLength={64} autoFocus placeholder="np. serwer-dom" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          </Field>
          <Field label="Adres agenta" hint="Adres WireGuard i port, na którym nasłuchuje agent (NEXOS_LISTEN).">
            <input className="field readout" required placeholder="10.66.0.2:7443" value={form.address} onChange={(e) => setForm({ ...form, address: e.target.value })} />
          </Field>
          <ErrorText error={create.error} />
          <div className="flex justify-end gap-2">
            <button type="button" className="btn" onClick={onClose}>
              anuluj
            </button>
            <button className="btn btn-primary" disabled={create.isPending}>
              utwórz i wygeneruj pakiet
            </button>
          </div>
        </form>
      )}
    </Modal>
  );
}

export function EnrollInstructions({ host, enrollment }: { host: Host; enrollment: Enrollment }) {
  const listen = host.address;
  const compose = `NEXOS_LISTEN=${listen}
NEXOS_ENROLL=${enrollment.bundle}`;
  return (
    <div className="space-y-4">
      <p className="text-sm text-ink-2">
        Pakiet jest <strong className="text-ink">jednorazowy</strong> i wygasa{" "}
        <span className="readout text-amber">{new Date(enrollment.expires_at).toLocaleTimeString("pl-PL")}</span>. Zawiera odcisk CA huba, więc
        agent nie zaufa nikomu innemu. Klucz prywatny agenta powstaje lokalnie i nigdy nie opuszcza serwera.
      </p>
      <Field label="1 · Zapisz obie linie jako plik .env w katalogu z compose.yaml agenta">
        <CopyBox value={compose} rows={4} />
      </Field>
      <Field label="2 · W tym samym katalogu uruchom agenta">
        <CopyBox value="docker compose up -d && docker compose logs -f nexos-agent" rows={1} />
      </Field>
      <p className="text-xs text-ink-3">
        Oczekiwany log: <code className="readout text-signal">"msg":"enrolled"</code>, potem <code className="readout">"agent listening"</code>. Host zmieni
        status na online po kilku sekundach.
      </p>
      <p className="text-xs text-ink-3">
        Po udanej rejestracji usuń <code className="readout">NEXOS_ENROLL</code> z pliku .env — tożsamość jest już zapisana w wolumenie agenta.
      </p>
    </div>
  );
}
