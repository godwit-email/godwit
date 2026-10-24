import { expect, test } from "vitest";
import type { Result } from "./bridge";
import { render } from "./mail";

// show_mail's text for a message and for its thread, as godwit mcp writes them (mcpserver/tools.go).
const message = `account 2
message 40 · thread 3 · folder 5 · 2026-10-23T08:00:00Z · flags \\Seen · has attachments
<untrusted_email id="message-40" field="from">Bob &lt;bob@example.com&gt;</untrusted_email>
<untrusted_email id="message-40" field="to">alice@example.com</untrusted_email>
<untrusted_email id="message-40" field="subject">Re: &lt;b&gt;Plans&lt;/b&gt;</untrusted_email>
<untrusted_email id="message-40" field="snippet">Hi Alice</untrusted_email>
attachment 9 · 120 bytes
<untrusted_email id="message-40" field="attachment_name">notes.txt</untrusted_email>
<untrusted_email id="message-40" field="body">Hi Alice,
<img src=x onerror=alert(1)> ![pixel](https://example.com/p.png)
[click](https://example.com) &amp; https://example.com</untrusted_email>
`;

const thread = `thread 3 · 2 messages
<untrusted_email id="thread-3" field="subject">Plans</untrusted_email>

message 39 · thread 3 · folder 5 · 2026-10-22T08:00:00Z
<untrusted_email id="message-39" field="from">alice@example.com</untrusted_email>
<untrusted_email id="message-39" field="subject">Plans</untrusted_email>
<untrusted_email id="message-39" field="snippet">Lunch on Friday?</untrusted_email>

message 40 · thread 3 · folder 5 · 2026-10-23T08:00:00Z
<untrusted_email id="message-40" field="from">Bob &lt;bob@example.com&gt;</untrusted_email>
<untrusted_email id="message-40" field="subject">Re: Plans</untrusted_email>
<untrusted_email id="message-40" field="snippet">Hi Alice</untrusted_email>
`;

const result = (text: string, isError = false): Result => ({
  content: [{ type: "text", text }],
  isError,
});

test("a message renders its sender, recipients, subject and body as text", () => {
  const root = document.createElement("div");
  render(root, result(message));
  const text = root.textContent ?? "";
  expect(text).toContain("Bob <bob@example.com>");
  expect(text).toContain("alice@example.com");
  expect(text).toContain("Re: <b>Plans</b>");
  expect(text).toContain("<img src=x onerror=alert(1)> ![pixel](https://example.com/p.png)");
  expect(text).toContain("[click](https://example.com) & https://example.com");
  expect(text).toContain("notes.txt");
  expect(text).not.toContain("truncated");
  expect(root.querySelector("img, a, b, script")).toBeNull();
});

test("a truncated body says so", () => {
  const root = document.createElement("div");
  render(root, result(`${message}(body truncated)\n`));
  expect(root.querySelector(".truncated")?.textContent).toMatch(/truncated/i);
});

test("a thread lists its messages and opens one's body in the same card", async () => {
  const root = document.createElement("div");
  const opened: number[] = [];
  render(root, result(thread), async (id) => {
    opened.push(id);
    return result(message);
  });
  const text = root.textContent ?? "";
  expect(text).toContain("Plans");
  expect(text).toContain("Lunch on Friday?");
  expect(text).toContain("Bob <bob@example.com>");
  expect(text).not.toContain("onerror");

  const buttons = root.querySelectorAll("button");
  expect(buttons).toHaveLength(2);
  buttons[1].click();
  await new Promise((r) => setTimeout(r));
  expect(opened).toEqual([40]);
  expect(root.textContent).toContain("[click](https://example.com) & https://example.com");
  expect(root.querySelector("img, a, b, script")).toBeNull();
});

test("an error result shows its text", () => {
  const root = document.createElement("div");
  render(root, result("message not found", true));
  expect(root.textContent).toBe("message not found");
});
