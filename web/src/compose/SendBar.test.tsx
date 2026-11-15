import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeT } from "@/shared/i18n";
import { presets, SendBar } from "./SendBar";

const t = makeT("zh-Hans");

/** pick is the item chosen from the native menu, by its label. */
function setup(frozen = false, sending = false, pick = "") {
  const onSend = vi.fn();
  const onSendLater = vi.fn();
  const menu = vi.fn(async (items: string[], _x: number, _y: number) =>
    items.findIndex((l) => pick && l.startsWith(pick)),
  );
  render(
    <SendBar
      sending={sending}
      frozen={frozen}
      onSend={onSend}
      onSendLater={onSendLater}
      menu={menu}
      t={t}
    />,
  );
  return { onSend, onSendLater, menu };
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
  const [items, x, y] = menu.mock.calls[0];
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

test("send later…: a dialog for any time, that says only a running Godwit sends", async () => {
  const { onSend, onSendLater } = setup(false, false, "稍后发送");
  await options();
  expect(await screen.findByRole("alertdialog", { name: "稍后发送" })).toBeInTheDocument();
  expect(screen.getByText(/只在 Godwit 运行时发出/)).toBeInTheDocument();
  const next = new Date();
  next.setHours(next.getHours() + 1, 0, 0, 0);
  expect(screen.getByRole("slider", { name: "时间" })).toHaveAttribute(
    "aria-valuenow",
    String(next.getHours() * 60),
  );
  await userEvent.click(screen.getByRole("button", { name: "排定发送" }));
  expect(onSendLater).toHaveBeenCalledWith(next.getTime());
  expect(onSend).not.toHaveBeenCalled();
});

test("send later…: the close button in the corner closes it, scheduling nothing", async () => {
  const { onSendLater } = setup(false, false, "稍后发送");
  await options();
  await userEvent.click(await screen.findByRole("button", { name: "关闭" }));
  expect(screen.queryByRole("alertdialog")).toBeNull();
  expect(onSendLater).not.toHaveBeenCalled();
});

describe("at 15:30 on Sunday 15 November 2026", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2026, 10, 15, 15, 30));
    vi.spyOn(navigator, "language", "get").mockReturnValue("zh-Hans"); // the header's and calendar's locale
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  const open = async () => {
    const r = setup(false, false, "稍后发送");
    await options();
    return { ...r, clock: await screen.findByRole("slider", { name: "时间" }) };
  };

  test("the keys step the clock, the calendar picks the day, the header reads it out", async () => {
    const { onSendLater, clock } = await open();
    expect(screen.getByText("今天 · 11月15日周日 16:00")).toBeInTheDocument();
    fireEvent.keyDown(clock, { key: "ArrowRight" });
    fireEvent.keyDown(clock, { key: "ArrowUp" });
    expect(screen.getByText("今天 · 11月15日周日 17:05")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "上午" }));
    expect(screen.getByText("今天 · 11月15日周日 5:05")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "下午" }));
    await userEvent.click(screen.getByRole("button", { name: /11月16日/ }));
    expect(screen.getByText("明天 · 11月16日周一 17:05")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /11月14日/ })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "排定发送" }));
    expect(onSendLater).toHaveBeenCalledWith(new Date(2026, 10, 16, 17, 5).getTime());
  });

  test("the arrow keys move through the calendar's days, Enter picks one", async () => {
    await open();
    screen.getByRole("button", { name: /11月15日/ }).focus();
    await userEvent.keyboard("{ArrowRight}");
    expect(screen.getByRole("button", { name: /11月16日/ })).toHaveFocus();
    await userEvent.keyboard("{ArrowDown}{Enter}");
    expect(screen.getByText("11月23日周一 16:00")).toBeInTheDocument();
  });

  test("the minute hand drags round, and past 12 it carries the hour", async () => {
    const { clock } = await open();
    // The face is 184 px: its centre at 92, the minute hand's tip at 12 near the top.
    const at = (x: number, y: number) => ({ clientX: x, clientY: y, pointerId: 1 });
    fireEvent.pointerDown(clock, at(92, 30));
    fireEvent.pointerMove(clock, at(180, 92));
    expect(clock).toHaveAttribute("aria-valuetext", "16:15");
    for (const [x, y] of [
      [92, 180],
      [4, 92],
      [92, 4],
    ])
      fireEvent.pointerMove(clock, at(x, y));
    fireEvent.pointerUp(clock, at(92, 4));
    expect(clock).toHaveAttribute("aria-valuetext", "17:00");
  });

  test("the hour hand drags back through 12 into the morning", async () => {
    const { clock } = await open();
    const at = (x: number, y: number) => ({ clientX: x, clientY: y, pointerId: 1 });
    // At 16:00 the hour hand points to 4, its tip 42 px out.
    fireEvent.pointerDown(clock, at(92 + 36, 92 + 21));
    fireEvent.pointerMove(clock, at(92, 4));
    fireEvent.pointerMove(clock, at(30, 56));
    fireEvent.pointerUp(clock, at(30, 56));
    expect(clock).toHaveAttribute("aria-valuetext", "10:00");
  });

  test("a past time is refused, in the header", async () => {
    const { onSendLater, clock } = await open();
    fireEvent.keyDown(clock, { key: "ArrowDown" });
    fireEvent.keyDown(clock, { key: "ArrowDown" });
    await userEvent.click(screen.getByRole("button", { name: "排定发送" }));
    expect(onSendLater).not.toHaveBeenCalled();
    expect(screen.getByText("请选择一个将来的时间。")).toBeInTheDocument();
    fireEvent.keyDown(clock, { key: "ArrowUp" });
    expect(screen.queryByText("请选择一个将来的时间。")).toBeNull();
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
