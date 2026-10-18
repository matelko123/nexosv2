import { useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, loginWithPasskey, passkeysSupported, usersApi } from "../lib/api";
import { ErrorText, Field } from "../components/ui";

function Frame({ step, title, children }: { step: string; title: string; children: ReactNode }) {
  // Two-step flows (setup, invitation) show where the user is.
  const n = step.match(/^(?:SETUP|INVITE) \/ 0(\d)$/)?.[1];
  return (
    <div className="grid min-h-screen place-items-center px-4 py-10">
      <div className="w-full max-w-[400px]">
        <div className="rise mb-8 flex flex-col items-center text-center">
          <img src="/favicon.svg" alt="" className="size-16 rounded-[16px] shadow-[0_8px_24px_-8px_rgb(0_0_0/0.4)]" />
          <div className="mt-4 font-display text-[1.375rem] font-semibold tracking-[-0.018em]">NexOS</div>
          <div className="text-sm text-ink-3">Zarządzanie homelabem</div>
        </div>
        <div className="panel rise" style={{ animationDelay: "60ms" }}>
          <div className="space-y-5 p-6 sm:p-7">
            <div>
              {n && <div className="label mb-1">Krok {n} z 2</div>}
              <h1 className="text-[1.25rem] font-semibold tracking-[-0.017em]">{title}</h1>
            </div>
            {children}
          </div>
        </div>
        <p className="rise mt-5 text-center text-xs text-ink-3" style={{ animationDelay: "120ms" }}>
          Połączenia chronione przez mTLS, WireGuard i TOTP
        </p>
      </div>
    </div>
  );
}

const codeInput = {
  inputMode: "numeric" as const,
  autoComplete: "one-time-code",
  pattern: "[0-9]{6}",
  maxLength: 6,
  className: "field readout text-center text-2xl tracking-[0.5em]",
};

export function LoginPage() {
  const qc = useQueryClient();
  const [form, setForm] = useState({ username: "", password: "", code: "" });
  const login = useMutation({
    // Without a code, accounts with passkeys answer with a WebAuthn challenge.
    mutationFn: async (b: typeof form) => {
      const r = await api.login(b);
      return "passkey_required" in r ? loginWithPasskey(r.login_id, r.options) : r;
    },
    onSuccess: (me) => qc.setQueryData(["me"], me),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    login.mutate(form);
  };
  return (
    <Frame step="AUTH / 01" title="Logowanie">
      <form className="space-y-4" onSubmit={submit}>
        <Field label="Użytkownik">
          <input className="field" autoComplete="username" required autoFocus value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} />
        </Field>
        <Field label="Hasło">
          <input className="field" type="password" autoComplete="current-password" required value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
        </Field>
        <Field
          label="Kod z aplikacji uwierzytelniającej"
          hint={passkeysSupported() ? "Masz passkey? Zostaw puste i kliknij „Zaloguj” — przeglądarka poprosi o klucz." : undefined}
        >
          <input {...codeInput} value={form.code} onChange={(e) => setForm({ ...form, code: e.target.value.replace(/\D/g, "") })} />
        </Field>
        <ErrorText error={login.error} />
        <button className="btn btn-primary w-full" disabled={login.isPending}>
          {login.isPending ? "Weryfikacja…" : "Zaloguj"}
        </button>
      </form>
    </Frame>
  );
}

export function SetupPage() {
  const qc = useQueryClient();
  const [form, setForm] = useState({ setup_token: "", username: "admin", password: "", confirm: "" });
  const [code, setCode] = useState("");
  const begin = useMutation({ mutationFn: api.setupBegin });
  const complete = useMutation({
    mutationFn: api.setupComplete,
    onSuccess: (me) => {
      qc.setQueryData(["setup"], { needs_setup: false });
      qc.setQueryData(["me"], me);
    },
  });

  if (begin.data) {
    const s = begin.data;
    return (
      <Frame step="SETUP / 02" title="Skonfiguruj drugi składnik">
        <p className="text-sm text-ink-2">
          Zeskanuj kod w aplikacji (Aegis, 2FAS, Google Authenticator, 1Password…) i wpisz wygenerowany kod. Konto zostanie utworzone dopiero po
          potwierdzeniu.
        </p>
        <div className="flex justify-center">
          <img src={s.qr_png} alt="Kod QR TOTP" className="size-48 border-8 border-white bg-white [image-rendering:pixelated]" />
        </div>
        <Field label="Sekret (wpis ręczny)">
          <input readOnly className="field readout text-xs" value={s.totp_secret} onFocus={(e) => e.currentTarget.select()} />
        </Field>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            complete.mutate({ setup_id: s.setup_id, code });
          }}
        >
          <Field label="Kod weryfikacyjny">
            <input {...codeInput} required autoFocus value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} />
          </Field>
          <ErrorText error={complete.error} />
          <button className="btn btn-primary w-full" disabled={complete.isPending}>
            Utwórz konto administratora
          </button>
        </form>
      </Frame>
    );
  }

  const mismatch = form.confirm !== "" && form.confirm !== form.password;
  return (
    <Frame step="SETUP / 01" title="Pierwsze uruchomienie">
      <p className="text-sm text-ink-2">
        Token instalacyjny znajdziesz w logach huba: <code className="readout text-ink">docker logs nexos-hub</code> (pole{" "}
        <code className="readout">setup_token</code>).
      </p>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!mismatch) begin.mutate({ setup_token: form.setup_token, username: form.username, password: form.password });
        }}
      >
        <Field label="Token instalacyjny">
          <input className="field readout" required autoFocus value={form.setup_token} onChange={(e) => setForm({ ...form, setup_token: e.target.value })} />
        </Field>
        <Field label="Użytkownik">
          <input className="field" autoComplete="username" required value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} />
        </Field>
        <Field label="Hasło" hint="Minimum 12 znaków. Najlepiej fraza z kilku słów.">
          <input className="field" type="password" autoComplete="new-password" required minLength={12} value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
        </Field>
        <Field label="Powtórz hasło">
          <input className="field" type="password" autoComplete="new-password" required value={form.confirm} onChange={(e) => setForm({ ...form, confirm: e.target.value })} />
        </Field>
        {mismatch && <ErrorText error="Hasła nie są identyczne" />}
        <ErrorText error={begin.error} />
        <button className="btn btn-primary w-full" disabled={begin.isPending || mismatch}>
          Dalej
        </button>
      </form>
    </Frame>
  );
}

/** Accepting an invitation: the invited person sets password and TOTP. */
export function InvitePage({ token, onDone }: { token: string; onDone: () => void }) {
  const qc = useQueryClient();
  const info = useQuery({ queryKey: ["invite", token], queryFn: () => usersApi.inviteInfo(token), retry: false });
  const [form, setForm] = useState({ password: "", confirm: "" });
  const [code, setCode] = useState("");
  const begin = useMutation({ mutationFn: () => usersApi.inviteBegin(token, form.password) });
  const complete = useMutation({
    mutationFn: (setupId: string) => usersApi.inviteComplete(setupId, code),
    onSuccess: (me) => {
      qc.setQueryData(["me"], me);
      onDone();
    },
  });

  if (info.error) {
    return (
      <Frame step="INVITE" title="Zaproszenie nieważne">
        <p className="text-sm text-ink-2">Link wygasł albo został już użyty. Poproś administratora o nowe zaproszenie.</p>
      </Frame>
    );
  }
  if (!info.data) return <div className="label grid min-h-screen place-items-center">sprawdzanie zaproszenia…</div>;

  if (begin.data) {
    const s = begin.data;
    return (
      <Frame step="INVITE / 02" title="Skonfiguruj drugi składnik">
        <p className="text-sm text-ink-2">Zeskanuj kod w aplikacji uwierzytelniającej i wpisz wygenerowany kod.</p>
        <div className="flex justify-center">
          <img src={s.qr_png} alt="Kod QR TOTP" className="size-48 border-8 border-white bg-white [image-rendering:pixelated]" />
        </div>
        <Field label="Sekret (wpis ręczny)">
          <input readOnly className="field readout text-xs" value={s.totp_secret} onFocus={(e) => e.currentTarget.select()} />
        </Field>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            complete.mutate(s.setup_id);
          }}
        >
          <Field label="Kod weryfikacyjny">
            <input {...codeInput} required autoFocus value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} />
          </Field>
          <ErrorText error={complete.error} />
          <button className="btn btn-primary w-full" disabled={complete.isPending}>
            Aktywuj konto
          </button>
        </form>
      </Frame>
    );
  }

  const mismatch = form.confirm !== "" && form.confirm !== form.password;
  return (
    <Frame step="INVITE / 01" title={`Witaj, ${info.data.username}`}>
      <p className="text-sm text-ink-2">
        Zostałeś zaproszony z rolą <strong className="text-ink">{info.data.role}</strong>. Ustaw hasło, a w następnym kroku aplikację uwierzytelniającą.
      </p>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (!mismatch) begin.mutate();
        }}
      >
        <input type="text" autoComplete="username" value={info.data.username} readOnly hidden />
        <Field label="Hasło" hint="Minimum 12 znaków.">
          <input className="field" type="password" autoComplete="new-password" required minLength={12} autoFocus value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
        </Field>
        <Field label="Powtórz hasło">
          <input className="field" type="password" autoComplete="new-password" required value={form.confirm} onChange={(e) => setForm({ ...form, confirm: e.target.value })} />
        </Field>
        {mismatch && <ErrorText error="Hasła nie są identyczne" />}
        <ErrorText error={begin.error} />
        <button className="btn btn-primary w-full" disabled={begin.isPending || mismatch}>
          Dalej
        </button>
      </form>
    </Frame>
  );
}
