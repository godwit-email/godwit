// show_draft's view (bun run views builds it into mcpserver/views/draft.html).
import { callTool, connect, text } from "./bridge";
import { render } from "./draft";

const root = document.body;
connect("draft", async (r) => {
  render(root, r);
  if (r.isError) return;
  // The address is ours, not mail's; a host that lets views call no tools keeps the number.
  const accounts = await callTool("list_accounts", {}).catch(() => undefined);
  if (accounts && !accounts.isError) render(root, r, text(accounts));
});
