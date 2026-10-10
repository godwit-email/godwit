import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeT } from "@/shared/i18n";
import { SendBar } from "./SendBar";

const t = makeT({
  "compose.send": "发送",
  "compose.discard": "丢弃草稿",
  "compose.discard.title": "丢弃这封草稿？",
  "compose.discard.body": "草稿和它的附件会被删除，无法恢复。",
  "compose.discard.confirm": "丢弃",
  "common.cancel": "取消",
  "compose.note": "发送永远需要你亲自点击，插件和 agent 只能写草稿",
  "compose.status.saving": "正在保存…",
  "compose.status.saved": "草稿已保存 · %s",
  "compose.status.unsaved": "未保存",
});

function setup(status: "saving" | "saved" | "unsaved" = "saved") {
  const onSend = vi.fn();
  const onDiscard = vi.fn();
  render(<SendBar sending={false} status={status} savedAt="21:58" onSend={onSend} onDiscard={onDiscard} t={t} />);
  return { onSend, onDiscard };
}

test("send and note", async () => {
  const { onSend } = setup();
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  expect(onSend).toHaveBeenCalled();
  expect(screen.getByText(/发送永远需要/)).toBeTruthy();
});

test("discard asks; cancel does nothing, confirm discards", async () => {
  const { onDiscard } = setup();
  await userEvent.click(screen.getByRole("button", { name: "丢弃草稿" }));
  expect(screen.getByText("丢弃这封草稿？")).toBeTruthy();
  await userEvent.click(screen.getByRole("button", { name: "取消" }));
  expect(onDiscard).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "丢弃草稿" }));
  await userEvent.click(screen.getByRole("button", { name: "丢弃" }));
  expect(onDiscard).toHaveBeenCalledTimes(1);
});

test.each([
  ["saving", "正在保存…"],
  ["saved", "草稿已保存 · 21:58"],
  ["unsaved", "未保存"],
] as const)("status %s", (s, text) => {
  setup(s);
  expect(screen.getByText(text)).toBeTruthy();
});
