import { expect, test } from "vitest";
import { render } from "./draft";

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
