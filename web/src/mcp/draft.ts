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

/** The card's tool calls through the host: send_draft and get_draft. */
export type Actions = {
  send: (id: number, revision: number) => Promise<Result>;
  reload: (id: number) => Promise<Result>;
};

const failed = (e: Error): Result => ({
  content: [{ type: "text", text: e.message }],
  isError: true,
});

/** The Send button and its outcome; send_draft's result is the only sign of the user's setting. */
function sendBar(r: Result, actions: Actions, rerender: (r: Result, note: string) => void) {
  const [, id, revision] = text(r).match(/^draft (\d+) · .* · revision (\d+) · /m) ?? [];
  const bar = document.createElement("footer");
  const button = el("button", "", "Send") as HTMLButtonElement;
  button.type = "button";
  button.disabled = !id;
  const status = el("p", "status", "");
  button.onclick = async () => {
    button.disabled = true;
    const got = await actions.send(Number(id), Number(revision)).catch(failed);
    const said = text(got);
    status.className = "status";
    if (!got.isError) {
      status.textContent = said.startsWith("sent")
        ? "Sent: the mail is in Godwit's outbox."
        : "Waiting for you to confirm in Godwit.";
    } else if (said.startsWith("sending through MCP is off")) {
      // 不允许, or confirm under --headless: the button stays off for this card.
      status.textContent =
        "Send it in Godwit: sending through MCP is off (Settings › Local API › Send through MCP).";
    } else if (said.startsWith("draft changed")) {
      const fresh = await actions.reload(Number(id)).catch(failed);
      rerender(fresh, "This draft changed; the card shows it as it is now.");
    } else {
      status.className = "status error";
      status.textContent = said;
      button.disabled = false;
    }
  };
  bar.append(button, status);
  return bar;
}

/**
 * Renders show_draft's result in root; accounts is list_accounts' text, to name the account.
 * With actions the card has a Send button; note is a line shown above it.
 */
export function render(root: HTMLElement, r: Result, accounts = "", actions?: Actions, note = "") {
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
  if (note) card.append(el("p", "status", note));
  if (actions)
    card.append(sendBar(r, actions, (fresh, line) => render(root, fresh, accounts, actions, line)));
  root.replaceChildren(card);
}
