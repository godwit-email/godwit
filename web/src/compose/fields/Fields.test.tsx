import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef, useState } from "react";
import { makeT } from "@/shared/i18n";
import { Fields, type FieldsEdit, type FieldsHandle } from "./Fields";

const t = makeT({
  "compose.from": "发件人",
  "compose.from.select": "发件账户",
  "compose.to": "收件人",
  "compose.to.input": "添加收件人",
  "compose.cc_bcc": "抄送 · 密送",
  "compose.cc": "抄送",
  "compose.bcc": "密送",
  "compose.reply_to": "回复地址",
  "compose.subject": "主题",
  "compose.invalid": "地址无效：%s",
  "compose.remove": "移除 %s",
});

const accounts = [
  { id: 1, email: "me@x.com" },
  { id: 2, email: "work@x.com" },
];

// A stateful host, as the page is: edits flow back in as props.
function setup(
  init: Partial<Record<"to" | "cc" | "bcc", string[]> & { replyTo: string }> = {},
  frozen = false,
) {
  const onChange = vi.fn();
  const onFrom = vi.fn();
  const ref = createRef<FieldsHandle>();
  function Host() {
    const [v, setV] = useState({ to: [], cc: [], bcc: [], replyTo: "", subject: "", ...init });
    return (
      <Fields
        ref={ref}
        accounts={accounts}
        from={1}
        onFrom={onFrom}
        frozen={frozen}
        {...v}
        onChange={(e: FieldsEdit) => {
          onChange(e);
          setV((o) => ({ ...o, ...e }));
        }}
        t={t}
      />
    );
  }
  render(<Host />);
  return { onChange, onFrom, ref };
}

const chips = () => screen.queryAllByRole("button", { name: /^移除 / }).map((b) => b.ariaLabel);

test("typing an address and a comma makes a chip", async () => {
  const { onChange } = setup();
  await userEvent.type(screen.getByRole("textbox", { name: "收件人" }), "a@b.c,");
  expect(onChange).toHaveBeenLastCalledWith({ to: ["a@b.c"] });
  expect(chips()).toEqual(["移除 a@b.c"]);
  expect(screen.getByRole("textbox", { name: "收件人" })).toHaveValue("");
  await userEvent.type(screen.getByRole("textbox", { name: "收件人" }), "c@d.e{Enter}");
  expect(chips()).toEqual(["移除 a@b.c", "移除 c@d.e"]);
});

test("blur commits pending text", async () => {
  setup();
  await userEvent.type(screen.getByRole("textbox", { name: "收件人" }), "a@b.c");
  await userEvent.click(screen.getByRole("textbox", { name: "主题" }));
  expect(chips()).toEqual(["移除 a@b.c"]);
});

test("commitPending commits text still in the input", async () => {
  const { onChange, ref } = setup({ replyTo: "r@x.io" });
  await userEvent.type(screen.getByRole("textbox", { name: "收件人" }), "a@b.c");
  fireEvent.change(screen.getByRole("textbox", { name: "回复地址" }), {
    target: { value: "n@x.io" },
  });
  act(() => ref.current?.commitPending());
  expect(onChange).toHaveBeenCalledWith({ to: ["a@b.c"] });
  expect(onChange).toHaveBeenCalledWith({ replyTo: "n@x.io" });
  expect(screen.getByRole("textbox", { name: "收件人" })).toHaveValue("");
});

test("× removes", async () => {
  const { onChange } = setup({ to: ["a@b.c", "c@d.e"] });
  await userEvent.click(screen.getByRole("button", { name: "移除 a@b.c" }));
  expect(onChange).toHaveBeenLastCalledWith({ to: ["c@d.e"] });
  expect(chips()).toEqual(["移除 c@d.e"]);
});

test("Cc, Bcc and Reply-To hidden until 抄送 · 密送", async () => {
  setup();
  expect(screen.queryByRole("textbox", { name: "抄送" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "抄送 · 密送" }));
  for (const name of ["抄送", "密送", "回复地址"])
    expect(screen.getByRole("textbox", { name })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "抄送 · 密送" })).not.toBeInTheDocument();
});

test("Cc shown when any of them has a value", () => {
  setup({ bcc: ["b@x.io"] });
  expect(screen.getByRole("textbox", { name: "抄送" })).toBeInTheDocument();
});

test("invalid addresses are listed", () => {
  setup({ to: ["ok@x.io", "bad"], cc: ["worse"] });
  expect(screen.getByText("地址无效：bad, worse")).toBeInTheDocument();
});

test("choosing another account calls onFrom", async () => {
  const { onFrom } = setup();
  await userEvent.click(screen.getByRole("combobox", { name: "发件账户" }));
  await userEvent.click(await screen.findByRole("option", { name: "work@x.com" }));
  expect(onFrom).toHaveBeenCalledWith(2);
});

test("subject edits", async () => {
  const { onChange } = setup();
  await userEvent.type(screen.getByRole("textbox", { name: "主题" }), "Hi");
  expect(onChange).toHaveBeenLastCalledWith({ subject: "Hi" });
});

test("pasting several addresses at once makes chips of all", () => {
  setup();
  fireEvent.change(screen.getByRole("textbox", { name: "收件人" }), {
    target: { value: "a@b.c, c@d.e" },
  });
  expect(chips()).toEqual(["移除 a@b.c", "移除 c@d.e"]);
});

test("a comma inside a quoted name does not commit", async () => {
  setup();
  await userEvent.type(screen.getByRole("textbox", { name: "收件人" }), '"Doe, J." <j@x.io>,');
  expect(chips()).toEqual(['移除 "Doe, J." <j@x.io>']);
});

test("frozen: read-only, nothing committed, removed or switched", async () => {
  const { onChange, onFrom, ref } = setup({ to: ["a@b.c"] }, true);
  const to = screen.getByRole("textbox", { name: "收件人" });
  expect(to).toHaveAttribute("readonly");
  expect(screen.getByRole("textbox", { name: "主题" })).toHaveAttribute("readonly");
  fireEvent.change(to, { target: { value: "x@y.z," } });
  fireEvent.change(screen.getByRole("textbox", { name: "主题" }), { target: { value: "s" } });
  act(() => ref.current?.commitPending());
  await userEvent.click(screen.getByRole("button", { name: "移除 a@b.c" }));
  await userEvent.click(screen.getByRole("combobox", { name: "发件账户" }));
  expect(screen.queryByRole("option", { name: "work@x.com" })).not.toBeInTheDocument();
  expect(onChange).not.toHaveBeenCalled();
  expect(onFrom).not.toHaveBeenCalled();
  expect(chips()).toEqual(["移除 a@b.c"]);
});

test("while an IME composes, neither a comma nor leaving commits", () => {
  setup();
  const to = screen.getByRole("textbox", { name: "收件人" });
  fireEvent.compositionStart(to);
  fireEvent.change(to, { target: { value: "ni," } });
  fireEvent.blur(to);
  expect(chips()).toEqual([]);
  expect(to).toHaveValue("ni,");
  fireEvent.compositionEnd(to);
  fireEvent.change(to, { target: { value: "你@x.io," } });
  expect(chips()).toEqual(["移除 你@x.io"]);
});

test("rows read 收件人, 抄送 · 密送's, 主题, then 发件人", () => {
  const { container } = render(
    <Fields
      accounts={accounts}
      from={1}
      onFrom={vi.fn()}
      to={[]}
      cc={["c@x.com"]}
      bcc={[]}
      replyTo=""
      subject=""
      onChange={vi.fn()}
      t={t}
    />,
  );
  const labels = [...container.querySelectorAll("label")].map((l) => l.textContent);
  expect(labels).toEqual(["收件人", "抄送", "密送", "回复地址", "主题", "发件人"]);
});
