// A godwit mcp tool result's text: our own lines, and the mail-derived fields godwit mcp wraps
// one by one in <untrusted_email id="…" field="…">…</untrusted_email> with < > & escaped
// (mcpserver/untrusted.go), so the first closing marker after an opening one is its own.

export type Field = { id: string; field: string; value: string };

const marker = /<untrusted_email id="([^"]*)" field="([^"]*)">([\s\S]*?)<\/untrusted_email>\n?/g;

const unmark = (s: string) => s.replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");

/** The text's own non-empty lines, and its fields with their values unescaped, in order. */
export function parse(text: string): { lines: string[]; fields: Field[] } {
  const fields = [...text.matchAll(marker)].map(([, id, field, value]) => ({
    id,
    field,
    value: unmark(value),
  }));
  const lines = text
    .replace(marker, "")
    .split("\n")
    .filter((l) => l !== "");
  return { lines, fields };
}
