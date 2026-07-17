/** Human file size. Comics land in the tens of MB, so one decimal is plenty. */
export function formatBytes(bytes: number): string {
  if (bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / Math.pow(1024, i);
  return `${i === 0 ? value : value.toFixed(1)} ${units[i]}`;
}

/** Unix seconds to a short local date. */
export function formatDate(seconds: number): string {
  return new Date(seconds * 1000).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}

/** Unix seconds as "3 days ago". Falls back to a date once it stops being useful. */
export function formatRelative(seconds: number): string {
  const diff = Date.now() / 1000 - seconds;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)} min ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} hr ago`;
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)} days ago`;
  return formatDate(seconds);
}

/** "Batman #12" from a comic's series and number, falling back to the title. */
export function comicLabel(comic: { title: string; series?: string; number?: string }): string {
  if (!comic.series) return comic.title;
  return comic.number ? `${comic.series} #${comic.number}` : comic.series;
}
