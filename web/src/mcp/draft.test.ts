import { expect, test } from "vitest";
import { render, withAccounts } from "./draft";

// show_draft's text, as godwit mcp writes it (mcpserver/tools.go).
const shown = `draft 7 · account 2 · reply to message 40 · revision 3 · saved 2026-10-24T09:00:00Z
<untrusted_email id="draft-7" field="to">Bob &lt;bob@example.com&gt;</untrusted_email>
<untrusted_email id="draft-7" field="cc">carol@example.com</untrusted_email>
<untrusted_email id="draft-7" field="subject">Re: &lt;b&gt;Plans&lt;/b&gt;</untrusted_email>
<untrusted_email id="draft-7" field="body">Hi Bob,
<img src=x onerror=alert(1)> ![pixel](https://example.com/p.png)
[click](https://example.com) &amp; https://example.com</untrusted_email>
<untrusted_email id="draft-7" field="quote">On Friday Bob wrote: lunch?</untrusted_email>
attachment 9 · 120 bytes
<untrusted_email id="draft-7" field="attachment_name">notes.txt</untrusted_email>
`;

const accounts = `account 1 · alice@example.com · Alice · default
account 2 · carol@example.com · Carol
`;

test("a draft renders its account, recipients, subject and body as text", () => {
  const root = document.createElement("div");
  render(root, { content: [{ type: "text", text: shown }] }, accounts);
  const text = root.textContent ?? "";
  expect(text).toContain("carol@example.com");
  expect(text).toContain("Bob <bob@example.com>");
  expect(text).toContain("Re: <b>Plans</b>");
  expect(text).toContain("<img src=x onerror=alert(1)> ![pixel](https://example.com/p.png)");
  expect(text).toContain("[click](https://example.com) & https://example.com");
  expect(text).toContain("On Friday Bob wrote: lunch?");
  expect(text).toContain("notes.txt");
  expect(root.querySelector("img, a, b, script")).toBeNull();
});

test("without the account list the account is shown by number", () => {
  const root = document.createElement("div");
  render(root, { content: [{ type: "text", text: shown }] });
  expect(root.textContent).toContain("account 2");
});

test("an error result shows its text", () => {
  const root = document.createElement("div");
  render(root, { content: [{ type: "text", text: "draft not found" }], isError: true });
  expect(root.textContent).toBe("draft not found");
});

const result = (text: string, isError = false) => ({ content: [{ type: "text", text }], isError });
const sendButton = (root: HTMLElement) => root.querySelector("button") as HTMLButtonElement;
const settle = () => new Promise((r) => setTimeout(r));

test("Send asks Godwit to send the draft at the shown revision and waits for the user", async () => {
  const root = document.createElement("div");
  const calls: [number, number][] = [];
  render(root, result(shown), accounts, {
    send: async (id, revision) => {
      calls.push([id, revision]);
      return result("waiting for the user to confirm in Godwit; it is not sent yet");
    },
    reload: async () => result(shown),
  });
  sendButton(root).click();
  await settle();
  expect(calls).toEqual([[7, 3]]);
  expect(root.textContent).toContain("Waiting for you to confirm in Godwit");
  expect(root.textContent).not.toMatch(/\bsent\b/i);
  expect(sendButton(root).disabled).toBe(true);
});

const sendWith = async (said: Result, reload = async (_id: number) => result(shown)) => {
  const root = document.createElement("div");
  render(root, result(shown), accounts, { send: async () => said, reload });
  sendButton(root).click();
  await settle();
  return root;
};
type Result = ReturnType<typeof result>;

test("with direct sending the card says the mail was sent", async () => {
  const root = await sendWith(result("sent: the draft is in Godwit's outbox"));
  expect(root.textContent).toContain("Sent");
  expect(sendButton(root).disabled).toBe(true);
});

test("with sending through MCP off the button is unavailable and points to Godwit", async () => {
  const root = await sendWith(
    result(
      "sending through MCP is off; the user can turn it on in Godwit under Settings › Local API › Send through MCP",
      true,
    ),
  );
  expect(root.textContent).toContain("Send it in Godwit");
  expect(sendButton(root).disabled).toBe(true);
});

test("a changed draft is reloaded into the card, which says so", async () => {
  const reloaded: number[] = [];
  const root = await sendWith(result("draft changed, read it again", true), async (id) => {
    reloaded.push(id);
    return result(shown.replace("revision 3", "revision 4").replace("Hi Bob,", "Hello Bob,"));
  });
  await settle();
  expect(reloaded).toEqual([7]);
  expect(root.textContent).toContain("Hello Bob,");
  expect(root.textContent).toContain("This draft changed");
  expect(sendButton(root).disabled).toBe(false);
});

test("another send error shows its text and lets the user try again", async () => {
  const root = await sendWith(result("the message has no recipients", true));
  expect(root.textContent).toContain("the message has no recipients");
  expect(sendButton(root).disabled).toBe(false);
});

test("the account list arriving after Send keeps what Send did", async () => {
  const root = document.createElement("div");
  let answer = (_: Result) => {};
  const actions = {
    send: () => new Promise<Result>((r) => (answer = r)),
    reload: async () => result(shown),
  };
  render(root, result(shown), "", actions);
  sendButton(root).click();
  render(root, result(shown), accounts, actions); // list_accounts answers while send_draft is out
  expect(root.textContent).toContain("carol@example.com");
  expect(sendButton(root).disabled).toBe(true);
  answer(result("waiting for the user to confirm in Godwit; it is not sent yet"));
  await settle();
  expect(root.textContent).toContain("Waiting for you to confirm in Godwit");
  expect(sendButton(root).disabled).toBe(true);
});

test("a changed draft reloaded after the account list still names the account", async () => {
  const root = document.createElement("div");
  const actions = {
    send: async () => result("draft changed, read it again", true),
    reload: async () => result(shown.replace("revision 3", "revision 4")),
  };
  render(root, result(shown), "", actions);
  render(root, result(shown), accounts, actions);
  sendButton(root).click();
  await settle();
  await settle();
  expect(root.textContent).toContain("This draft changed");
  expect(root.textContent).toContain("carol@example.com");
});

test("the account list arriving after a reload names the account on the reloaded draft", async () => {
  const root = document.createElement("div");
  const actions = {
    send: async () => result("draft changed, read it again", true),
    reload: async () =>
      result(shown.replace("revision 3", "revision 4").replace("Hi Bob,", "Hello Bob,")),
  };
  render(root, result(shown), "", actions);
  sendButton(root).click();
  await settle();
  await settle();
  withAccounts(root, accounts, actions); // list_accounts answers late
  expect(root.textContent).toContain("Hello Bob,");
  expect(root.textContent).toContain("This draft changed");
  expect(root.textContent).toContain("carol@example.com");
});
