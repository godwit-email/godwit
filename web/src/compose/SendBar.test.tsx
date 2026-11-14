import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeT } from "@/shared/i18n";
import { localInput, SendBar } from "./SendBar";

const t = makeT("zh-Hans");

function setup(frozen = false, sending = false) {
  const onSend = vi.fn();
  const onSendLater = vi.fn();
  render(
    <SendBar sending={sending} frozen={frozen} onSend={onSend} onSendLater={onSendLater} t={t} />,
  );
  return { onSend, onSendLater };
}

test("send and send later, and nothing else: no discard, no note", async () => {
  const { onSend } = setup();
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  expect(onSend).toHaveBeenCalled();
  expect(screen.getAllByRole("button")).toHaveLength(2);
  expect(screen.queryByText(/只能写草稿/)).toBeNull();
});

test("send later: tomorrow at 8 by default, says only a running Godwit sends", async () => {
  const { onSend, onSendLater } = setup();
  await userEvent.click(screen.getByRole("button", { name: "稍后发送…" }));
  const now = new Date();
  const want = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 8, 0);
  expect(screen.getByLabelText("发送时间")).toHaveValue(localInput(want));
  expect(screen.getByText(/只在 Godwit 运行时发出/)).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "排定发送" }));
  expect(onSendLater).toHaveBeenCalledWith(want.getTime());
  expect(onSend).not.toHaveBeenCalled();
});

test("send later: a past time is refused", async () => {
  const { onSendLater } = setup();
  await userEvent.click(screen.getByRole("button", { name: "稍后发送…" }));
  fireEvent.change(screen.getByLabelText("发送时间"), { target: { value: "2000-01-01T08:00" } });
  await userEvent.click(screen.getByRole("button", { name: "排定发送" }));
  expect(onSendLater).not.toHaveBeenCalled();
  expect(screen.getByText("请选择一个将来的时间。")).toBeInTheDocument();
});

test("frozen or sending: send is disabled", () => {
  setup(true);
  expect(screen.getByRole("button", { name: "发送" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "稍后发送…" })).toBeDisabled();
});

test("sending: send is disabled", () => {
  setup(false, true);
  expect(screen.getByRole("button", { name: "发送" })).toBeDisabled();
});
