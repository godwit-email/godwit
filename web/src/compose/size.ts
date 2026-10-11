/** Formats a byte count: `%d B` under 1 KB, `%d KB` rounded under 1 MB, else `%.1f MB`. */
export function byteSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1 << 20)).toFixed(1)} MB`;
}
