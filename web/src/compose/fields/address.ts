/** Adds the comma-separated addresses typed in `pending` to `list`, skipping empty ones and exact duplicates; `single` (Reply-To) keeps only the last. */
export function commit(pending: string, list: string[], single: boolean): string[] {
  let out = list;
  for (const raw of pending.split(",")) {
    const s = raw.trim();
    if (!s || out.includes(s)) continue;
    out = single ? [s] : [...out, s];
  }
  return out;
}

// RFC 5322's common forms, as Go's net/mail.ParseAddress takes them: a dot-atom addr-spec, alone or
// in angle brackets after a display name (words, or a quoted string). ponytail: no comments or
// quoted local parts; add if users meet them.
const atom = String.raw`[^\s"(),.:;<>@[\\\]]+`;
const spec = `${atom}(?:\\.${atom})*@${atom}(?:\\.${atom})*`;
const name = String.raw`(?:"(?:[^"\\]|\\.)*"|[^"(),:;<>@[\\\]]*)`;
const address = new RegExp(`^(?:${spec}|${name}\\s*<${spec}>)$`);

/** The entries that are not an address (`addr@domain` or `Name <addr@domain>`). */
export function invalid(list: string[]): string[] {
  return list.filter((s) => !address.test(s.trim()));
}
