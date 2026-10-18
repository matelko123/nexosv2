const UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];

export function bytes(n: number | undefined, digits = 1): string {
  if (!n || n < 1) return "0 B";
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), UNITS.length - 1);
  const v = n / 1024 ** i;
  return `${v.toFixed(i === 0 || v >= 100 ? 0 : digits)} ${UNITS[i]}`;
}

export const rate = (n: number | undefined) => `${bytes(n)}/s`;

export const pct = (n: number | undefined, digits = 1) => `${(n ?? 0).toFixed(digits)}%`;

export function duration(seconds: number | undefined): string {
  let s = Math.floor(seconds ?? 0);
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

export function ago(iso: string | undefined): string {
  if (!iso) return "—";
  const diff = (Date.now() - new Date(iso).getTime()) / 1000;
  if (diff < 0) return "za " + duration(-diff);
  if (diff < 60) return "przed chwilą";
  return duration(diff) + " temu";
}

export function dateTime(v: string | number | undefined): string {
  if (v === undefined) return "—";
  return new Date(v).toLocaleString("pl-PL", { dateStyle: "short", timeStyle: "medium" });
}
