import type { ReactNode } from "react";
import type { Host, Me } from "../lib/types";
import { ErrorText } from "../components/ui";
import { HostHeader, useHost } from "./HostDetail";

/** Frame for host sub-pages: header with tabs, then content for online hosts. */
export function HostPage({ id, me, children }: { id: string; me: Me; children: (host: Host) => ReactNode }) {
  const host = useHost(id);
  if (host.error) return <ErrorText error={host.error} />;
  if (!host.data) return <div className="label">ładowanie…</div>;
  return (
    <div className="space-y-6">
      <HostHeader host={host.data} isAdmin={me.user.role === "admin"} />
      {host.data.status === "online" ? (
        children(host.data)
      ) : (
        <div className="panel p-6 text-sm text-ink-2">Host jest {host.data.status} — zarządzanie zasobami jest dostępne tylko dla połączonych hostów.</div>
      )}
    </div>
  );
}

export function Toolbar({ children }: { children: ReactNode }) {
  return <div className="flex flex-wrap items-center justify-between gap-3">{children}</div>;
}

export function Th({ children, className = "" }: { children?: ReactNode; className?: string }) {
  return <th className={`label px-4 py-2.5 text-left font-normal ${className}`}>{children}</th>;
}
