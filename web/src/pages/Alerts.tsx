import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, observability } from "../lib/api";
import { ago, dateTime, duration } from "../lib/format";
import type { AlertChannel, AlertEvent, AlertRule, ChannelConfig, ChannelType, Me } from "../lib/types";
import { Confirm, ErrorText, Field, Modal } from "../components/ui";
import { Th } from "./HostPage";

type Tab = "active" | "history" | "rules" | "channels";

export const ruleTypeLabels: Record<string, string> = {
  host_offline: "Host offline",
  host_cpu: "CPU hosta",
  host_memory: "Pamięć hosta",
  host_disk: "Zajętość dysku",
  host_temperature: "Temperatura",
  container_unhealthy: "Problem z kontenerem",
  image_update: "Aktualizacja obrazu",
};

const usesThreshold = (t: string) => ["host_cpu", "host_memory", "host_disk", "host_temperature"].includes(t);
const usesTarget = (t: string) => ["container_unhealthy", "image_update", "host_disk"].includes(t);

/** Image updates are informational; everything else needs attention. */
export const isInfo = (e: { rule_type: string }) => e.rule_type === "image_update";

/** Live count of firing alerts, for navigation badges. */
export function useActiveAlerts() {
  return useQuery({ queryKey: ["alerts", "open"], queryFn: () => observability.alerts("open"), refetchInterval: 60_000 });
}

export function AlertsPage({ me }: { me: Me }) {
  const isAdmin = me.user.role === "admin";
  const [tab, setTab] = useState<Tab>("active");
  const active = useActiveAlerts();
  const tabs: { key: Tab; label: string; show: boolean }[] = [
    { key: "active", label: `Aktywne${active.data?.length ? ` (${active.data.length})` : ""}`, show: true },
    { key: "history", label: "Historia", show: true },
    { key: "rules", label: "Reguły", show: true },
    { key: "channels", label: "Kanały powiadomień", show: isAdmin },
  ];
  return (
    <div className="space-y-6">
      <header className="rise">
        <div className="label">02 / obserwowalność</div>
        <h1 className="font-display text-2xl font-semibold tracking-wider">Alerty</h1>
      </header>
      <nav className="flex gap-1 overflow-x-auto overflow-y-hidden border-b border-rule">
        {tabs
          .filter((t) => t.show)
          .map((t) => (
            <button
              key={t.key}
              onClick={() => setTab(t.key)}
              className={`-mb-px border-b-2 px-4 py-2 font-display text-xs font-semibold tracking-[0.14em] whitespace-nowrap uppercase transition-colors ${
                tab === t.key ? "border-amber text-ink" : "border-transparent text-ink-3 hover:text-ink"
              }`}
            >
              {t.label}
            </button>
          ))}
      </nav>
      {tab === "active" && <Events open />}
      {tab === "history" && <Events open={false} />}
      {tab === "rules" && <Rules isAdmin={isAdmin} />}
      {tab === "channels" && isAdmin && <Channels />}
    </div>
  );
}

function Events({ open }: { open: boolean }) {
  const q = useQuery({ queryKey: ["alerts", open ? "open" : "all"], queryFn: () => observability.alerts(open ? "open" : "all"), refetchInterval: 30_000 });
  if (q.error) return <ErrorText error={q.error} />;
  const list = q.data ?? [];
  // Problems first, then informational alerts.
  if (open) list.sort((a, b) => Number(isInfo(a)) - Number(isInfo(b)));
  if (q.data && list.length === 0) {
    return (
      <div className="panel rise p-10 text-center">
        <div className="mx-auto mb-3 size-3 rounded-full bg-signal lamp-on text-signal" />
        <p className="font-display tracking-wider">{open ? "Wszystko w normie" : "Brak zdarzeń"}</p>
        {open && <p className="mt-1 text-sm text-ink-3">Żadna reguła nie jest aktualnie przekroczona.</p>}
      </div>
    );
  }
  return (
    <div className="space-y-2">
      {list.map((e, i) => (
        <EventRow key={e.id} e={e} delay={i * 30} />
      ))}
    </div>
  );
}

function EventRow({ e, delay }: { e: AlertEvent; delay: number }) {
  const firing = !e.resolved_at;
  const info = isInfo(e);
  const lasted = ((e.resolved_at ? new Date(e.resolved_at).getTime() : Date.now()) - new Date(e.started_at).getTime()) / 1000;
  return (
    <div className={`panel rise flex flex-wrap items-center gap-x-6 gap-y-1 px-4 py-3 ${firing ? (info ? "border-l-2 !border-l-cyan" : "border-l-2 !border-l-alarm") : ""}`} style={{ animationDelay: `${delay}ms` }}>
      <span className={`inline-block size-2.5 shrink-0 rounded-full ${firing ? (info ? "bg-cyan" : "lamp-on bg-alarm text-alarm") : "bg-signal"}`} />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-baseline gap-x-3">
          <span className="font-medium">{e.rule_name}</span>
          <Link to="/hosts/$id" params={{ id: e.agent_id }} className="label hover:!text-amber">
            {e.host_name}
          </Link>
          {e.subject && <span className="readout text-xs text-ink-2">{e.subject}</span>}
        </div>
        <div className="mt-0.5 text-sm text-ink-2">{e.message}</div>
      </div>
      <div className="readout text-right text-xs text-ink-3">
        <div>{firing ? `od ${ago(e.started_at)}` : `${dateTime(e.started_at)}`}</div>
        <div>{firing ? "trwa" : `rozwiązany po ${duration(lasted)}`}</div>
      </div>
    </div>
  );
}

const emptyRule = (): Omit<AlertRule, "id"> & { id?: number } => ({
  name: "",
  type: "host_cpu",
  threshold: 90,
  duration_s: 300,
  target: "",
  channels: [],
  enabled: true,
});

function Rules({ isAdmin }: { isAdmin: boolean }) {
  const qc = useQueryClient();
  const rules = useQuery({ queryKey: ["alert-rules"], queryFn: observability.rules });
  const channels = useQuery({ queryKey: ["alert-channels"], queryFn: observability.channels, enabled: isAdmin });
  const hosts = useQuery({ queryKey: ["hosts"], queryFn: api.hosts });
  const [edit, setEdit] = useState<(Omit<AlertRule, "id"> & { id?: number }) | null>(null);
  const [del, setDel] = useState<AlertRule | null>(null);
  const invalidate = () => qc.invalidateQueries({ queryKey: ["alert-rules"] });
  const toggle = useMutation({ mutationFn: (r: AlertRule) => observability.saveRule({ ...r, enabled: !r.enabled }), onSuccess: invalidate });
  const remove = useMutation({ mutationFn: (id: number) => observability.deleteRule(id), onSuccess: () => (setDel(null), invalidate()) });
  const units = rules.data?.types ?? {};
  const hostName = (id?: string) => (id ? (hosts.data?.find((h) => h.id === id)?.name ?? id) : "wszystkie hosty");
  const chName = (id: number) => channels.data?.find((c) => c.id === id)?.name ?? `#${id}`;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-2xl text-sm text-ink-2">
          Reguła odpala, gdy warunek utrzymuje się przez zadany czas. Bez przypisanych kanałów alert jest widoczny tylko tutaj.
        </p>
        {isAdmin && (
          <button className="btn btn-primary" onClick={() => setEdit(emptyRule())}>
            + nowa reguła
          </button>
        )}
      </div>
      <ErrorText error={rules.error ?? toggle.error} />
      <div className="panel overflow-x-auto">
        <table className="w-full min-w-[820px] text-sm">
          <thead>
            <tr>
              <Th>reguła</Th>
              <Th>warunek</Th>
              <Th>zakres</Th>
              <Th>kanały</Th>
              <Th />
            </tr>
          </thead>
          <tbody className="divide-y divide-rule">
            {rules.data?.rules.map((r) => (
              <tr key={r.id} className={`hover:bg-panel-2 ${r.enabled ? "" : "opacity-50"}`}>
                <td className="px-4 py-2.5">
                  <div className="font-medium">{r.name}</div>
                  <div className="label">{ruleTypeLabels[r.type] ?? r.type}</div>
                </td>
                <td className="readout px-4 py-2.5 text-xs">
                  {usesThreshold(r.type) && `≥ ${r.threshold}${units[r.type]} `}
                  {r.duration_s > 0 ? `przez ${duration(r.duration_s)}` : "od razu"}
                </td>
                <td className="readout px-4 py-2.5 text-xs text-ink-2">
                  {hostName(r.agent_id)}
                  {r.target && r.target !== "*" && <div className="text-ink-3">cel: {r.target}</div>}
                </td>
                <td className="px-4 py-2.5 text-xs">
                  {r.channels.length ? r.channels.map(chName).join(", ") : <span className="label">tylko UI</span>}
                </td>
                <td className="px-4 py-2.5">
                  {isAdmin && (
                    <div className="flex justify-end gap-1.5">
                      <button className="btn !px-2 !py-0.5 !text-[10px]" onClick={() => toggle.mutate(r)}>
                        {r.enabled ? "wyłącz" : "włącz"}
                      </button>
                      <button className="btn !px-2 !py-0.5 !text-[10px]" onClick={() => setEdit(r)}>
                        edytuj
                      </button>
                      <button className="btn btn-danger !px-2 !py-0.5 !text-[10px]" onClick={() => setDel(r)}>
                        usuń
                      </button>
                    </div>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {edit && (
        <RuleEditor
          rule={edit}
          units={units}
          hosts={hosts.data?.map((h) => ({ id: h.id, name: h.name })) ?? []}
          channels={channels.data ?? []}
          onClose={() => setEdit(null)}
          onSaved={() => (setEdit(null), invalidate())}
        />
      )}
      {del && (
        <Confirm title="Usuń regułę" confirmLabel="usuń" danger pending={remove.isPending} error={remove.error} onClose={() => setDel(null)} onConfirm={() => remove.mutate(del.id)}>
          <p>
            Reguła <strong className="text-ink">{del.name}</strong> i jej historia zdarzeń zostaną usunięte.
          </p>
        </Confirm>
      )}
    </div>
  );
}

function RuleEditor({
  rule,
  units,
  hosts,
  channels,
  onClose,
  onSaved,
}: {
  rule: Omit<AlertRule, "id"> & { id?: number };
  units: Record<string, string>;
  hosts: { id: string; name: string }[];
  channels: AlertChannel[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [r, setR] = useState(rule);
  const [minutes, setMinutes] = useState(String(Math.round(rule.duration_s / 60)));
  const save = useMutation({
    mutationFn: () => observability.saveRule({ ...r, duration_s: Math.max(0, Math.round(Number(minutes) * 60) || 0) }),
    onSuccess: onSaved,
  });
  return (
    <Modal title={r.id ? "Edycja reguły" : "Nowa reguła"} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Nazwa">
            <input className="field" required maxLength={80} value={r.name} onChange={(e) => setR({ ...r, name: e.target.value })} autoFocus />
          </Field>
          <Field label="Typ">
            <select className="field" value={r.type} onChange={(e) => setR({ ...r, type: e.target.value })}>
              {Object.keys(units).map((t) => (
                <option key={t} value={t}>
                  {ruleTypeLabels[t] ?? t}
                </option>
              ))}
            </select>
          </Field>
          {usesThreshold(r.type) && (
            <Field label={`Próg (${units[r.type]})`}>
              <input className="field readout" type="number" min={0} max={1000} step="any" value={r.threshold} onChange={(e) => setR({ ...r, threshold: Number(e.target.value) })} />
            </Field>
          )}
          <Field label="Czas trwania (minuty)" hint="0 = alarm od razu">
            <input className="field readout" type="number" min={0} step="any" value={minutes} onChange={(e) => setMinutes(e.target.value)} />
          </Field>
          <Field label="Host">
            <select className="field" value={r.agent_id ?? ""} onChange={(e) => setR({ ...r, agent_id: e.target.value || undefined })}>
              <option value="">wszystkie hosty</option>
              {hosts.map((h) => (
                <option key={h.id} value={h.id}>
                  {h.name}
                </option>
              ))}
            </select>
          </Field>
          {usesTarget(r.type) && (
            <Field label={r.type === "host_disk" ? "Punkt montowania (puste = wszystkie)" : "Kontenery (wzorzec)"} hint={r.type === "host_disk" ? "np. /mnt/storage" : "np. immich-*, * = wszystkie"}>
              <input className="field readout" value={r.target} onChange={(e) => setR({ ...r, target: e.target.value })} />
            </Field>
          )}
        </div>
        <Field label="Powiadom przez">
          <div className="flex flex-wrap gap-3 pt-1 text-sm">
            {channels.length === 0 && <span className="text-xs text-ink-3">brak kanałów — dodaj je w zakładce „Kanały powiadomień”</span>}
            {channels.map((c) => (
              <label key={c.id} className="flex items-center gap-2">
                <input
                  type="checkbox"
                  className="accent-amber"
                  checked={r.channels.includes(c.id)}
                  onChange={(e) => setR({ ...r, channels: e.target.checked ? [...r.channels, c.id] : r.channels.filter((x) => x !== c.id) })}
                />
                {c.name} <span className="label">{c.type}</span>
              </label>
            ))}
          </div>
        </Field>
        <ErrorText error={save.error} />
        <div className="flex justify-end gap-2">
          <button type="button" className="btn" onClick={onClose}>
            anuluj
          </button>
          <button className="btn btn-primary" disabled={save.isPending}>
            zapisz
          </button>
        </div>
      </form>
    </Modal>
  );
}

const channelFields: Record<ChannelType, { key: keyof ChannelConfig; label: string; secret?: boolean; placeholder?: string; type?: string }[]> = {
  ntfy: [
    { key: "url", label: "Adres tematu", placeholder: "https://ntfy.sh/moj-temat" },
    { key: "token", label: "Token dostępu (opcjonalnie)", secret: true },
    { key: "priority", label: "Priorytet 1–5", type: "number" },
  ],
  discord: [{ key: "webhook_url", label: "Webhook URL", secret: true, placeholder: "https://discord.com/api/webhooks/…" }],
  telegram: [
    { key: "bot_token", label: "Token bota", secret: true },
    { key: "chat_id", label: "Chat ID" },
  ],
  webhook: [
    { key: "url", label: "URL", placeholder: "https://…" },
    { key: "secret", label: "Sekret HMAC (opcjonalnie)", secret: true },
  ],
  email: [
    { key: "host", label: "Serwer SMTP" },
    { key: "port", label: "Port", type: "number", placeholder: "587" },
    { key: "security", label: "Szyfrowanie (starttls / tls / none)", placeholder: "starttls" },
    { key: "username", label: "Użytkownik" },
    { key: "password", label: "Hasło", secret: true },
    { key: "from", label: "Od", placeholder: "nexos@example.com" },
    { key: "to", label: "Do (po przecinku)" },
  ],
};

function Channels() {
  const qc = useQueryClient();
  const channels = useQuery({ queryKey: ["alert-channels"], queryFn: observability.channels });
  const [edit, setEdit] = useState<AlertChannel | "new" | null>(null);
  const [del, setDel] = useState<AlertChannel | null>(null);
  const [tested, setTested] = useState<Record<number, string>>({});
  const invalidate = () => qc.invalidateQueries({ queryKey: ["alert-channels"] });
  const remove = useMutation({ mutationFn: (id: number) => observability.deleteChannel(id), onSuccess: () => (setDel(null), invalidate()) });
  const test = useMutation({
    mutationFn: (id: number) => observability.testChannel(id),
    onSuccess: (_d, id) => setTested((t) => ({ ...t, [id]: "wysłano ✓" })),
    onError: (e, id) => setTested((t) => ({ ...t, [id]: e instanceof Error ? e.message : "błąd" })),
  });

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-2xl text-sm text-ink-2">Tokeny i hasła są szyfrowane w bazie huba i nigdy nie wracają do przeglądarki.</p>
        <button className="btn btn-primary" onClick={() => setEdit("new")}>
          + nowy kanał
        </button>
      </div>
      <ErrorText error={channels.error} />
      <div className="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(min(100%,320px),1fr))]">
        {channels.data?.map((c) => (
          <div key={c.id} className={`panel p-4 ${c.enabled ? "" : "opacity-60"}`}>
            <div className="flex items-start justify-between gap-3">
              <div>
                <div className="font-medium">{c.name}</div>
                <div className="label">
                  {c.type}
                  {!c.enabled && " · wyłączony"}
                </div>
              </div>
              <div className="flex gap-1.5">
                <button className="btn !px-2 !py-0.5 !text-[10px]" disabled={test.isPending} onClick={() => test.mutate(c.id)}>
                  test
                </button>
                <button className="btn !px-2 !py-0.5 !text-[10px]" onClick={() => setEdit(c)}>
                  edytuj
                </button>
                <button className="btn btn-danger !px-2 !py-0.5 !text-[10px]" onClick={() => setDel(c)}>
                  usuń
                </button>
              </div>
            </div>
            <div className="readout mt-2 truncate text-xs text-ink-3">{c.config.url || c.config.chat_id || c.config.to || (c.secrets_set.length ? "skonfigurowany" : "")}</div>
            {tested[c.id] && <div className={`mt-2 text-xs ${tested[c.id].includes("✓") ? "text-signal" : "text-alarm"}`}>{tested[c.id]}</div>}
          </div>
        ))}
        {channels.data?.length === 0 && <div className="panel p-6 text-sm text-ink-3">Brak kanałów. Najprostszy start: ntfy.sh — darmowa aplikacja na telefon, wystarczy nazwa tematu.</div>}
      </div>
      {edit && <ChannelEditor channel={edit === "new" ? null : edit} onClose={() => setEdit(null)} onSaved={() => (setEdit(null), invalidate())} />}
      {del && (
        <Confirm title="Usuń kanał" confirmLabel="usuń" danger pending={remove.isPending} error={remove.error} onClose={() => setDel(null)} onConfirm={() => remove.mutate(del.id)}>
          <p>
            Kanał <strong className="text-ink">{del.name}</strong> zostanie usunięty i odpięty od reguł.
          </p>
        </Confirm>
      )}
    </div>
  );
}

function ChannelEditor({ channel, onClose, onSaved }: { channel: AlertChannel | null; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(channel?.name ?? "");
  const [type, setType] = useState<ChannelType>(channel?.type ?? "ntfy");
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [cfg, setCfg] = useState<ChannelConfig>(channel?.config ?? {});
  const save = useMutation({ mutationFn: () => observability.saveChannel({ id: channel?.id, name, type, enabled, config: cfg }), onSuccess: onSaved });
  const secretSet = (k: string) => channel?.type === type && channel.secrets_set.includes(k);
  return (
    <Modal title={channel ? "Edycja kanału" : "Nowy kanał"} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Nazwa">
            <input className="field" required value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          <Field label="Typ">
            <select className="field" value={type} onChange={(e) => setType(e.target.value as ChannelType)}>
              {(Object.keys(channelFields) as ChannelType[]).map((t) => (
                <option key={t}>{t}</option>
              ))}
            </select>
          </Field>
          {channelFields[type].map((f) => (
            <Field key={f.key} label={f.label} hint={f.secret && secretSet(f.key) ? "ustawiony — zostaw puste, aby nie zmieniać" : undefined}>
              <input
                className="field readout"
                type={f.secret ? "password" : (f.type ?? "text")}
                autoComplete="off"
                placeholder={f.secret && secretSet(f.key) ? "••••••••" : f.placeholder}
                value={(cfg[f.key] as string | number | undefined) ?? ""}
                onChange={(e) => setCfg({ ...cfg, [f.key]: f.type === "number" ? (e.target.value ? Number(e.target.value) : undefined) : e.target.value })}
              />
            </Field>
          ))}
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" className="accent-amber" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          aktywny
        </label>
        <ErrorText error={save.error} />
        <div className="flex justify-end gap-2">
          <button type="button" className="btn" onClick={onClose}>
            anuluj
          </button>
          <button className="btn btn-primary" disabled={save.isPending}>
            zapisz
          </button>
        </div>
      </form>
    </Modal>
  );
}
