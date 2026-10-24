// show_draft's view (bun run views builds it into mcpserver/views/draft.html).

import { callTool, connect, text } from "./bridge";
import { type Actions, render } from "./draft";

const root = document.body;
// Send goes through the host as send_draft, so the user's Send through MCP setting decides (ADR 0023).
const actions: Actions = {
  send: (id, revision) => callTool("send_draft", { draft_id: id, revision }),
  reload: (id) => callTool("get_draft", { draft_id: id }),
};
connect("draft", async (r) => {
  render(root, r, "", actions);
  if (r.isError) return;
  // The address is ours, not mail's; a host that lets views call no tools keeps the number.
  const accounts = await callTool("list_accounts", {}).catch(() => undefined);
  if (accounts && !accounts.isError) render(root, r, text(accounts), actions);
});
