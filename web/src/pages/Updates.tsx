import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { systemApi } from "../lib/api";
import { useRun } from "../lib/useRun";
import { ago, dateTime } from "../lib/format";
import type { Me } from "../lib/types";
import { Confirm, ErrorText, PanelHeader } from "../components/ui";
import { OutputConsole } from "../components/OutputConsole";
import { Th } from "./HostPage";

/** NexOS release status; admins only (the endpoint is readable by all). */
export function useNexosVersion(enabled = true) {
  return useQuery({ queryKey: ["nexos-version"], queryFn: systemApi.version, refetchInterval: 10 * 60_000, enabled });
}

/** Banner for admins when the hub or any agent can be updated. */
export function UpdateBanner({ me }: { me: Me }) {
  const v = useNexosVersion(me.user.role === "admin");
  if (me.user.role !== "admin" || !v.data?.hub.latest) return null;
  const outdated = v.data.hosts.filter((h) => h.update_available).length;
  if (!v.data.hub.update_available && outdated === 0) return null;
  return (
    <Link to="/updates" className="mb-5 flex flex-wrap items-center justify-between gap-3 border-l-2 border-cyan bg-cyan/5 px-4 py-2.5 text-sm hover:bg-cyan/10">
      <span>
        Dostępna nowa wersja NexOS <span className="readout text-cyan">{v.data.hub.latest.version}</span>
        {v.data.hub.update_available ? ` (hub: ${v.data.hub.hub_version})` : ""}
        {outdated > 0 ? ` · ${outdated} ${outdated === 1 ? "agent do aktualizacji" : "agentów do aktualizacji"}` : ""}
      </span>
      <span className="label !text-cyan">szczegóły →</span>
    </Link>
  );
}

function waitForHub(target: string, onProgress: (s: string) => void) {
  // The hub restarts during its update: poll until it reports the new
  // version, then reload so the browser loads the new UI as well.
  const started = Date.now();
  const tick = async () => {
    try {
      const res = await fetch("/api/system/version", { credentials: "same-origin" });
      if (res.ok) {
        const v = (await res.json()) as { hub: { hub_version: string } };
        if (v.hub.hub_version === target) {
          onProgress("hub działa w nowej wersji — przeładowanie…");
          setTimeout(() => location.reload(), 1000);
          return;
        }
      }
    } catch {
      // hub restarting
    }
    if (Date.now() - started > 5 * 60_000) {
      onProgress("hub nie odpowiedział w nowej wersji w ciągu 5 minut — sprawdź logi kontenera");
      return;
    }
    setTimeout(tick, 3000);
  };
  setTimeout(tick, 5000);
}

export function UpdatesPage({ me }: { me: Me }) {
  const qc = useQueryClient();
  const v = useNexosVersion();
  const check = useMutation({ mutationFn: systemApi.check, onSuccess: (d) => qc.setQueryData(["nexos-version"], d) });
  const { run, start, clear } = useRun();
  const [confirm, setConfirm] = useState<null | { kind: "hub" } | { kind: "agent"; id: string; name: string }>(null);
  const [waiting, setWaiting] = useState<string | null>(null);
  const isAdmin = me.user.role === "admin";
  const d = v.data;
  const latest = d?.hub.latest;

  const doUpdate = async () => {
    if (!confirm || !latest) return;
    const c = confirm;
    setConfirm(null);
    if (c.kind === "hub") {
      const res = await start(`aktualizacja huba → ${latest.version}`, (on) => systemApi.updateHub(on));
      if (!res.error) {
        setWaiting("hub restartuje się…");
        waitForHub(latest.version, setWaiting);
      }
    } else {
      await start(`aktualizacja agenta ${c.name} → ${latest.version}`, (on) => systemApi.updateAgent(c.id, on));
      // The agent reconnects within seconds; refresh versions afterwards.
      setTimeout(() => qc.invalidateQueries({ queryKey: ["nexos-version"] }), 15_000);
    }
  };

  return (
    <div className="space-y-6">
      <header className="rise flex flex-wrap items-end justify-between gap-4">
        <div>
          <div className="label">05 / system</div>
          <h1 className="font-display text-2xl font-semibold tracking-wider">Aktualizacje NexOS</h1>
        </div>
        {isAdmin && (
          <button className="btn" disabled={check.isPending} onClick={() => check.mutate()}>
            {check.isPending ? "sprawdzanie…" : "sprawdź teraz"}
          </button>
        )}
      </header>
      <ErrorText error={v.error ?? check.error} />
      {d?.hub.error && <ErrorText error={`Ostatnie sprawdzenie: ${d.hub.error}`} />}
      {waiting && <div className="border-l-2 border-warn bg-warn/10 px-4 py-2.5 text-sm text-warn">{waiting}</div>}
      {run && <OutputConsole run={run} onClose={run.running ? undefined : clear} />}

      {d && (
        <div className="grid gap-5 xl:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
          <section className="panel rise self-start">
            <PanelHeader title="Wersje" />
            <div className="space-y-4 p-4">
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <div className="label">zainstalowany hub</div>
                  <div className="readout mt-1 text-xl">{d.hub.hub_version}</div>
                </div>
                <div>
                  <div className="label">najnowsze wydanie</div>
                  <div className={`readout mt-1 text-xl ${d.hub.update_available ? "text-cyan" : "text-signal"}`}>{latest?.version ?? "—"}</div>
                </div>
              </div>
              <div className="readout text-xs text-ink-3">
                źródło: github.com/{d.hub.repo} · sprawdzono {d.hub.checked_at ? ago(d.hub.checked_at) : "jeszcze nie"}
              </div>
              {isAdmin && latest && d.hub.update_available && (
                <button className="btn btn-primary w-full" disabled={run?.running || !!waiting || !d.hub_host_id} onClick={() => setConfirm({ kind: "hub" })}>
                  aktualizuj hub do {latest.version}
                </button>
              )}
              {isAdmin && d.hub.update_available && !d.hub_host_id && (
                <p className="text-xs text-warn">
                  Żaden podłączony agent nie widzi kontenera huba (etykieta nexos.role=hub), więc hub trzeba zaktualizować ręcznie: docker compose pull && docker
                  compose up -d.
                </p>
              )}
            </div>
            <table className="w-full border-t border-rule text-sm">
              <thead>
                <tr>
                  <Th>agent</Th>
                  <Th>wersja</Th>
                  <Th />
                </tr>
              </thead>
              <tbody className="divide-y divide-rule">
                {d.hosts.map((h) => (
                  <tr key={h.id}>
                    <td className="px-4 py-2">
                      {h.name}
                      {h.runs_hub && <span className="label ml-2 !text-amber">hub</span>}
                      {h.status !== "online" && <span className="label ml-2">{h.status}</span>}
                    </td>
                    <td className={`readout px-4 py-2 text-xs ${h.update_available ? "text-cyan" : ""}`}>{h.agent_version ?? "—"}</td>
                    <td className="px-4 py-2 text-right">
                      {isAdmin && h.update_available && (
                        <button
                          className="btn !px-2 !py-0.5 !text-[10px]"
                          disabled={run?.running || h.status !== "online"}
                          onClick={() => setConfirm({ kind: "agent", id: h.id, name: h.name })}
                        >
                          aktualizuj
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>

          <section className="panel rise" style={{ animationDelay: "60ms" }}>
            <PanelHeader title={latest ? `Co nowego w ${latest.name || latest.version}` : "Informacje o wydaniu"}>
              {latest?.url && (
                <a href={latest.url} target="_blank" rel="noreferrer noopener" className="label hover:!text-amber">
                  github ↗
                </a>
              )}
            </PanelHeader>
            <div className="p-4">
              {latest ? (
                <>
                  <div className="readout mb-3 text-xs text-ink-3">opublikowano {latest.published_at ? dateTime(latest.published_at) : "—"}</div>
                  <div className="max-h-[480px] overflow-auto text-sm leading-relaxed whitespace-pre-wrap text-ink-2">{latest.notes || "brak opisu"}</div>
                </>
              ) : (
                <p className="text-sm text-ink-3">Brak informacji o wydaniach. Kliknij „sprawdź teraz”.</p>
              )}
            </div>
          </section>
        </div>
      )}

      <div className="panel p-4 text-xs text-ink-3">
        Aktualizacja pobiera obraz z oficjalnego repozytorium i odtwarza kontener przez docker compose na hoście (kontener pomocniczy agenta). Dane w wolumenach
        zostają. Nic nie aktualizuje się samo — każdą aktualizację zatwierdza administrator.
      </div>

      {confirm && latest && (
        <Confirm
          title={confirm.kind === "hub" ? "Aktualizacja huba" : `Aktualizacja agenta · ${confirm.name}`}
          confirmLabel={`aktualizuj do ${latest.version}`}
          onClose={() => setConfirm(null)}
          onConfirm={doUpdate}
        >
          {confirm.kind === "hub" ? (
            <p>Hub zostanie zrestartowany w nowej wersji. Interfejs będzie niedostępny przez kilkanaście sekund, potem strona przeładuje się sama.</p>
          ) : (
            <p>Agent zostanie zrestartowany w nowej wersji. Zarządzanie tym hostem będzie niedostępne przez kilka sekund; kontenery na hoście działają dalej.</p>
          )}
        </Confirm>
      )}
    </div>
  );
}
