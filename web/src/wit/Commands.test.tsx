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
