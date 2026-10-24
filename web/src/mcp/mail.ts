import { type Result, text } from "./bridge";
import { el } from "./draft";
import { type Field, parse } from "./marked";

// shortcut: labels are English only, as mail headers; move to the i18n catalog when a view has prose.
const headers = [
  ["from", "From"],
  ["to", "To"],
  ["cc", "Cc"],
  ["reply_to", "Reply-To"],
  ["subject", "Subject"],
] as const;

const value = (fields: Field[], id: string, field: string) =>
  fields
    .filter((f) => f.id === id && f.field === field)
    .map((f) => f.value)
    .join(", ");

// A message's summary line: "message 40 · thread 3 · folder 5 · <date> · …".
const summary = /^message (\d+) · thread \d+ · folder \d+ · (\S+)/;

/** A get_message result as a card: headers, plain-text body, attachments. */
function message(r: Result): HTMLElement {
  const shown = text(r);
  if (r.isError) return el("p", "error", shown);
  const { lines, fields } = parse(shown);
  const [, id = "", date = ""] = lines.map((l) => l.match(summary)).find(Boolean) ?? [];
  const key = `message-${id}`;
  const card = document.createElement("article");
  const head = document.createElement("dl");
  for (const [field, label] of [...headers, ["", "Date"]] as const) {
    const v = field ? value(fields, key, field) : date;
    if (v) head.append(el("dt", "", label), el("dd", "", v));
  }
  card.append(head, el("div", "body", value(fields, key, "body")));
  // get_message cuts the body at 64 KB, or Godwit stored only its beginning.
  if (lines.includes("(body truncated)"))
    card.append(el("p", "truncated", "Body truncated: only its beginning is shown."));
  const files = value(fields, key, "attachment_name");
  if (files) card.append(el("p", "attachments", files));
  return card;
}

/**
 * Renders show_mail's result in root. A thread lists its messages; open fetches one's body
 * (get_message through the host), which opens under it in the same card.
 */
export function render(root: HTMLElement, r: Result, open?: (id: number) => Promise<Result>) {
  const shown = text(r);
  const { lines, fields } = parse(shown);
  const thread = !r.isError && lines[0]?.match(/^thread (\d+) · /)?.[1];
  if (!thread) {
    root.replaceChildren(message(r));
    return;
  }
  const card = document.createElement("article");
  card.append(el("h1", "subject", value(fields, `thread-${thread}`, "subject")));
  for (const line of lines) {
    const [, id, date] = line.match(summary) ?? [];
    if (!id) continue;
    const key = `message-${id}`;
    const button = document.createElement("button");
    button.type = "button";
    button.disabled = !open;
    button.append(
      el("span", "from", value(fields, key, "from")),
      el("span", "date", date),
      el("span", "snippet", value(fields, key, "snippet")),
    );
    const slot = document.createElement("div");
    button.onclick = async () => {
      if (slot.firstChild || !open) return slot.replaceChildren();
      button.disabled = true;
      const got = await open(Number(id)).catch((e: Error) => ({
        content: [{ type: "text", text: e.message }],
        isError: true,
      }));
      button.disabled = false;
      slot.replaceChildren(message(got));
    };
    const item = document.createElement("section");
    item.append(button, slot);
    card.append(item);
  }
  root.replaceChildren(card);
}
