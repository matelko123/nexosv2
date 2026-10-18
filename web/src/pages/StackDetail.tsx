import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, resources } from "../lib/api";
import { useRun } from "../lib/useRun";
import type { Me, StackOperation } from "../lib/types";
import { Confirm, ErrorText, PanelHeader } from "../components/ui";
import { CodeEditor } from "../components/CodeEditor";
import { OutputConsole } from "../components/OutputConsole";
import { useHostUpdates } from "../components/updates";
import { HostPage } from "./HostPage";
import { StackStatus } from "./Stacks";
import { StackResources } from "./StackResources";

export function StackDetailPage({ me }: { me: Me }) {
  const { id, name } = useParams({ from: "/hosts/$id/stacks/$name" });
  return <HostPage id={id} me={me}>{() => <StackView hostId={id} name={name} me={me} />}</HostPage>;
}

function StackView({ hostId, name, me }: { hostId: string; name: string; me: Me }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const isAdmin = me.user.role === "admin";
  const canOperate = me.user.role !== "viewer";
  const detail = useQuery({ queryKey: ["stack", hostId, name], queryFn: () => resources.stack(hostId, name), enabled: canOperate });
  const list = useQuery({ queryKey: ["stacks", hostId], queryFn: () => resources.stacks(hostId) });
  const containers = useQuery({ queryKey: ["containers", hostId], queryFn: () => api.containers(hostId) });
  const stack = list.data?.find((s) => s.name === name) ?? detail.data?.stack;
  const members = (containers.data ?? []).filter((c) => c.compose_project === name);
  const upd = useHostUpdates(hostId);
  const outdated = members.filter((c) => upd.byContainer.get(c.name)?.update_available).length;

  const [compose, setCompose] = useState("");
  const [env, setEnv] = useState("");
  const [file, setFile] = useState<"compose" | "env">("compose");
  const [loaded, setLoaded] = useState(false);
  useEffect(() => {
    if (detail.data && !loaded) {
      setCompose(detail.data.compose);
      setEnv(detail.data.env);
      setLoaded(true);
    }
  }, [detail.data, loaded]);
  const dirty = !!detail.data && (compose !== detail.data.compose || (detail.data.env_visible && env !== detail.data.env));

  const { run, start, clear } = useRun();
  const [confirm, setConfirm] = useState<null | "down" | "delete">(null);
  const [opts, setOpts] = useState({ pull: false, volumes: false });

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ["stacks", hostId] });
    qc.invalidateQueries({ queryKey: ["containers", hostId] });
  };
  const save = useMutation({
    mutationFn: () => resources.updateStack(hostId, name, { compose, env: detail.data?.env_visible ? env : (detail.data?.env ?? "") }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["stack", hostId, name] });
      setLoaded(false);
      refresh();
    },
  });

  const op = async (operation: StackOperation, extra: { pull?: boolean; remove_volumes?: boolean } = {}) => {
    setConfirm(null);
    await start(`${operation} · ${name}`, (on) => resources.runStack(hostId, name, { operation, ...extra }, on));
    refresh();
  };
  const saveAndDeploy = async () => {
    try {
      await save.mutateAsync();
    } catch {
      return; // validation error is shown under the editor
    }
    await op("up", { pull: opts.pull });
  };
  const remove = async () => {
    setConfirm(null);
    const res = await start(`usuwanie · ${name}`, (on) => resources.deleteStack(hostId, name, true, opts.volumes, on));
    if (!res.error && res.exitCode === 0) {
      refresh();
      navigate({ to: "/hosts/$id/stacks", params: { id: hostId } });
    }
  };

  if (list.isPending) return <div className="label">ładowanie…</div>;
  if (!stack) return <ErrorText error={detail.error ?? "Stack nie istnieje."} />;
  const managed = !!stack.managed;
  const busy = run?.running ?? false;
  const locked = stack.protected;

  return (
    <div className="space-y-5">
      <div className="rise flex flex-wrap items-end justify-between gap-4">
        <div className="min-w-0">
          <Link to="/hosts/$id/stacks" params={{ id: hostId }} className="back-link">
            ‹ Stacki
          </Link>
          <div className="mt-1 flex flex-wrap items-center gap-4">
            <h2 className="text-[1.375rem] font-bold tracking-[-0.018em]">{stack.name}</h2>
            <StackStatus s={stack} />
            <span className={`label border px-1.5 ${managed ? "border-accent-dim !text-accent" : "border-rule"}`}>{managed ? "zarządzany" : "zewnętrzny"}</span>
            {locked && <span className="label rounded-md border border-rule px-1.5 !text-accent-dim">nexos · chroniony</span>}
          </div>
          <div className="readout mt-1 truncate text-xs text-ink-3">{stack.working_dir}</div>
        </div>
        {canOperate && !locked && (
          <div className="flex flex-wrap items-center gap-1.5">
            {managed && (
              <>
                <label className="label mr-1 flex cursor-pointer items-center gap-1.5">
                  <input type="checkbox" className="accent-accent" checked={opts.pull} onChange={(e) => setOpts({ ...opts, pull: e.target.checked })} />
                  pobierz obrazy
                </label>
                <button className="btn btn-primary" disabled={busy} onClick={() => op("up", { pull: opts.pull })}>
                  Up
                </button>
                <button className="btn" disabled={busy} onClick={() => op("pull")}>
                  Pull
                </button>
              </>
            )}
            {!managed && stack.status === "stopped" && (
              <button className="btn" disabled={busy} onClick={() => op("start")}>
                Start
              </button>
            )}
            <button className="btn" disabled={busy || stack.status === "empty"} onClick={() => op("restart")}>
              Restart
            </button>
            <button className="btn" disabled={busy || stack.status === "empty" || stack.status === "stopped"} onClick={() => op("stop")}>
              Stop
            </button>
            <button className="btn btn-danger" disabled={busy || stack.status === "empty"} onClick={() => setConfirm("down")}>
              Down
            </button>
            {managed && isAdmin && (
              <button className="btn btn-danger" disabled={busy} onClick={() => setConfirm("delete")}>
                Usuń
              </button>
            )}
          </div>
        )}
      </div>

      {run && <OutputConsole run={run} onClose={clear} />}

      {outdated > 0 && canOperate && !locked && managed && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-[var(--radius-control)] bg-cyan/5 px-4 py-2.5 text-sm">
          <span>
            <span className="text-cyan">{outdated}</span> {outdated === 1 ? "kontener ma" : "kontenery mają"} nowszy obraz w rejestrze.
          </span>
          <button className="btn btn-sm" disabled={busy} onClick={() => op("up", { pull: true })}>
            Pobierz i wdróż
          </button>
        </div>
      )}

      {members.length > 0 ? (
        <StackResources hostId={hostId} members={members} updates={upd.byContainer} />
      ) : (
        <div className="panel px-4 py-4 text-sm text-ink-3">Stack nie ma kontenerów — uruchom „up”, aby zobaczyć zużycie zasobów.</div>
      )}

      <div>
        <section className="panel flex min-h-[520px] flex-col">
          {managed ? (
            <>
              <div className="flex items-center justify-between gap-3 border-b border-rule px-3 py-2">
                <div className="tabs" role="tablist">
                  {(["compose", "env"] as const).map((f) => (
                    <button
                      key={f}
                      onClick={() => setFile(f)}
                      role="tab"
                      aria-selected={file === f}
                      className="tab readout"
                    >
                      {f === "compose" ? "compose.yaml" : ".env"}
                    </button>
                  ))}
                </div>
                {isAdmin ? (
                  <div className="flex items-center gap-2">
                    {dirty && <span className="label !text-warn">niezapisane zmiany</span>}
                    <button className="btn btn-sm" disabled={!dirty || save.isPending} onClick={() => save.mutate()} title="Ctrl+S">
                      Zapisz
                    </button>
                    <button className="btn btn-primary btn-sm" disabled={(!dirty && stack.status === "running") || save.isPending || busy} onClick={saveAndDeploy}>
                      Zapisz i wdróż
                    </button>
                  </div>
                ) : (
                  <span className="label">tylko odczyt</span>
                )}
              </div>
              <div className="flex-1 p-2">
                {!detail.data ? (
                  <div className="label p-3">{detail.isPending ? "ładowanie…" : "brak dostępu do plików"}</div>
                ) : file === "compose" ? (
                  <CodeEditor value={compose} onChange={setCompose} readOnly={!isAdmin} onSave={() => dirty && save.mutate()} />
                ) : detail.data.env_visible ? (
                  <CodeEditor value={env} onChange={setEnv} language="env" readOnly={!isAdmin} onSave={() => dirty && save.mutate()} />
                ) : (
                  <div className="p-4 text-sm text-ink-3">Plik .env może zawierać sekrety — widoczny tylko dla administratorów.</div>
                )}
              </div>
              {save.error && (
                <div className="border-t border-rule p-2">
                  <pre className="readout max-h-40 overflow-auto rounded-[var(--radius-control)] bg-alarm/10 px-3 py-2 text-xs whitespace-pre-wrap text-alarm">
                    {save.error instanceof Error ? save.error.message : String(save.error)}
                  </pre>
                </div>
              )}
            </>
          ) : (
            <>
              <PanelHeader title="Pliki projektu (tylko odczyt)" />
              <div className="flex-1 space-y-3 overflow-auto p-3">
                {!detail.data && <div className="label">{detail.isPending ? "ładowanie…" : "brak dostępu"}</div>}
                {detail.data?.files.map((f) => (
                  <div key={f.path} className="space-y-1">
                    <div className="readout text-xs text-ink-2">{f.path}</div>
                    {f.error ? (
                      <div className="text-xs text-warn">nie można odczytać: {f.error}</div>
                    ) : (
                      <div className="h-[360px]">
                        <CodeEditor value={f.content ?? ""} readOnly language={f.path?.endsWith(".env") ? "env" : "yaml"} />
                      </div>
                    )}
                  </div>
                ))}
                <p className="text-xs text-ink-3">
                  Ten projekt jest uruchamiany spoza NexOS. Aby edytować go tutaj, przenieś jego pliki do katalogu stacków agenta.
                </p>
              </div>
            </>
          )}
        </section>

      </div>

      {confirm === "down" && (
        <Confirm title={`down · ${name}`} confirmLabel="Down" danger onClose={() => setConfirm(null)} onConfirm={() => op("down", { remove_volumes: opts.volumes })}>
          <p>Kontenery i sieci stacka zostaną usunięte. Dane w wolumenach nazwanych pozostaną, chyba że zaznaczysz inaczej.</p>
          {isAdmin && (
            <label className="flex items-center gap-2">
              <input type="checkbox" className="accent-accent" checked={opts.volumes} onChange={(e) => setOpts({ ...opts, volumes: e.target.checked })} />
              <span className={opts.volumes ? "text-alarm" : ""}>usuń też wolumeny (nieodwracalne)</span>
            </label>
          )}
        </Confirm>
      )}
      {confirm === "delete" && (
        <Confirm title={`usuń stack · ${name}`} confirmLabel="Usuń stack" danger typeToConfirm={name} onClose={() => setConfirm(null)} onConfirm={remove}>
          <p>Wykonam „down” i usunę katalog stacka razem z compose.yaml i .env.</p>
          <label className="flex items-center gap-2">
            <input type="checkbox" className="accent-accent" checked={opts.volumes} onChange={(e) => setOpts({ ...opts, volumes: e.target.checked })} />
            <span className={opts.volumes ? "text-alarm" : ""}>usuń też wolumeny (nieodwracalne)</span>
          </label>
        </Confirm>
      )}
    </div>
  );
}
