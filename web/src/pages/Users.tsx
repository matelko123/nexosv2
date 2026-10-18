import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { usersApi } from "../lib/api";
import { ago, dateTime } from "../lib/format";
import type { Me, Role, UserSummary } from "../lib/types";
import { Confirm, CopyBox, ErrorText, Field, Modal } from "../components/ui";
import { Th } from "./HostPage";

const roleInfo: Record<Role, string> = {
  viewer: "przegląda hosty, kontenery, logi i metryki",
  operator: "uruchamia, zatrzymuje i aktualizuje kontenery oraz stacki",
  admin: "pełny dostęp: konsola, usuwanie danych, edycja stacków, użytkownicy",
};

function inviteLink(token: string) {
  return `${location.origin}/invite/${token}`;
}

export function UsersPage({ me }: { me: Me }) {
  const qc = useQueryClient();
  const users = useQuery({ queryKey: ["users"], queryFn: usersApi.list });
  const invalidate = () => qc.invalidateQueries({ queryKey: ["users"] });
  const [creating, setCreating] = useState(false);
  const [invite, setInvite] = useState<{ username: string; token: string; expires: string } | null>(null);
  const [confirm, setConfirm] = useState<{ kind: "delete" | "reset" | "sessions"; u: UserSummary } | null>(null);

  const setRole = useMutation({ mutationFn: (v: { id: number; role: Role }) => usersApi.setRole(v.id, v.role), onSuccess: invalidate });
  const act = useMutation({
    mutationFn: async (c: { kind: "delete" | "reset" | "sessions"; u: UserSummary }) => {
      if (c.kind === "delete") return usersApi.remove(c.u.id);
      if (c.kind === "sessions") return usersApi.revokeSessions(c.u.id);
      const r = await usersApi.reset(c.u.id);
      setInvite({ username: c.u.username, token: r.invite_token, expires: r.expires_at });
    },
    onSuccess: () => (setConfirm(null), invalidate()),
  });

  return (
    <div className="space-y-6">
      <header className="rise flex flex-wrap items-end justify-between gap-4">
        <div>
          <div className="label">04 / dostęp</div>
          <h1 className="font-display text-2xl font-semibold tracking-wider">Użytkownicy</h1>
        </div>
        <button className="btn btn-primary" onClick={() => setCreating(true)}>
          + zaproś użytkownika
        </button>
      </header>
      <ErrorText error={users.error ?? setRole.error} />

      <div className="panel rise overflow-x-auto">
        <table className="w-full min-w-[820px] text-sm">
          <thead>
            <tr>
              <Th>użytkownik</Th>
              <Th>rola</Th>
              <Th>ostatnie logowanie</Th>
              <Th>sesje</Th>
              <Th>passkeys</Th>
              <Th />
            </tr>
          </thead>
          <tbody className="divide-y divide-rule">
            {users.data?.map((u) => {
              const self = u.id === me.user.id;
              return (
                <tr key={u.id} className="hover:bg-panel-2">
                  <td className="px-4 py-2.5">
                    <span className="font-medium">{u.username}</span>
                    {self && <span className="label ml-2 !text-amber">ty</span>}
                    {u.pending && <span className="label ml-2 !text-warn">oczekuje na aktywację</span>}
                  </td>
                  <td className="px-4 py-2.5">
                    {self ? (
                      <span className="readout text-xs">{u.role}</span>
                    ) : (
                      <select className="field !w-32 !py-1 text-xs" value={u.role} onChange={(e) => setRole.mutate({ id: u.id, role: e.target.value as Role })}>
                        {(Object.keys(roleInfo) as Role[]).map((r) => (
                          <option key={r}>{r}</option>
                        ))}
                      </select>
                    )}
                  </td>
                  <td className="readout px-4 py-2.5 text-xs text-ink-2">{u.last_login_at ? ago(u.last_login_at) : "—"}</td>
                  <td className="readout px-4 py-2.5 text-xs">{u.sessions}</td>
                  <td className="readout px-4 py-2.5 text-xs">{u.passkeys}</td>
                  <td className="px-4 py-2.5">
                    {!self && (
                      <div className="flex justify-end gap-1.5">
                        {u.sessions > 0 && (
                          <button className="btn !px-2 !py-0.5 !text-[10px]" onClick={() => setConfirm({ kind: "sessions", u })}>
                            wyloguj
                          </button>
                        )}
                        <button className="btn !px-2 !py-0.5 !text-[10px]" onClick={() => setConfirm({ kind: "reset", u })}>
                          {u.pending ? "nowe zaproszenie" : "resetuj dostęp"}
                        </button>
                        <button className="btn btn-danger !px-2 !py-0.5 !text-[10px]" onClick={() => setConfirm({ kind: "delete", u })}>
                          usuń
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

      <div className="panel p-4 text-sm">
        <div className="label mb-2">role</div>
        {(Object.keys(roleInfo) as Role[]).map((r) => (
          <div key={r} className="flex gap-3">
            <span className="readout w-20 text-amber">{r}</span>
            <span className="text-ink-2">{roleInfo[r]}</span>
          </div>
        ))}
      </div>

      {creating && (
        <CreateUser
          onClose={() => setCreating(false)}
          onCreated={(username, token, expires) => {
            setCreating(false);
            setInvite({ username, token, expires });
            invalidate();
          }}
        />
      )}
      {invite && (
        <Modal title={`Zaproszenie · ${invite.username}`} onClose={() => setInvite(null)}>
          <div className="space-y-4">
            <p className="text-sm text-ink-2">
              Przekaż ten link osobie zapraszanej bezpiecznym kanałem. Link jest jednorazowy, ważny do{" "}
              <span className="readout text-amber">{dateTime(invite.expires)}</span>. Hasło i drugi składnik ustawi sama — nie poznasz ich.
            </p>
            <CopyBox value={inviteLink(invite.token)} rows={2} />
          </div>
        </Modal>
      )}
      {confirm && (
        <Confirm
          title={confirm.kind === "delete" ? "Usuń użytkownika" : confirm.kind === "reset" ? "Resetuj dostęp" : "Wyloguj użytkownika"}
          confirmLabel={confirm.kind === "delete" ? "usuń" : confirm.kind === "reset" ? "resetuj" : "wyloguj"}
          danger={confirm.kind !== "sessions"}
          pending={act.isPending}
          error={act.error}
          onClose={() => setConfirm(null)}
          onConfirm={() => act.mutate(confirm)}
        >
          {confirm.kind === "delete" && (
            <p>
              Konto <strong className="text-ink">{confirm.u.username}</strong> zostanie usunięte wraz z sesjami i passkeys. Wpisy w audycie pozostaną.
            </p>
          )}
          {confirm.kind === "reset" && (
            <p>
              Hasło, TOTP i passkeys użytkownika <strong className="text-ink">{confirm.u.username}</strong> zostaną skasowane, a sesje zakończone. Otrzymasz nowy
              link aktywacyjny.
            </p>
          )}
          {confirm.kind === "sessions" && <p>Wszystkie sesje użytkownika {confirm.u.username} zostaną zakończone.</p>}
        </Confirm>
      )}
    </div>
  );
}

function CreateUser({ onClose, onCreated }: { onClose: () => void; onCreated: (username: string, token: string, expires: string) => void }) {
  const [username, setUsername] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const create = useMutation({ mutationFn: () => usersApi.create(username.trim(), role), onSuccess: (r) => onCreated(username.trim(), r.invite_token, r.expires_at) });
  return (
    <Modal title="Zaproś użytkownika" onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <Field label="Nazwa użytkownika">
          <input className="field" required autoFocus pattern="[a-zA-Z0-9._\-]{3,32}" value={username} onChange={(e) => setUsername(e.target.value)} />
        </Field>
        <Field label="Rola">
          <div className="space-y-2 pt-1">
            {(Object.keys(roleInfo) as Role[]).map((r) => (
              <label key={r} className="flex items-start gap-2 text-sm">
                <input type="radio" name="role" className="mt-1 accent-amber" checked={role === r} onChange={() => setRole(r)} />
                <span>
                  <span className="readout text-amber">{r}</span> <span className="text-ink-2">— {roleInfo[r]}</span>
                </span>
              </label>
            ))}
          </div>
        </Field>
        <ErrorText error={create.error} />
        <div className="flex justify-end gap-2">
          <button type="button" className="btn" onClick={onClose}>
            anuluj
          </button>
          <button className="btn btn-primary" disabled={create.isPending}>
            utwórz zaproszenie
          </button>
        </div>
      </form>
    </Modal>
  );
}
