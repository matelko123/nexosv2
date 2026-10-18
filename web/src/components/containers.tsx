import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import type { Container, ContainerAction } from "../lib/types";
import { ErrorText, Modal } from "./ui";

const stateStyle: Record<string, { color: string; label: string }> = {
  running: { color: "var(--color-signal)", label: "działa" },
  paused: { color: "var(--color-warn)", label: "wstrzymany" },
  restarting: { color: "var(--color-warn)", label: "restart" },
  created: { color: "var(--color-ink-3)", label: "utworzony" },
  removing: { color: "var(--color-ink-3)", label: "usuwanie" },
  exited: { color: "var(--color-ink-3)", label: "zatrzymany" },
  dead: { color: "var(--color-alarm)", label: "martwy" },
};

/** Exited with a non-zero code is worth flagging. */
export function exitedWithError(c: Container) {
  const m = c.status?.match(/^Exited \((\d+)\)/);
  return c.state === "exited" && !!m && m[1] !== "0";
}

export function StateBadge({ c }: { c: Container }) {
  const s = stateStyle[c.state] ?? stateStyle.created;
  const color = exitedWithError(c) || c.health === "unhealthy" ? "var(--color-alarm)" : s.color;
  return (
    <span className="inline-flex items-center gap-2 whitespace-nowrap">
      <span className={`inline-block size-2 rounded-full ${c.state === "running" ? "lamp-on" : ""}`} style={{ background: color, color }} />
      <span className="label" style={{ color }}>
        {s.label}
      </span>
      {c.health && c.health !== "none" && (
        <span className="label" style={{ color: c.health === "healthy" ? "var(--color-signal)" : c.health === "starting" ? "var(--color-warn)" : "var(--color-alarm)" }}>
          · {c.health}
        </span>
      )}
    </span>
  );
}

const actionLabels: Record<ContainerAction | "remove", string> = {
  start: "start",
  stop: "stop",
  restart: "restart",
  kill: "kill",
  pause: "pauza",
  unpause: "wznów",
  remove: "usuń",
};

const confirmText: Partial<Record<ContainerAction | "remove", string>> = {
  stop: "Kontener otrzyma SIGTERM, a po upływie limitu czasu SIGKILL.",
  restart: "Kontener zostanie zatrzymany i uruchomiony ponownie.",
  kill: "Kontener zostanie natychmiast zabity (SIGKILL), bez czasu na zamknięcie.",
};

export function availableActions(c: Container): (ContainerAction | "remove")[] {
  switch (c.state) {
    case "running":
      return ["stop", "restart", "pause", "kill"];
    case "paused":
      return ["unpause", "kill"];
    case "restarting":
      return ["stop", "kill"];
    default:
      return ["start", "remove"];
  }
}

/** Container action buttons with confirmation for disruptive operations. */
export function ContainerActions({
  hostId,
  c,
  compact = false,
  onRemoved,
}: {
  hostId: string;
  c: Container;
  compact?: boolean;
  onRemoved?: () => void;
}) {
  const qc = useQueryClient();
  const [confirm, setConfirm] = useState<ContainerAction | "remove" | null>(null);
  const [removeOpts, setRemoveOpts] = useState({ force: false, volumes: false });
  const run = useMutation({
    mutationFn: (a: ContainerAction | "remove") =>
      a === "remove" ? api.removeContainer(hostId, c.id, { ...removeOpts, name: c.name }) : api.containerAction(hostId, c.id, a, c.name),
    onSuccess: (_d, a) => {
      setConfirm(null);
      qc.invalidateQueries({ queryKey: ["containers", hostId] });
      if (a === "remove") onRemoved?.();
    },
  });

  if (c.protected) {
    return <span className="label border border-rule px-2 py-1 text-amber-dim" title="Kontener NexOS — chroniony przed zmianami z UI">nexos</span>;
  }
  const actions = compact ? availableActions(c).filter((a) => a !== "kill" && a !== "pause" && a !== "remove") : availableActions(c);
  const trigger = (a: ContainerAction | "remove") => (confirmText[a] || a === "remove" ? setConfirm(a) : run.mutate(a));

  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {actions.map((a) => (
        <button
          key={a}
          className={`btn ${compact ? "!px-2 !py-0.5 !text-[10px]" : ""} ${a === "remove" || a === "kill" ? "btn-danger" : ""}`}
          disabled={run.isPending}
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
            trigger(a);
          }}
        >
          {run.isPending && run.variables === a ? "…" : actionLabels[a]}
        </button>
      ))}
      {!confirm && run.error && <ErrorText error={run.error} />}
      {confirm && (
        <Modal title={`${actionLabels[confirm]} · ${c.name}`} onClose={() => setConfirm(null)}>
          <div className="space-y-4">
            <p className="text-sm text-ink-2">
              {confirm === "remove" ? "Kontener zostanie usunięty. Obraz i wolumeny nazwane pozostaną, chyba że zaznaczysz inaczej." : confirmText[confirm]}
            </p>
            {confirm === "remove" && (
              <div className="space-y-2 text-sm">
                <label className="flex items-center gap-2">
                  <input type="checkbox" className="accent-amber" checked={removeOpts.force} onChange={(e) => setRemoveOpts({ ...removeOpts, force: e.target.checked })} />
                  wymuś (zatrzymaj, jeśli działa)
                </label>
                <label className="flex items-center gap-2">
                  <input type="checkbox" className="accent-amber" checked={removeOpts.volumes} onChange={(e) => setRemoveOpts({ ...removeOpts, volumes: e.target.checked })} />
                  usuń też anonimowe wolumeny
                </label>
              </div>
            )}
            <ErrorText error={run.error} />
            <div className="flex justify-end gap-2">
              <button className="btn" onClick={() => setConfirm(null)}>
                anuluj
              </button>
              <button className={`btn ${confirm === "remove" || confirm === "kill" ? "btn-danger" : "btn-primary"}`} onClick={() => run.mutate(confirm)} disabled={run.isPending}>
                {run.isPending ? "wykonywanie…" : actionLabels[confirm]}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
}

export function portsText(c: Container): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const p of c.ports ?? []) {
    const s = p.public_port ? `${p.public_port}→${p.private_port}/${p.type}` : `${p.private_port}/${p.type}`;
    if (!seen.has(s)) {
      seen.add(s);
      out.push(s);
    }
  }
  return out;
}
