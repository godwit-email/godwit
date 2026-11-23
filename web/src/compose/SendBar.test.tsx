import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { MenuEntry } from "@/mygo";
import { makeT } from "@/shared/i18n";
import { presets, SendBar } from "./SendBar";

const t = makeT("zh-Hans");

/**
 * pick is the item chosen from the native menu, by its label; chosen, the time native's send later
 * dialog gives back (0: cancelled).
 */
function setup(frozen = false, sending = false, pick = "", scheduledFor = 0, chosen = 0) {
  const onSend = vi.fn();
  const onSendLater = vi.fn();
  const menu = vi.fn(
    async (entries: MenuEntry[], _x: number, _y: number) =>
      entries.find((e) => pick && e.label.startsWith(pick))?.id ?? "",
  );
  const later = vi.fn(async (_at: number) => chosen);
  render(
    <SendBar
      sending={sending}
      frozen={frozen}
      scheduledFor={scheduledFor}
      onSend={onSend}
      onSendLater={onSendLater}
      menu={menu}
      later={later}
      t={t}
    />,
  );
  return { onSend, onSendLater, menu, later };
}

const options = () => userEvent.click(screen.getByRole("button", { name: "发送选项" }));

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

test("the native menu: send now, the presets, send later…", async () => {
  const { menu } = setup();
  await options();
  const [entries, x, y] = menu.mock.calls[0];
  const items = entries.map((e) => (e.separator ? "" : e.label));
  expect(items[0]).toBe("立即发送");
  expect(items[1]).toBe("");
  expect(items.at(-2)).toBe("");
  expect(items.at(-1)).toBe("稍后发送…");
  expect(items).toContainEqual(expect.stringMatching(/^明天 .*发送$/));
  expect([typeof x, typeof y]).toEqual(["number", "number"]);
});

test("the native menu: send now sends", async () => {
  const { onSend, onSendLater } = setup(false, false, "立即发送");
  await options();
  expect(onSend).toHaveBeenCalledTimes(1);
  expect(onSendLater).not.toHaveBeenCalled();
});

test("the native menu: tomorrow sends at 8:00", async () => {
  const { onSend, onSendLater } = setup(false, false, "明天");
  await options();
  const now = new Date();
  const tomorrow = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 8, 0);
  expect(onSendLater).toHaveBeenCalledWith(tomorrow.getTime());
  expect(onSend).not.toHaveBeenCalled();
});

test("the native menu: closed with nothing chosen does nothing", async () => {
  const { onSend, onSendLater } = setup();
  await options();
  expect(onSend).not.toHaveBeenCalled();
  expect(onSendLater).not.toHaveBeenCalled();
  expect(screen.queryByRole("alertdialog")).toBeNull();
});

test("send later…: native's dialog, at the next whole hour, schedules the time chosen", async () => {
  const { onSend, onSendLater, later } = setup(false, false, "稍后发送", 0, 1_900_000_000_000);
  await options();
  const next = new Date();
  next.setHours(next.getHours() + 1, 0, 0, 0);
  expect(later).toHaveBeenCalledExactlyOnceWith(next.getTime());
  expect(onSendLater).toHaveBeenCalledExactlyOnceWith(1_900_000_000_000);
  expect(onSend).not.toHaveBeenCalled();
});

test("send later…: cancelled, it schedules nothing", async () => {
  const { onSend, onSendLater, later } = setup(false, false, "稍后发送");
  await options();
  expect(later).toHaveBeenCalledTimes(1);
  expect(onSendLater).not.toHaveBeenCalled();
  expect(onSend).not.toHaveBeenCalled();
});

describe("at 15:30 on Sunday 15 November 2026", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2026, 10, 15, 15, 30));
    vi.spyOn(navigator, "language", "get").mockReturnValue("zh-Hans"); // the menu's locale
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  const original = new Date(2026, 10, 17, 8, 17, 42, 321).getTime();
  const nextHour = new Date(2026, 10, 15, 16, 0).getTime();

  test("原定时间列在其他预设前，选中后按原毫秒值排定", async () => {
    const { menu, onSendLater, later } = setup(false, false, "按原定时间发送", original);
    expect(onSendLater).not.toHaveBeenCalled();
    await options();
    expect(menu.mock.calls[0][0][2].label).toBe("按原定时间发送（11月17日周二 8:17）");
    expect(onSendLater).toHaveBeenCalledExactlyOnceWith(original);
    expect(later).not.toHaveBeenCalled();
  });

  test("稍后发送…预填原定时间，按选回的时间排定", async () => {
    const chosen = new Date(2026, 10, 18, 9, 17).getTime();
    const { onSendLater, later } = setup(false, false, "稍后发送", original, chosen);
    await options();
    expect(later).toHaveBeenCalledExactlyOnceWith(original);
    expect(onSendLater).toHaveBeenCalledExactlyOnceWith(chosen);
  });

  test.each([-1, 0])("原定时间距离现在 %i 毫秒时不推荐，对话框回到下一整点", async (offset) => {
    const { menu, onSendLater, later } = setup(false, false, "稍后发送", Date.now() + offset);
    await options();
    expect(menu.mock.calls[0][0].some((e) => e.label.startsWith("按原定时间发送"))).toBe(false);
    expect(later).toHaveBeenCalledExactlyOnceWith(nextHour);
    expect(onSendLater).not.toHaveBeenCalled();
  });

  test("菜单打开期间原定时间已过，选择后要重新挑选时间", async () => {
    const { menu, onSendLater, later } = setup(false, false, "", original);
    menu.mockImplementationOnce(async (entries) => {
      vi.setSystemTime(original);
      return entries.find((e) => e.label.startsWith("按原定时间发送"))?.id ?? "";
    });
    await options();
    expect(later).toHaveBeenCalledExactlyOnceWith(new Date(2026, 10, 17, 9, 0).getTime());
    expect(onSendLater).not.toHaveBeenCalled();
  });
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
