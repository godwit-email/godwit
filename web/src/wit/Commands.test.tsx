import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { makeT } from "@/shared/i18n";
import { type Command, CommandPicker } from "./Commands";
import type { PickerKeys } from "./Quote";

const t = makeT("zh-Hans");

it("a command's options open from the commands as they are now: a list that arrives later shows", async () => {
  const user = userEvent.setup();
  const keys = createRef<PickerKeys>();
  const model = (names: string[]): Command[] => [
    {
      key: "model",
      group: "对话",
      label: "模型",
      options: names.map((n) => ({ key: `model:${n}`, group: "对话", label: n, run: () => {} })),
    },
  ];
  const { rerender } = render(
    <CommandPicker commands={model([])} query="" onClose={() => {}} keys={keys} t={t} />,
  );
  await user.click(screen.getByRole("option", { name: "模型" }));
  expect(screen.queryByRole("option", { name: "Sonnet" })).toBeNull();
  rerender(
    <CommandPicker commands={model(["Sonnet"])} query="" onClose={() => {}} keys={keys} t={t} />,
  );
  expect(await screen.findByRole("option", { name: "Sonnet" })).toBeInTheDocument();
});

it("the query filters the commands by key or label as it grows", () => {
  const commands: Command[] = [
    { key: "new", group: "对话", label: "新对话", run: () => {} },
    { key: "effort", group: "对话", label: "思考强度", run: () => {} },
  ];
  const picker = (query: string) => (
    <CommandPicker commands={commands} query={query} onClose={() => {}} t={t} />
  );
  const { rerender } = render(picker(""));
  expect(screen.getAllByRole("option")).toHaveLength(2);
  rerender(picker("eff"));
  expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["思考强度"]);
  rerender(picker("新"));
  expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["新对话"]);
  rerender(picker("zzz"));
  expect(screen.queryByRole("option")).toBeNull();
});
