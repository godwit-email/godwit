import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { MenuEntry } from "@/mygo";
import { makeT } from "@/shared/i18n";
import { presets, SendBar } from "./SendBar";
import { FACE, facePoint } from "./SendLaterDialog";

const t = makeT("zh-Hans");

/** pick is the item chosen from the native menu, by its label. */
function setup(frozen = false, sending = false, pick = "") {
  const onSend = vi.fn();
  const onSendLater = vi.fn();
  const menu = vi.fn(
    async (entries: MenuEntry[], _x: number, _y: number) =>
      entries.find((e) => pick && e.label.startsWith(pick))?.id ?? "",
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
  await userEvent.click(screen.getByRole("button", { name: "定时发送" }));
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
    // The time field types it, the face follows.
    const field = screen.getByLabelText("时间", { selector: "input" });
    expect(field).toHaveValue("17:05");
    fireEvent.change(field, { target: { value: "05:20" } });
    expect(screen.getByText("今天 · 11月15日周日 5:20")).toBeInTheDocument();
    expect(clock).toHaveAttribute("aria-valuetext", "5:20");
    fireEvent.change(field, { target: { value: "17:05" } });
    await userEvent.click(screen.getByRole("button", { name: /11月16日/ }));
    expect(screen.getByText("明天 · 11月16日周一 17:05")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /11月14日/ })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "定时发送" }));
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

  /** A pointer at turn (0 at 12, clockwise) and r from the centre, the face laid out 100 px wide, as its viewBox. */
  const point = (clock: HTMLElement) => {
    vi.spyOn(clock, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 100, 100));
    return (turn: number, r: number) => {
      const [clientX, clientY] = facePoint(turn, r);
      return { clientX, clientY, pointerId: 1 };
    };
  };

  test("the minute hand drags round, and past 12 it carries the hour", async () => {
    const { clock } = await open();
    const at = point(clock);
    const tip = FACE.minute.length;
    fireEvent.pointerDown(clock, at(0, tip));
    fireEvent.pointerMove(clock, at(1 / 4, tip));
    expect(clock).toHaveAttribute("aria-valuetext", "16:15");
    for (const turn of [1 / 2, 3 / 4, 1]) fireEvent.pointerMove(clock, at(turn, tip));
    fireEvent.pointerUp(clock, at(1, tip));
    expect(clock).toHaveAttribute("aria-valuetext", "17:00");
  });

  test("the hour hand drags back through 12 into the morning", async () => {
    const { clock } = await open();
    const at = point(clock);
    const tip = FACE.hour.length; // at 16:00 it points to 4
    fireEvent.pointerDown(clock, at(4 / 12, tip));
    fireEvent.pointerMove(clock, at(0, tip));
    fireEvent.pointerMove(clock, at(10 / 12, tip));
    fireEvent.pointerUp(clock, at(10 / 12, tip));
    expect(clock).toHaveAttribute("aria-valuetext", "10:00");
  });

  test("a cleared time field schedules nothing", async () => {
    const { onSendLater } = await open();
    fireEvent.change(screen.getByLabelText("时间", { selector: "input" }), {
      target: { value: "" },
    });
    await userEvent.click(screen.getByRole("button", { name: "定时发送" }));
    expect(onSendLater).not.toHaveBeenCalled();
  });

  test("a past time is refused, in the header", async () => {
    const { onSendLater, clock } = await open();
    fireEvent.keyDown(clock, { key: "ArrowDown" });
    fireEvent.keyDown(clock, { key: "ArrowDown" });
    await userEvent.click(screen.getByRole("button", { name: "定时发送" }));
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
