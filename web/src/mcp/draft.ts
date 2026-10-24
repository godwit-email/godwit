import { type Result, text } from "./bridge";
import { parse } from "./marked";

// shortcut: labels are English only, as mail headers; move to the i18n catalog when a view has prose.
const headers = [
  ["to", "To"],
  ["cc", "Cc"],
  ["bcc", "Bcc"],
  ["subject", "Subject"],
] as const;

export const el = (tag: string, cls: string, value: string) => {
  const e = document.createElement(tag);
  e.className = cls;
  e.textContent = value; // a text node only: no HTML, Markdown, images or links (ADR 0022)
  return e;
};

/** Renders show_draft's result in root; accounts is list_accounts' text, to name the account. */
export function render(root: HTMLElement, r: Result, accounts = "") {
  const shown = text(r);
  if (r.isError) {
    root.replaceChildren(el("p", "error", shown));
    return;
  }
  const { lines, fields } = parse(shown);
  const id = lines[0]?.match(/ · account (\d+)/)?.[1] ?? "";
  const from =
    accounts.match(new RegExp(`^account ${id} · (\\S+)`, "m"))?.[1] ?? (id && `account ${id}`);
  const value = (field: string) =>
    fields
      .filter((f) => f.field === field)
      .map((f) => f.value)
      .join(", ");

  const card = document.createElement("article");
  const head = document.createElement("dl");
  for (const [field, label] of [["", "From"], ...headers] as const) {
    const v = field ? value(field) : from;
    if (v) head.append(el("dt", "", label), el("dd", "", v));
  }
  card.append(head, el("div", "body", value("body")));
  for (const field of ["quote", "signature"]) {
    const v = value(field);
    if (v) card.append(el("div", field, v));
  }
  const files = value("attachment_name");
  if (files) card.append(el("p", "attachments", files));
  root.replaceChildren(card);
}
