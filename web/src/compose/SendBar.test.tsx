import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeT } from "@/shared/i18n";
import { presets, SendBar } from "./SendBar";

const t = makeT("zh-Hans");

function setup(frozen = false, sending = false) {
  const onSend = vi.fn();
  const onSendLater = vi.fn();
  render(
    <SendBar sending={sending} frozen={frozen} onSend={onSend} onSendLater={onSendLater} t={t} />,
  );
  return { onSend, onSendLater };
}

test("send and its options, and nothing else: no discard, no note", async () => {
  const { onSend } = setup();
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  expect(onSend).toHaveBeenCalled();
  expect(screen.getAllByRole("button")).toHaveLength(2);
  expect(screen.queryByText(/只能写草稿/)).toBeNull();
});

test("presets: tonight at 21:00 only before then, tomorrow at 8:00", () => {
  const at = (p: { at: Date }[]) => p.map((x) => x.at.getTime());
  const day = (h: number, m = 0, d = 15) => new Date(2026, 10, d, h, m);
  expect(at(presets(day(10)))).toEqual([day(21).getTime(), day(8, 0, 16).getTime()]);
  expect(at(presets(day(21)))).toEqual([day(8, 0, 16).getTime()]);
  expect(at(presets(day(23, 30)))).toEqual([day(8, 0, 16).getTime()]);
});

test("the menu: send now, tomorrow, and the dialog", async () => {
  const { onSend, onSendLater } = setup();
  await userEvent.click(screen.getByRole("button", { name: "发送选项" }));
  await userEvent.click(await screen.findByRole("menuitem", { name: "立即发送" }));
  expect(onSend).toHaveBeenCalledTimes(1);
  await userEvent.click(screen.getByRole("button", { name: "发送选项" }));
  await userEvent.click(await screen.findByRole("menuitem", { name: /^明天 .*发送$/ }));
  const now = new Date();
  const tomorrow = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 8, 0);
  expect(onSendLater).toHaveBeenCalledWith(tomorrow.getTime());
});

test("send later…: a dialog for any time, that says only a running Godwit sends", async () => {
  const { onSend, onSendLater } = setup();
  await userEvent.click(screen.getByRole("button", { name: "发送选项" }));
  await userEvent.click(await screen.findByRole("menuitem", { name: "稍后发送…" }));
  expect(await screen.findByRole("alertdialog", { name: "稍后发送" })).toBeInTheDocument();
  expect(screen.getByText(/只在 Godwit 运行时发出/)).toBeInTheDocument();
  const next = new Date();
  next.setHours(next.getHours() + 1, 0, 0, 0);
  await userEvent.click(screen.getByRole("button", { name: "排定发送" }));
  expect(onSendLater).toHaveBeenCalledWith(next.getTime());
  expect(onSend).not.toHaveBeenCalled();
});

test("send later…: a past time is refused", async () => {
  const { onSendLater } = setup();
  await userEvent.click(screen.getByRole("button", { name: "发送选项" }));
  await userEvent.click(await screen.findByRole("menuitem", { name: "稍后发送…" }));
  fireEvent.change(screen.getByLabelText("日期"), { target: { value: "2000-01-01" } });
  await userEvent.click(screen.getByRole("button", { name: "排定发送" }));
  expect(onSendLater).not.toHaveBeenCalled();
  expect(screen.getByText("请选择一个将来的时间。")).toBeInTheDocument();
});

test("frozen or sending: send is disabled", () => {
  setup(true);
  expect(screen.getByRole("button", { name: "发送" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "发送选项" })).toBeDisabled();
});

test("sending: send is disabled", () => {
  setup(false, true);
  expect(screen.getByRole("button", { name: "发送" })).toBeDisabled();
});
