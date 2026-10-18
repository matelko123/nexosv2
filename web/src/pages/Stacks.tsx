import { useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, resources } from "../lib/api";
import { bytes, rate } from "../lib/format";
import { storageOf, useDiskUsage } from "../components/storage";
import { sumStats, useContainerStats } from "./StackResources";
import { useRun } from "../lib/useRun";
import type { Me, Stack, StackOperation } from "../lib/types";
import { ErrorText, Field, Modal } from "../components/ui";
import { OutputConsole } from "../components/OutputConsole";
import { HostPage, Th, Toolbar } from "./HostPage";

export const STARTER = `services:
  app:
    image: nginx:1.27-alpine
    restart: unless-stopped
    ports:
      - "8080:80"
    environment:
      TZ: \${TZ:-Europe/Warsaw}
`;

const statusStyle: Record<string, { color: string; label: string }> = {
  running: { color: "var(--color-signal)", label: "działa" },
  partial: { color: "var(--color-warn)", label: "częściowo" },
  stopped: { color: "var(--color-ink-3)", label: "zatrzymany" },
  empty: { color: "var(--color-ink-3)", label: "nie wdrożony" },
};

export function StackStatus({ s }: { s: Stack }) {
  const st = statusStyle[s.status] ?? statusStyle.empty;
  return (
    <span className="inline-flex items-center gap-2 whitespace-nowrap">
      <span className={`inline-block size-2 rounded-full ${s.status === "running" ? "" : ""}`} style={{ background: st.color, color: st.color }} />
      <span className="label" style={{ color: st.color }}>
        {st.label}
      </span>
      {s.containers_total ? (
        <span className="readout text-xs text-ink-3">
          {s.containers_running}/{s.containers_total}
        </span>
      ) : null}
    </span>
  );
}

export function StacksPage({ me }: { me: Me }) {
  const { id } = useParams({ from: "/hosts/$id/stacks" });
  return (
    <HostPage id={id} me={me}>
      {() => <StacksList hostId={id} me={me} />}
    </HostPage>
  );
}

function StacksList({ hostId, me }: { hostId: string; me: Me }) {
  const qc = useQueryClient();
  const stacks = useQuery({ queryKey: ["stacks", hostId], queryFn: () => resources.stacks(hostId), refetchInterval: 30_000 });
  const containers = useQuery({ queryKey: ["containers", hostId], queryFn: () => api.containers(hostId), refetchInterval: 30_000 });
  const stats = useContainerStats(hostId);
  const du = useDiskUsage(hostId);
  const measured = !!du.data?.computed_unix;
  const membersOf = (name: string) => (containers.data ?? []).filter((c) => c.compose_project === name);
  const [creating, setCreating] = useState(false);
  const { run, start, clear } = useRun();
  const canOperate = me.user.role !== "viewer";

  const op = async (s: Stack, operation: StackOperation) => {
    await start(`${operation} · ${s.name}`, (on) => resources.runStack(hostId, s.name, { operation }, on));
    qc.invalidateQueries({ queryKey: ["stacks", hostId] });
  };

  const managed = stacks.data?.filter((s) => s.managed) ?? [];
  const external = stacks.data?.filter((s) => !s.managed) ?? [];

  const table = (list: Stack[], title: string, hint: string) => (
    <section className="panel rise">
      <div className="flex items-baseline justify-between gap-4 border-b border-rule px-4 py-2.5">
        <h2 className="text-[1.0625rem] font-semibold tracking-[-0.012em] first-letter:uppercase">{title}</h2>
        <span className="text-xs text-ink-3">{hint}</span>
      </div>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[900px] text-sm">
          <thead>
            <tr>
              <Th>nazwa</Th>
              <Th>status</Th>
              <Th>na dysku</Th>
              <Th>sieć teraz</Th>
              <Th>katalog</Th>
              <Th />
            </tr>
          </thead>
          <tbody className="divide-y divide-rule">
            {list.map((s) => {
              const members = membersOf(s.name);
              const running = members.filter((c) => c.state === "running");
              return (
              <tr key={s.name} className="group hover:bg-panel-2">
                <td className="px-4 py-2.5">
                  <Link to="/hosts/$id/stacks/$name" params={{ id: hostId, name: s.name }} className="font-medium group-hover:text-accent">
                    {s.name}
                  </Link>
                  {s.protected && <span className="label ml-2 rounded-md border border-rule px-1.5 !text-accent-dim">nexos</span>}
                </td>
                <td className="px-4 py-2.5">
                  <StackStatus s={s} />
                </td>
                <td className="readout px-4 py-2.5 text-xs">{members.length ? (measured ? bytes(storageOf(members, du.data).total) : "…") : "—"}</td>
                <td className="readout px-4 py-2.5 text-[11px] leading-tight whitespace-nowrap">
                  {running.length ? (
                    <>
                      <div className="text-cyan">↓ {rate(sumStats(running, stats, (x) => x.network_rx_bytes_per_second))}</div>
                      <div className="text-violet">↑ {rate(sumStats(running, stats, (x) => x.network_tx_bytes_per_second))}</div>
                    </>
                  ) : (
                    "—"
                  )}
                </td>
                <td className="readout max-w-[320px] truncate px-4 py-2.5 text-xs text-ink-3" title={s.working_dir}>
                  {s.working_dir || "—"}
                </td>
                <td className="px-4 py-2.5">
                  {canOperate && !s.protected && (
                    <div className="flex justify-end gap-1.5">
                      {s.managed && s.status !== "running" && (
                        <button className="btn btn-sm" disabled={run?.running} onClick={() => op(s, "up")}>
                          Up
                        </button>
                      )}
                      {s.status !== "empty" && s.status !== "stopped" && (
                        <button className="btn btn-sm" disabled={run?.running} onClick={() => op(s, "restart")}>
                          Restart
                        </button>
                      )}
                      {!s.managed && s.status === "stopped" && (
                        <button className="btn btn-sm" disabled={run?.running} onClick={() => op(s, "start")}>
                          Start
                        </button>
                      )}
                    </div>
                  )}
                </td>
              </tr>
              );
            })}
            {list.length === 0 && (
              <tr>
                <td colSpan={6} className="px-4 py-6 text-center text-sm text-ink-3">
                  brak
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );

  return (
    <div className="space-y-5">
      <Toolbar>
        <p className="max-w-2xl text-sm text-ink-2">
          Stacki zarządzane leżą w katalogu agenta i możesz je edytować oraz wdrażać. Pozostałe projekty compose na hoście są wykrywane automatycznie i
          dostępne do podglądu, startu, zatrzymania i restartu.
        </p>
        {me.user.role === "admin" && (
          <button className="btn btn-primary" onClick={() => setCreating(true)}>
            Nowy stack
          </button>
        )}
      </Toolbar>
      <ErrorText error={stacks.error} />
      {run && <OutputConsole run={run} onClose={clear} />}
      {stacks.data && (
        <>
          {table(managed, "Zarządzane przez NexOS", "edycja · up · down · pull")}
          {table(external, "Pozostałe projekty compose", "tylko odczyt plików · start · stop · restart")}
        </>
      )}
      {creating && <NewStackModal hostId={hostId} onClose={() => setCreating(false)} />}
    </div>
  );
}

function NewStackModal({ hostId, onClose }: { hostId: string; onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: () => resources.createStack(hostId, { name, compose: STARTER, env: "" }),
    onSuccess: (s) => {
      qc.invalidateQueries({ queryKey: ["stacks", hostId] });
      navigate({ to: "/hosts/$id/stacks/$name", params: { id: hostId, name: s.name } });
    },
  });
  return (
    <Modal title="Nowy stack" onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <Field label="Nazwa projektu" hint="Małe litery, cyfry, '-' i '_'. To także nazwa katalogu i prefiks kontenerów.">
          <input className="field readout" autoFocus required pattern="[a-z0-9][a-z0-9_\-]{0,62}" value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="np. media" />
        </Field>
        <p className="text-xs text-ink-3">Stack powstanie z przykładowym compose.yaml, który od razu edytujesz. Nic nie zostanie uruchomione bez Twojej decyzji.</p>
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
