import { Link, Outlet } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useLiveConnection } from "../lib/live";
import type { Me } from "../lib/types";
import { StatusLamp } from "../components/ui";

const nav = [
  { to: "/", label: "Hosty", code: "01" },
  { to: "/audit", label: "Audyt", code: "02", admin: true },
] as const;

export function Shell({ me }: { me: Me }) {
  const qc = useQueryClient();
  const live = useLiveConnection();
  const hosts = useQuery({ queryKey: ["hosts"], queryFn: api.hosts });
  const logout = useMutation({
    mutationFn: api.logout,
    onSettled: () => qc.setQueryData(["me"], null),
  });
  const online = hosts.data?.filter((h) => h.status === "online").length ?? 0;

  return (
    <div className="mx-auto flex min-h-screen max-w-[1500px] flex-col md:flex-row">
      <aside className="flex shrink-0 flex-col border-rule md:sticky md:top-0 md:h-screen md:w-56 md:border-r">
        <div className="flex items-center justify-between px-5 py-5 md:block">
          <Link to="/" className="font-display text-xl font-semibold tracking-[0.2em]">
            NEX<span className="text-amber">OS</span>
          </Link>
          <div className="label hidden md:mt-1 md:block">homelab control</div>
          <button className="btn !px-2.5 !py-1 md:hidden" onClick={() => logout.mutate()}>
            wyloguj
          </button>
        </div>
        <nav className="flex gap-1 px-3 md:flex-col">
          {nav
            .filter((n) => !("admin" in n) || me.user.role === "admin")
            .map((n) => (
              <Link
                key={n.to}
                to={n.to}
                activeOptions={{ exact: n.to === "/" }}
                className="group flex items-center gap-3 border-l-2 border-transparent px-3 py-2 text-ink-2 transition-colors hover:text-ink data-[status=active]:border-amber data-[status=active]:bg-panel-2 data-[status=active]:text-ink"
              >
                <span className="readout text-[10px] text-ink-3 group-data-[status=active]:text-amber">{n.code}</span>
                <span className="font-display text-sm font-medium tracking-wider uppercase">{n.label}</span>
              </Link>
            ))}
        </nav>
        <div className="mt-auto hidden space-y-4 border-t border-rule px-5 py-4 md:block">
          <div className="space-y-1.5">
            <div className="flex items-center justify-between">
              <span className="label">łącze live</span>
              <StatusLamp status={live === "open" ? "online" : live === "connecting" ? "connecting" : "offline"} withLabel={false} />
            </div>
            <div className="flex items-center justify-between">
              <span className="label">hosty online</span>
              <span className="readout text-xs">
                {online}/{hosts.data?.length ?? 0}
              </span>
            </div>
          </div>
          <div className="flex items-center justify-between gap-2">
            <div className="min-w-0">
              <div className="truncate text-sm">{me.user.username}</div>
              <div className="label">{me.user.role}</div>
            </div>
            <button className="btn !px-2.5 !py-1" onClick={() => logout.mutate()}>
              wyloguj
            </button>
          </div>
        </div>
      </aside>
      <main className="min-w-0 flex-1 px-4 py-6 md:px-8 md:py-8">
        <Outlet />
      </main>
    </div>
  );
}
