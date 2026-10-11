import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeT } from "@/shared/i18n";
import { SendBar } from "./SendBar";

const t = makeT({ "compose.send": "发送" });

function setup(frozen = false, sending = false) {
  const onSend = vi.fn();
  render(<SendBar sending={sending} frozen={frozen} onSend={onSend} t={t} />);
  return { onSend };
}

test("send, and nothing else: no discard, no note", async () => {
  const { onSend } = setup();
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  expect(onSend).toHaveBeenCalled();
  expect(screen.getAllByRole("button")).toHaveLength(1);
  expect(screen.queryByText(/只能写草稿/)).toBeNull();
});

test("frozen or sending: send is disabled", () => {
  setup(true);
  expect(screen.getByRole("button", { name: "发送" })).toBeDisabled();
});

test("sending: send is disabled", () => {
  setup(false, true);
  expect(screen.getByRole("button", { name: "发送" })).toBeDisabled();
});
