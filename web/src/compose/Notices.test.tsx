import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeT } from "@/shared/i18n";
import { Notices } from "./Notices";

const t = makeT({
  "compose.warn": "发送前检查：%s",
  "compose.warn.from": "来自插件 %s（message.beforeSend）",
  "compose.send_anyway": "仍然发送",
  "compose.conflict": "草稿在别处被修改",
  "compose.conflict.keep": "保留我的版本",
  "compose.conflict.latest": "使用最新版本",
  "common.retry": "重试",
});

function setup(p: Partial<Parameters<typeof Notices>[0]> = {}) {
  const f = { onSendAnyway: vi.fn(), onKeepMine: vi.fn(), onUseLatest: vi.fn(), onRetry: vi.fn() };
  render(<Notices warnings={[]} blocked={null} conflict={false} latestFailed={false} {...f} t={t} {...p} />);
  return f;
}

test("warnings and send anyway", async () => {
  const f = setup({ warnings: [{ plugin: "lint", message: "no subject" }] });
  expect(screen.getByText("发送前检查：no subject")).toBeTruthy();
  expect(screen.getByText("来自插件 lint（message.beforeSend）")).toBeTruthy();
  await userEvent.click(screen.getByRole("button", { name: "仍然发送" }));
  expect(f.onSendAnyway).toHaveBeenCalled();
});

test("blocked is a danger alert without send anyway", () => {
  setup({ blocked: { plugin: "dlp", message: "secrets inside" } });
  expect(screen.getByRole("alert").textContent).toContain("secrets inside");
  expect(screen.getByText("来自插件 dlp（message.beforeSend）")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "仍然发送" })).toBeNull();
});

test("conflict choices, or retry when the latest failed", async () => {
  const f = setup({ conflict: true });
  expect(screen.getByText("草稿在别处被修改")).toBeTruthy();
  await userEvent.click(screen.getByRole("button", { name: "保留我的版本" }));
  await userEvent.click(screen.getByRole("button", { name: "使用最新版本" }));
  expect(f.onKeepMine).toHaveBeenCalled();
  expect(f.onUseLatest).toHaveBeenCalled();
});

test("retry on latestFailed", async () => {
  const f = setup({ conflict: true, latestFailed: true });
  expect(screen.queryByRole("button", { name: "使用最新版本" })).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "重试" }));
  expect(f.onRetry).toHaveBeenCalled();
});
