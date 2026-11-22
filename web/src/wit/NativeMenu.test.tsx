import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { MenuEntry, WitHistory } from "@/mygo";
import { makeT } from "@/shared/i18n";
import { Panel } from "./Panel";
import { WitTransport } from "./transport";

vi.mock("@/mygo", () => ({ events: { wit: { on: () => () => {} } } }));

const t = makeT("zh-Hans");

const empty: WitHistory = {
  conversation: 1,
  model: "",
  effort: "",
  usage: { input: 0, output: 0, context: 0, cached: 0, history: 0, summary: 0, compactable: false },
  running: 0,
  turns: [],
};

const models = [
  { ref: "a/sonnet", service: "A", name: "sonnet", default: true },
  { ref: "a/haiku", service: "A", name: "haiku", default: false },
  { ref: "b/gpt", service: "B", name: "gpt", default: false },
];

// The input in the app: its menus are the system's (popup), answered by `choose`.
function setup(choose: (entries: MenuEntry[]) => string = () => "") {
  const popup = vi.fn(async (entries: MenuEntry[], _x: number, _y: number) => choose(entries));
  const pick = vi.fn();
  const effortPick = vi.fn();
  const onModels = vi.fn();
  const onSearch = vi.fn().mockResolvedValue([]);
  const skills = vi.fn().mockResolvedValue([{ name: "triage", description: "Sort the inbox" }]);
  render(
    <Panel
      history={empty}
      locked={false}
      onLink={vi.fn()}
      transport={new WitTransport({ ask: vi.fn(), follow: vi.fn(), stop: vi.fn() }, () => () => {})}
      t={t}
      layout="page"
      model="sonnet"
      onModels={onModels}
      models={{ ref: "a/haiku", list: vi.fn().mockResolvedValue(models), pick }}
      effort={{ value: "medium", pick: effortPick }}
      onSearch={onSearch}
      skills={skills}
      popup={popup}
    />,
  );
  return { popup, pick, effortPick, onModels };
}

describe("the input's native menus", () => {
  it("the model chip opens the system menu: groups, the ticked model, the effort as a submenu", async () => {
    const user = userEvent.setup();
    const { popup, pick, effortPick, onModels } = setup((e) =>
      e.some((x) => x.id === "model:a/haiku") ? "model:a/haiku" : "",
    );
    const chip = screen.getByRole("button", { name: /haiku/ });
    await user.click(chip);
    await waitFor(() => expect(popup).toHaveBeenCalledOnce());
    const [entries, x, y] = popup.mock.calls[0];
    expect(entries.map((e) => e.label || "—")).toEqual([
      "A",
      "sonnet",
      "haiku",
      "—",
      "B",
      "gpt",
      "—",
      "思考强度",
      "—",
      "模型服务设置",
    ]);
    expect(entries[0]).toMatchObject({ header: true });
    // the default in its group, tagged, following the default again (no ref); haiku ticked
    expect(entries[1]).toMatchObject({ id: "model:", badge: "默认", radio: true });
    expect(entries[2]).toMatchObject({ id: "model:a/haiku", radio: true, checked: true });
    expect(entries[7]).toMatchObject({ id: "effort", badge: "中" });
    expect(entries[7].items?.map((e) => [e.id, e.checked ?? false])).toEqual([
      ["effort:", false],
      ["effort:low", false],
      ["effort:medium", true],
      ["effort:high", false],
    ]);
    expect(entries[9]).toMatchObject({ id: "settings", icon: "settings" });
    // under the chip's lower left corner
    expect([x, y]).toEqual([0, 0]);
    // no web menu beside the system's; the choice applies and the chip has the focus back
    expect(screen.queryByRole("menu")).toBeNull();
    await waitFor(() => expect(pick).toHaveBeenCalledWith("a/haiku"));
    expect(chip).toHaveFocus();
    expect(effortPick).not.toHaveBeenCalled();
    expect(onModels).not.toHaveBeenCalled();
  });

  it("a choice in the effort submenu or the settings entry is applied", async () => {
    const user = userEvent.setup();
    const answers = ["effort:high", "model:", "settings", ""];
    const { popup, pick, effortPick, onModels } = setup(() => answers.shift() ?? "");
    const chip = screen.getByRole("button", { name: /haiku/ });
    for (let i = 0; i < 4; i++) {
      await user.click(chip);
      await waitFor(() => expect(popup).toHaveBeenCalledTimes(i + 1));
    }
    await waitFor(() => expect(onModels).toHaveBeenCalledOnce());
    expect(effortPick).toHaveBeenCalledWith("high");
    expect(pick).toHaveBeenCalledOnce();
    expect(pick).toHaveBeenCalledWith("");
  });

  it("ArrowDown on the focused chip opens it as Enter does", async () => {
    const user = userEvent.setup();
    const { popup } = setup();
    screen.getByRole("button", { name: /haiku/ }).focus();
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(popup).toHaveBeenCalledOnce());
    await user.keyboard("{Enter}");
    await waitFor(() => expect(popup).toHaveBeenCalledTimes(2));
  });

  it("+ offers 引用邮件, which opens the quote picker", async () => {
    const user = userEvent.setup();
    const { popup } = setup(() => "quote");
    await user.click(screen.getByRole("button", { name: "添加" }));
    await waitFor(() => expect(popup).toHaveBeenCalledOnce());
    expect(popup.mock.calls[0][0]).toMatchObject([
      { id: "quote", label: "引用邮件", icon: "mail", badge: "@" },
    ]);
    expect(await screen.findByRole("dialog", { name: "引用邮件" })).toBeInTheDocument();
  });

  it("the / list stays inline in the app: it filters as the user types and the menu bridge is not used", async () => {
    const user = userEvent.setup();
    const { popup } = setup();
    const box = screen.getByRole("textbox");
    await user.type(box, "/");
    const list = await screen.findByRole("listbox", { name: "指令" });
    expect(within(list).getByRole("option", { name: /Triage/ })).toBeInTheDocument();
    expect(within(list).getByRole("option", { name: /思考强度/ })).toBeInTheDocument();
    await user.type(box, "思考");
    await waitFor(() => expect(screen.queryByRole("option", { name: /Triage/ })).toBeNull());
    expect(screen.getByRole("option", { name: /思考强度/ })).toBeInTheDocument();
    expect(box).toHaveFocus();
    expect(popup).not.toHaveBeenCalled();
  });
});
