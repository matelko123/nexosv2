import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { passkeysSupported, registerPasskey, usersApi } from "../lib/api";
import { ago, dateTime } from "../lib/format";
import type { Me, SessionInfo } from "../lib/types";
import { Confirm, ErrorText, Field, PanelHeader } from "../components/ui";

/** Short, readable description of a browser user agent. */
export function describeAgent(ua: string): string {
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : /curl|node|Go-http/i.test(ua) ? "API" : "przeglądarka";
  const os = /Windows/.test(ua) ? "Windows" : /Android/.test(ua) ? "Android" : /iPhone|iPad/.test(ua) ? "iOS" : /Mac OS/.test(ua) ? "macOS" : /Linux/.test(ua) ? "Linux" : "";
  return os ? `${browser} · ${os}` : browser;
}

export function SessionRow({ s, onRevoke }: { s: SessionInfo; onRevoke?: () => void }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-2.5">
      <div className="min-w-0">
        <div className="text-sm">
          {describeAgent(s.user_agent)}
          {s.current && <span className="label ml-2 !text-signal">ta sesja</span>}
        </div>
        <div className="readout text-xs text-ink-3">
          {s.ip || "—"} · zalogowano {dateTime(s.created_at)} · aktywność {ago(s.last_seen_at)}
        </div>
      </div>
      {onRevoke && !s.current && (
        <button className="btn !px-2 !py-0.5 !text-[10px]" onClick={onRevoke}>
          wyloguj
        </button>
      )}
    </div>
  );
}

export function AccountPage({ me }: { me: Me }) {
  const qc = useQueryClient();
  const account = useQuery({ queryKey: ["account"], queryFn: usersApi.account });
  const invalidate = () => qc.invalidateQueries({ queryKey: ["account"] });

  const [pw, setPw] = useState({ current: "", next: "", confirm: "", code: "" });
  const changePw = useMutation({
    mutationFn: () => usersApi.changePassword(pw.current, pw.next, pw.code),
    onSuccess: () => {
      setPw({ current: "", next: "", confirm: "", code: "" });
      invalidate();
    },
  });
  const revoke = useMutation({ mutationFn: usersApi.revokeSession, onSuccess: invalidate });
  const revokeOthers = useMutation({ mutationFn: usersApi.revokeOtherSessions, onSuccess: invalidate });
  const [keyName, setKeyName] = useState("");
  const addKey = useMutation({ mutationFn: () => registerPasskey(keyName.trim()), onSuccess: () => (setKeyName(""), invalidate()) });
  const [delKey, setDelKey] = useState<{ id: string; name: string } | null>(null);
  const removeKey = useMutation({ mutationFn: (id: string) => usersApi.deletePasskey(id), onSuccess: () => (setDelKey(null), invalidate()) });

  const a = account.data;
  const mismatch = pw.confirm !== "" && pw.confirm !== pw.next;
  const others = (a?.sessions ?? []).filter((s) => !s.current).length;

  return (
    <div className="space-y-6">
      <header className="rise">
        <div className="label">konto</div>
        <h1 className="font-display text-2xl font-semibold tracking-wider">{me.user.username}</h1>
        <div className="label mt-1">rola: {me.user.role}</div>
      </header>
      <ErrorText error={account.error} />

      <div className="grid gap-5 xl:grid-cols-2">
        <section className="panel rise">
          <PanelHeader title="Zmiana hasła" />
          <form
            className="space-y-4 p-4"
            onSubmit={(e) => {
              e.preventDefault();
              if (!mismatch) changePw.mutate();
            }}
          >
            <input type="text" autoComplete="username" value={me.user.username} readOnly hidden />
            <Field label="Obecne hasło">
              <input className="field" type="password" autoComplete="current-password" required value={pw.current} onChange={(e) => setPw({ ...pw, current: e.target.value })} />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Nowe hasło" hint="Minimum 12 znaków.">
                <input className="field" type="password" autoComplete="new-password" required minLength={12} value={pw.next} onChange={(e) => setPw({ ...pw, next: e.target.value })} />
              </Field>
              <Field label="Powtórz nowe hasło">
                <input className="field" type="password" autoComplete="new-password" required value={pw.confirm} onChange={(e) => setPw({ ...pw, confirm: e.target.value })} />
              </Field>
            </div>
            <Field label="Kod z aplikacji uwierzytelniającej">
              <input className="field readout !w-40 tracking-[0.4em]" inputMode="numeric" autoComplete="one-time-code" maxLength={6} required value={pw.code} onChange={(e) => setPw({ ...pw, code: e.target.value.replace(/\D/g, "") })} />
            </Field>
            {mismatch && <ErrorText error="Hasła nie są identyczne" />}
            <ErrorText error={changePw.error} />
            {changePw.isSuccess && <p className="text-sm text-signal">Hasło zmienione. Pozostałe sesje zostały wylogowane.</p>}
            <button className="btn btn-primary" disabled={changePw.isPending || mismatch}>
              zmień hasło
            </button>
          </form>
        </section>

        <section className="panel rise self-start" style={{ animationDelay: "60ms" }}>
          <PanelHeader title="Passkeys (klucze dostępu)" />
          <div className="space-y-3 p-4">
            <p className="text-sm text-ink-2">
              Passkey (Windows Hello, Touch ID, klucz YubiKey, telefon) może zastąpić kod TOTP przy logowaniu. Jest odporny na phishing, bo przeglądarka wiąże
              go z adresem NexOS.
            </p>
            {a && !a.passkeys_available && <p className="border-l-2 border-warn bg-warn/10 px-3 py-2 text-sm text-warn">{a.passkeys_unavailable_reason}</p>}
            {a && a.passkeys_available && !passkeysSupported() && <p className="text-sm text-warn">Ta przeglądarka nie obsługuje passkeys.</p>}
            <div className="divide-y divide-rule border border-rule">
              {(a?.passkeys ?? []).map((k) => (
                <div key={k.id} className="flex items-center justify-between gap-3 px-3 py-2">
                  <div>
                    <div className="text-sm">{k.name}</div>
                    <div className="readout text-xs text-ink-3">
                      dodany {dateTime(k.created_at)} · {k.last_used_at ? `użyty ${ago(k.last_used_at)}` : "jeszcze nieużyty"}
                    </div>
                  </div>
                  <button className="btn btn-danger !px-2 !py-0.5 !text-[10px]" onClick={() => setDelKey(k)}>
                    usuń
                  </button>
                </div>
              ))}
              {a && a.passkeys.length === 0 && <div className="px-3 py-2 text-sm text-ink-3">brak passkeys</div>}
            </div>
            {a?.passkeys_available && passkeysSupported() && (
              <form
                className="flex flex-wrap gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  addKey.mutate();
                }}
              >
                <input className="field !w-56 !py-1.5" placeholder="nazwa, np. laptop" required maxLength={64} value={keyName} onChange={(e) => setKeyName(e.target.value)} />
                <button className="btn" disabled={addKey.isPending || !keyName.trim()}>
                  {addKey.isPending ? "czekam na klucz…" : "+ dodaj passkey"}
                </button>
              </form>
            )}
            <ErrorText error={addKey.error} />
          </div>
        </section>
      </div>

      <section className="panel rise" style={{ animationDelay: "120ms" }}>
        <PanelHeader title={`Aktywne sesje (${a?.sessions.length ?? 0})`}>
          {others > 0 && (
            <button className="btn !px-2 !py-0.5 !text-[10px]" onClick={() => revokeOthers.mutate()} disabled={revokeOthers.isPending}>
              wyloguj pozostałe ({others})
            </button>
          )}
        </PanelHeader>
        <div className="divide-y divide-rule">
          {(a?.sessions ?? []).map((s) => (
            <SessionRow key={s.id} s={s} onRevoke={() => revoke.mutate(s.id)} />
          ))}
        </div>
        <ErrorText error={revoke.error ?? revokeOthers.error} />
      </section>

      {delKey && (
        <Confirm title="Usuń passkey" confirmLabel="usuń" danger pending={removeKey.isPending} error={removeKey.error} onClose={() => setDelKey(null)} onConfirm={() => removeKey.mutate(delKey.id)}>
          <p>
            Passkey <strong className="text-ink">{delKey.name}</strong> przestanie działać przy logowaniu. Kod TOTP nadal będzie działał.
          </p>
        </Confirm>
      )}
    </div>
  );
}
