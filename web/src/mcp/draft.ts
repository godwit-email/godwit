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
  bar.dataset.draft = `${id}:${revision}`;
  const button = el("button", "", "Send") as HTMLButtonElement;
  button.type = "button";
  button.disabled = !id;
  const status = el("p", "status", "");
  button.onclick = async () => {
    button.disabled = true;
    const got = await actions.send(Number(id), Number(revision)).catch(failed);
    const said = text(got); // the prefixes are godwit mcp's stable texts (textSent…, mcpserver.go)
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
  drawn.set(root, { r, note, accounts });
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
  if (actions) {
    // A reload redraws only while root still shows this bar, not a newer result drawn meanwhile.
    const bar: HTMLElement = sendBar(r, actions, (fresh, line) => {
      if (root.contains(bar)) render(root, fresh, drawn.get(root)?.accounts, actions, line);
    });
    // The same draft drawn again (the account list arriving) keeps its bar: a send may be out.
    const old = root.querySelector<HTMLElement>(":scope > article > footer");
    card.append(old && old.dataset.draft === bar.dataset.draft ? old : bar);
  }
  root.replaceChildren(card);
}

/** What each root was last drawn with, for a bar kept across draws and a late account list. */
const drawn = new WeakMap<HTMLElement, { r: Result; note: string; accounts: string }>();

/** Draws root's last result and note again, naming the account from accounts. */
export function withAccounts(root: HTMLElement, accounts: string, actions?: Actions) {
  const last = drawn.get(root);
  if (last) render(root, last.r, accounts, actions, last.note);
}
