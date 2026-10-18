import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { observability } from "../lib/api";
import { ago } from "../lib/format";
import type { ImageUpdateInfo } from "../lib/types";
import { ErrorText } from "./ui";

/** Stored image update results of a host, indexed for quick lookups. */
export function useHostUpdates(hostId: string) {
  const q = useQuery({ queryKey: ["updates", hostId], queryFn: () => observability.updates(hostId), refetchInterval: 5 * 60_000 });
  return useMemo(() => {
    const byContainer = new Map<string, ImageUpdateInfo>();
    const byImage = new Map<string, ImageUpdateInfo>();
    for (const u of q.data ?? []) {
      if (u.local_image_id) byImage.set(u.local_image_id, u);
      for (const c of u.containers) byContainer.set(c, u);
    }
    const available = (q.data ?? []).filter((u) => u.update_available);
    const checkedAt = (q.data ?? []).reduce<string | undefined>((m, u) => (!m || u.checked_at > m ? u.checked_at : m), undefined);
    return { query: q, byContainer, byImage, available, checkedAt };
  }, [q]);
}

export function UpdateBadge({ u }: { u?: ImageUpdateInfo }) {
  if (!u?.update_available) return null;
  return (
    <span className="label border border-cyan/40 px-1.5 !text-cyan" title={`Nowa wersja ${u.display} w rejestrze`}>
      ↑ nowa wersja
    </span>
  );
}

/** "Check now" button with last-check time. */
export function CheckUpdatesButton({ hostId, checkedAt, canOperate }: { hostId: string; checkedAt?: string; canOperate: boolean }) {
  const qc = useQueryClient();
  const check = useMutation({
    mutationFn: () => observability.checkUpdates(hostId),
    onSuccess: (data) => qc.setQueryData(["updates", hostId], data),
  });
  return (
    <span className="inline-flex flex-wrap items-center gap-3">
      <span className="readout text-xs text-ink-3">{checkedAt ? `aktualizacje sprawdzone ${ago(checkedAt)}` : "aktualizacje nie były sprawdzane"}</span>
      {canOperate && (
        <button className="btn !px-2.5 !py-1 !text-[10px]" disabled={check.isPending} onClick={() => check.mutate()}>
          {check.isPending ? "sprawdzanie…" : "sprawdź teraz"}
        </button>
      )}
      <ErrorText error={check.error} />
    </span>
  );
}
