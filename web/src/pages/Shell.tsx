import { Link, Outlet } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useLiveConnection } from "../lib/live";
import type { Me } from "../lib/types";
import { StatusLamp } from "../components/ui";
import { isInfo, useActiveAlerts } from "./Alerts";
import { UpdateBanner } from "./Updates";

// 20×20 outline glyphs drawn on a 1.6px stroke, in the spirit of SF Symbols.
const icons = {
  hosts: "M4 4.5h12a1 1 0 0 1 1 1v3a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-3a1 1 0 0 1 1-1Zm0 6h12a1 1 0 0 1 1 1v3a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-3a1 1 0 0 1 1-1ZM6 7h.01M6 13h.01",
  alerts: "M10 3a4.5 4.5 0 0 0-4.5 4.5c0 3.5-1.5 5-1.5 5h12s-1.5-1.5-1.5-5A4.5 4.5 0 0 0 10 3Zm-1.6 12.5a1.7 1.7 0 0 0 3.2 0",
  audit: "M6.5 3.5h7a1.5 1.5 0 0 1 1.5 1.5v11a1.5 1.5 0 0 1-1.5 1.5h-7A1.5 1.5 0 0 1 5 16V5a1.5 1.5 0 0 1 1.5-1.5ZM8 7.5h4M8 10.5h4M8 13.5h2.5",
  users: "M7.5 9.5a2.75 2.75 0 1 0 0-5.5 2.75 2.75 0 0 0 0 5.5Zm-4.5 6.5c.4-2.4 2.2-4 4.5-4s4.1 1.6 4.5 4m1-6.6a2.3 2.3 0 1 0-.6-4.5m1.6 6.9c1.6.4 2.7 1.8 3 3.7",
  updates: "M10 17a7 7 0 1 0 0-14 7 7 0 0 0 0 14Zm0-10v6m-2.5-2.5L10 13l2.5-2.5",
} as const;

const nav = [
  { to: "/", label: "Hosty", icon: icons.hosts },
  { to: "/alerts", label: "Alerty", icon: icons.alerts },
  { to: "/audit", label: "Audyt", icon: icons.audit, admin: true },
  { to: "/users", label: "Użytkownicy", icon: icons.users, admin: true },
  { to: "/updates", label: "Aktualizacje", icon: icons.updates, admin: true },
] as const;

function Glyph({ d }: { d: string }) {
  return (
    <svg viewBox="0 0 20 20" className="size-[18px] shrink-0" fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d={d} />
    </svg>
  );
}

export function Shell({ me }: { me: Me }) {
  const qc = useQueryClient();
  const live = useLiveConnection();
  const hosts = useQuery({ queryKey: ["hosts"], queryFn: api.hosts });
  const logout = useMutation({
    mutationFn: api.logout,
    onSettled: () => qc.setQueryData(["me"], null),
  });
  const online = hosts.data?.filter((h) => h.status === "online").length ?? 0;
  const alerts = useActiveAlerts();
  const problems = alerts.data?.filter((a) => !isInfo(a)).length ?? 0;

  return (
    <div className="flex min-h-screen flex-col md:flex-row">
      <aside className="material sticky top-0 z-20 flex shrink-0 flex-col border-b border-rule/60 md:h-screen md:w-60 md:border-r md:border-b-0">
        <div className="flex items-center justify-between px-4 pt-3 pb-2 md:px-5 md:pt-6 md:pb-4">
          <Link to="/" className="flex items-center gap-2.5">
            <img src="/favicon.svg" alt="" className="size-7 rounded-[7px]" />
            <span className="font-display text-[1.0625rem] font-semibold tracking-[-0.015em]">NexOS</span>
          </Link>
          <button className="btn btn-sm md:hidden" onClick={() => logout.mutate()}>
            Wyloguj
          </button>
        </div>
        <nav className="flex gap-0.5 overflow-x-auto px-2 pb-2 [scrollbar-width:none] md:flex-col md:px-3 md:pb-0">
          {nav
            .filter((n) => !("admin" in n) || me.user.role === "admin")
            .map((n) => (
              <Link
                key={n.to}
                to={n.to}
                activeOptions={{ exact: n.to === "/" }}
                className="flex items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-[0.875rem] whitespace-nowrap text-ink-2 transition-colors duration-150 hover:bg-ink-3/10 hover:text-ink active:bg-ink-3/20 data-[status=active]:bg-accent/15 data-[status=active]:font-medium data-[status=active]:text-accent"
              >
                <Glyph d={n.icon} />
                <span>{n.label}</span>
                {n.to === "/alerts" && problems > 0 && <span className="badge ml-auto">{problems}</span>}
              </Link>
            ))}
        </nav>
        <div className="mt-auto hidden space-y-4 p-5 md:block">
          <div className="space-y-2 text-[0.8125rem] text-ink-2">
            <div className="flex items-center justify-between">
              <span>Łącze live</span>
              <StatusLamp status={live === "open" ? "online" : live === "connecting" ? "connecting" : "offline"} withLabel={false} />
            </div>
            <div className="flex items-center justify-between">
              <span>Hosty online</span>
              <span className="readout text-ink">
                {online}/{hosts.data?.length ?? 0}
              </span>
            </div>
          </div>
          <div className="flex items-center justify-between gap-2 border-t border-rule pt-4">
            <Link to="/account" className="group flex min-w-0 items-center gap-2.5">
              <span className="grid size-8 shrink-0 place-items-center rounded-full bg-accent text-sm font-semibold text-on-tint uppercase">
                {me.user.username.charAt(0)}
              </span>
              <span className="min-w-0">
                <span className="block truncate text-sm font-medium group-hover:text-accent">{me.user.username}</span>
                <span className="block text-xs text-ink-3 first-letter:uppercase">{me.user.role}</span>
              </span>
            </Link>
            <button className="btn btn-sm" onClick={() => logout.mutate()}>
              Wyloguj
            </button>
          </div>
        </div>
      </aside>
      <main className="mx-auto w-full max-w-[1280px] min-w-0 flex-1 px-4 py-6 md:px-10 md:py-10">
        <UpdateBanner me={me} />
        <Outlet />
      </main>
    </div>
  );
}
