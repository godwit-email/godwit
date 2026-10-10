/** Adds the comma-separated addresses typed in `pending` to `list`, skipping empty ones and exact duplicates; `single` (Reply-To) keeps only the last. A comma inside double quotes does not separate. */
export function commit(pending: string, list: string[], single: boolean): string[] {
  let out = list;
  for (const raw of pending.match(/(?:"(?:[^"\\]|\\.)*"?|[^,"])+/g) ?? []) {
    const s = raw.trim();
    if (!s || out.includes(s)) continue;
    out = single ? [s] : [...out, s];
  }
  return out;
}

// RFC 5322's common forms, as Go's net/mail.ParseAddress takes them: an addr-spec (dot-atom or
// quoted local part), alone or in angle brackets after a display name (words, or a quoted string).
// ponytail: no comments; add if users meet them.
const atom = String.raw`[^\s"(),.:;<>@[\\\]]+`;
const quoted = String.raw`"(?:[^"\\]|\\.)*"`;
const spec = `(?:${atom}(?:\\.${atom})*|${quoted})@${atom}(?:\\.${atom})*`;
const name = String.raw`(?:"(?:[^"\\]|\\.)*"|[^"(),:;<>@[\\\]]*)`;
const address = new RegExp(`^(?:${spec}|${name}\\s*<${spec}>)$`);

/** The entries that are not an address (`addr@domain` or `Name <addr@domain>`). */
export function invalid(list: string[]): string[] {
  return list.filter((s) => !address.test(s.trim()));
}
