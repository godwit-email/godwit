import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef, type Ref, useState } from "react";
import type { ContactHit } from "@/mygo";
import { makeT } from "@/shared/i18n";
import { Fields, type FieldsEdit, type FieldsHandle } from "./Fields";

const t = makeT("zh-Hans");

const accounts = [
  { id: 1, email: "me@x.com" },
  { id: 2, email: "work@x.com" },
];

type Init = Partial<Record<"to" | "cc" | "bcc", string[]> & { replyTo: string }>;

// A stateful host, as the page is: edits flow back in as props.
function Host({
  init,
  frozen,
  onChange,
  onFrom,
  onSearch,
  fieldsRef,
}: {
  init: Init;
  frozen: boolean;
  onSearch?: (q: string) => Promise<ContactHit[]>;
  onChange: (e: FieldsEdit) => void;
  onFrom: (id: number) => void;
  fieldsRef: Ref<FieldsHandle>;
}) {
  const [v, setV] = useState({ to: [], cc: [], bcc: [], replyTo: "", subject: "", ...init });
  return (
    <Fields
      ref={fieldsRef}
      accounts={accounts}
      from={1}
      onFrom={onFrom}
      frozen={frozen}
      onSearch={onSearch}
      {...v}
      onChange={(e: FieldsEdit) => {
        onChange(e);
        setV((o) => ({ ...o, ...e }));
      }}
      t={t}
    />
  );
}

function setup(
  init: Init = {},
  frozen = false,
  onSearch: ((q: string) => Promise<ContactHit[]>) | undefined = undefined,
) {
  const onChange = vi.fn();
  const onFrom = vi.fn();
  const ref = createRef<FieldsHandle>();
  render(
    <Host
      init={init}
      frozen={frozen}
      onChange={onChange}
      onFrom={onFrom}
      onSearch={onSearch}
      fieldsRef={ref}
    />,
  );
  return { onChange, onFrom, ref };
}

const chips = () => screen.queryAllByRole("button", { name: /^移除 / }).map((b) => b.ariaLabel);

test("typing an address and a comma makes a chip", async () => {
  const { onChange } = setup();
  await userEvent.type(screen.getByRole("combobox", { name: "收件人" }), "a@b.c,");
  expect(onChange).toHaveBeenLastCalledWith({ to: ["a@b.c"] });
  expect(chips()).toEqual(["移除 a@b.c"]);
  expect(screen.getByRole("combobox", { name: "收件人" })).toHaveValue("");
  await userEvent.type(screen.getByRole("combobox", { name: "收件人" }), "c@d.e{Enter}");
  expect(chips()).toEqual(["移除 a@b.c", "移除 c@d.e"]);
});

test("blur commits pending text", async () => {
  setup();
  await userEvent.type(screen.getByRole("combobox", { name: "收件人" }), "a@b.c");
  await userEvent.click(screen.getByRole("textbox", { name: "主题" }));
  expect(chips()).toEqual(["移除 a@b.c"]);
});

test("commitPending commits text still in the input", async () => {
  const { onChange, ref } = setup({ replyTo: "r@x.io" });
  await userEvent.type(screen.getByRole("combobox", { name: "收件人" }), "a@b.c");
  fireEvent.change(screen.getByRole("combobox", { name: "回复地址" }), {
    target: { value: "n@x.io" },
  });
  act(() => ref.current?.commitPending());
  expect(onChange).toHaveBeenCalledWith({ to: ["a@b.c"] });
  expect(onChange).toHaveBeenCalledWith({ replyTo: "n@x.io" });
  expect(screen.getByRole("combobox", { name: "收件人" })).toHaveValue("");
});

test("× removes", async () => {
  const { onChange } = setup({ to: ["a@b.c", "c@d.e"] });
  await userEvent.click(screen.getByRole("button", { name: "移除 a@b.c" }));
  expect(onChange).toHaveBeenLastCalledWith({ to: ["c@d.e"] });
  expect(chips()).toEqual(["移除 c@d.e"]);
});

test("a chip shows the display name; the address is in its tooltip", async () => {
  setup({ to: ["Lena Fischer <lena@partner.example>", '"Doe, J." <j@x.io>', "a@b.c"] });
  const names = screen.getAllByTestId("chip").map((c) => c.firstChild?.textContent);
  expect(names).toEqual(["Lena Fischer", "Doe, J.", "a@b.c"]);
  await userEvent.hover(screen.getByText("Lena Fischer"));
  expect(await screen.findByText("Lena Fischer <lena@partner.example>")).toBeInTheDocument();
});

test("a chip is labelled with name and address; an invalid one shows the entry as typed", () => {
  setup({ to: ["Lena Fischer <lena@partner.example>", "Bob <bob>"] });
  const [ok, bad] = screen.getAllByTestId("chip");
  expect(ok).toHaveAccessibleName("Lena Fischer <lena@partner.example>");
  expect(bad.firstChild?.textContent).toBe("Bob <bob>");
});

test("Cc, Bcc and Reply-To hidden until 抄送 · 密送", async () => {
  setup();
  expect(screen.queryByRole("combobox", { name: "抄送" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "抄送 · 密送" }));
  for (const name of ["抄送", "密送", "回复地址"])
    expect(screen.getByRole("combobox", { name })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "抄送 · 密送" })).not.toBeInTheDocument();
});

test("Cc shown when any of them has a value", () => {
  setup({ bcc: ["b@x.io"] });
  expect(screen.getByRole("combobox", { name: "抄送" })).toBeInTheDocument();
});

test("invalid addresses are listed", () => {
  setup({ to: ["ok@x.io", "bad"], cc: ["worse"] });
  expect(screen.getByText("地址无效：bad, worse")).toBeInTheDocument();
});

test("choosing another account calls onFrom", async () => {
  const { onFrom } = setup();
  const from = screen.getByRole("button", { name: "发件账户" });
  expect(from).toHaveTextContent("me@x.com"); // the pill shows the current account
  await userEvent.click(from);
  // the menu checks the one in use, then picks the other
  expect(await screen.findByRole("menuitemradio", { name: "me@x.com" })).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await userEvent.click(screen.getByRole("menuitemradio", { name: "work@x.com" }));
  expect(onFrom).toHaveBeenCalledWith(2);
});

test("subject edits", async () => {
  const { onChange } = setup();
  await userEvent.type(screen.getByRole("textbox", { name: "主题" }), "Hi");
  expect(onChange).toHaveBeenLastCalledWith({ subject: "Hi" });
});

test("pasting several addresses at once makes chips of all", () => {
  setup();
  fireEvent.change(screen.getByRole("combobox", { name: "收件人" }), {
    target: { value: "a@b.c, c@d.e" },
  });
  expect(chips()).toEqual(["移除 a@b.c", "移除 c@d.e"]);
});

test("a comma inside a quoted name does not commit", async () => {
  setup();
  await userEvent.type(screen.getByRole("combobox", { name: "收件人" }), '"Doe, J." <j@x.io>,');
  expect(chips()).toEqual(['移除 "Doe, J." <j@x.io>']);
});

test("frozen: read-only, nothing committed, removed or switched", async () => {
  const { onChange, onFrom, ref } = setup({ to: ["a@b.c"] }, true);
  const to = screen.getByRole("combobox", { name: "收件人" });
  expect(to).toHaveAttribute("readonly");
  expect(screen.getByRole("textbox", { name: "主题" })).toHaveAttribute("readonly");
  fireEvent.change(to, { target: { value: "x@y.z," } });
  fireEvent.change(screen.getByRole("textbox", { name: "主题" }), { target: { value: "s" } });
  act(() => ref.current?.commitPending());
  await userEvent.click(screen.getByRole("button", { name: "移除 a@b.c" }));
  expect(screen.getByRole("button", { name: "发件账户" })).toBeDisabled();
  expect(onChange).not.toHaveBeenCalled();
  expect(onFrom).not.toHaveBeenCalled();
  expect(chips()).toEqual(["移除 a@b.c"]);
});

test("a menu left open when the fields freeze switches nothing", async () => {
  const onFrom = vi.fn();
  const host = (frozen: boolean) => (
    <Host init={{}} frozen={frozen} onChange={vi.fn()} onFrom={onFrom} fieldsRef={createRef()} />
  );
  const { rerender } = render(host(false));
  await userEvent.click(screen.getByRole("button", { name: "发件账户" }));
  const work = await screen.findByRole("menuitemradio", { name: "work@x.com" });
  rerender(host(true));
  await userEvent.click(work);
  expect(onFrom).not.toHaveBeenCalled();
});

test("while an IME composes, neither a comma nor leaving commits", () => {
  setup();
  const to = screen.getByRole("combobox", { name: "收件人" });
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

const contacts: ContactHit[] = [
  { name: "Zhang, San", address: "zhang@acme.io", entry: '"Zhang, San" <zhang@acme.io>' },
  { name: "Zhao Si", address: "zhao@acme.io", entry: "Zhao Si <zhao@acme.io>" },
  { name: "", address: "zed@acme.io", entry: "zed@acme.io" },
];
const search = (q: string) =>
  Promise.resolve(
    contacts.filter((c) => `${c.name} ${c.address}`.toLowerCase().includes(q.toLowerCase())),
  );
const to = () => screen.getByRole("combobox", { name: "收件人" });
const options = () => screen.queryAllByRole("option").map((o) => o.textContent);
const active = () =>
  screen.getAllByRole("option").find((o) => o.ariaSelected === "true")?.textContent;

test("typing lists matching contacts, name and address", async () => {
  setup({}, false, search);
  await userEvent.type(to(), "z");
  expect(await screen.findAllByRole("option")).toHaveLength(3);
  expect(options()).toEqual(["Zhang, Sanzhang@acme.io", "Zhao Sizhao@acme.io", "zed@acme.io"]);
  expect(to()).toHaveAttribute("aria-expanded", "true");
});

test("a click commits the contact, nothing half-typed", async () => {
  const { onChange } = setup({}, false, search);
  await userEvent.type(to(), "zhang");
  await userEvent.click(await screen.findByRole("option"));
  expect(onChange).toHaveBeenCalledTimes(1);
  expect(onChange).toHaveBeenLastCalledWith({ to: ['"Zhang, San" <zhang@acme.io>'] });
  expect(to()).toHaveValue("");
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
});

test("arrows move, wrapping; Enter commits the active one", async () => {
  const { onChange } = setup({}, false, search);
  await userEvent.type(to(), "z");
  await screen.findAllByRole("option");
  expect(active()).toBe("Zhang, Sanzhang@acme.io"); // the first at once
  await userEvent.keyboard("{ArrowUp}");
  expect(active()).toBe("zed@acme.io");
  await userEvent.keyboard("{ArrowDown}{ArrowDown}");
  expect(active()).toBe("Zhao Sizhao@acme.io");
  await userEvent.keyboard("{Enter}");
  expect(onChange).toHaveBeenLastCalledWith({ to: ["Zhao Si <zhao@acme.io>"] });
});

test("an address typed whole: Enter commits it, not a contact", async () => {
  const { onChange } = setup({}, false, search);
  await userEvent.type(to(), "zed@acme.io");
  await screen.findByRole("option");
  expect(screen.getByRole("option")).toHaveAttribute("aria-selected", "false");
  await userEvent.keyboard("{Enter}");
  expect(onChange).toHaveBeenLastCalledWith({ to: ["zed@acme.io"] });
});

test("Escape closes the list and keeps the text", async () => {
  setup({}, false, search);
  await userEvent.type(to(), "zh");
  await screen.findAllByRole("option");
  await userEvent.keyboard("{Escape}");
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  expect(to()).toHaveValue("zh");
});

test("addresses already in the field are not offered", async () => {
  setup({ to: ["Zhao <ZHAO@acme.io>"] }, false, search);
  await userEvent.type(to(), "z");
  await screen.findAllByRole("option");
  expect(options()).toEqual(["Zhang, Sanzhang@acme.io", "zed@acme.io"]);
});

test("a late answer to older text does not show", async () => {
  const slow: Record<string, (h: ContactHit[]) => void> = {};
  setup({}, false, (q) => new Promise((r) => (slow[q] = r)));
  await userEvent.type(to(), "z");
  await waitFor(() => expect(slow.z).toBeDefined());
  await userEvent.type(to(), "h");
  await waitFor(() => expect(slow.zh).toBeDefined());
  await act(async () => slow.zh([contacts[0]]));
  await act(async () => slow.z(contacts));
  expect(options()).toEqual(["Zhang, Sanzhang@acme.io"]);
});

test("frozen: nothing is searched", async () => {
  const onSearch = vi.fn(search);
  setup({}, true, onSearch);
  fireEvent.change(to(), { target: { value: "z" } });
  expect(onSearch).not.toHaveBeenCalled();
});

test("Reply-To keeps the one contact picked", async () => {
  const { onChange } = setup({ replyTo: "old@x.io" }, false, search);
  await userEvent.type(screen.getByRole("combobox", { name: "回复地址" }), "zhao");
  await userEvent.click(await screen.findByRole("option"));
  expect(onChange).toHaveBeenLastCalledWith({ replyTo: "Zhao Si <zhao@acme.io>" });
});

test("while an IME composes, nothing is searched until it ends", async () => {
  const onSearch = vi.fn(search);
  setup({}, false, onSearch);
  fireEvent.compositionStart(to());
  fireEvent.change(to(), { target: { value: "zh" } });
  expect(onSearch).not.toHaveBeenCalled();
  fireEvent.compositionEnd(to());
  expect(await screen.findAllByRole("option")).toHaveLength(2);
  expect(onSearch).toHaveBeenLastCalledWith("zh");
});

test("Enter right away commits the first contact", async () => {
  const { onChange } = setup({}, false, search);
  await userEvent.type(to(), "zhao");
  await screen.findByRole("option");
  await userEvent.keyboard("{Enter}");
  expect(onChange).toHaveBeenLastCalledWith({ to: ["Zhao Si <zhao@acme.io>"] });
});

test("Enter before the hits of the text arrive commits the text", async () => {
  const { onChange } = setup({}, false, search);
  await userEvent.type(to(), "z");
  await screen.findAllByRole("option");
  await userEvent.type(to(), "x@y.io{Enter}"); // the hits shown, Zhang active, are still z's
  expect(onChange).toHaveBeenLastCalledWith({ to: ["zx@y.io"] });
});

test("a failed search shows nothing", async () => {
  let fail = false;
  setup({}, false, (q) => (fail ? Promise.reject(new Error("x")) : search(q)));
  await userEvent.type(to(), "z");
  await screen.findAllByRole("option");
  fail = true;
  await userEvent.type(to(), "h");
  await waitFor(() => expect(screen.queryByRole("listbox")).not.toBeInTheDocument());
});

test("hits answered after Escape stay closed until the text changes", async () => {
  const slow: Record<string, (h: ContactHit[]) => void> = {};
  setup({}, false, (q) => new Promise((r) => (slow[q] = r)));
  await userEvent.type(to(), "z");
  await waitFor(() => expect(slow.z).toBeDefined());
  await act(async () => slow.z(contacts));
  await userEvent.type(to(), "h");
  await waitFor(() => expect(slow.zh).toBeDefined());
  await userEvent.keyboard("{Escape}");
  await act(async () => slow.zh([contacts[0]]));
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  await userEvent.type(to(), "a");
  await waitFor(() => expect(slow.zha).toBeDefined());
  await act(async () => slow.zha([contacts[0]]));
  expect(options()).toEqual(["Zhang, Sanzhang@acme.io"]);
});

test("text typed again after Escape completes again", async () => {
  setup({}, false, search);
  await userEvent.type(to(), "zh");
  await screen.findAllByRole("option");
  await userEvent.keyboard("{Escape}");
  await userEvent.clear(to());
  await userEvent.type(to(), "zh");
  expect(await screen.findAllByRole("option")).toHaveLength(2);
});
