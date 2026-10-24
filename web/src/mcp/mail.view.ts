// show_mail's view (bun run views builds it into mcpserver/views/mail.html).
import { callTool, connect } from "./bridge";
import { render } from "./mail";

const root = document.body;
// Opening a thread's message calls get_message through the host: same card, no model turn.
connect("mail", (r) => render(root, r, (id) => callTool("get_message", { message_id: id })));
