import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeT } from "@/shared/i18n";
import { Response } from "./Response";

const t = makeT({
  "link.title": "打开这个链接？",
  "link.open": "打开",
  "link.copy": "复制链接",
  "link.copied": "链接已复制",
  "common.cancel": "取消",
});

const md = [
  "![x](https://t/x.png)",
  "![d](data:image/png;base64,AA)",
  "<script>alert(1)</script>",
  '<img src="x" onerror="alert(1)">',
  "<b>bold</b>",
  "[a](javascript:alert(1)) [f](file:///etc) [b](https://ok.org/p) [m](mailto:x@y.z)",
].join("\n\n");

describe("answer layout", () => {
  it("the page size sets the reading leading; tables get the bordered style", async () => {
    const { container } = render(
      <Response text={"| a | b |\n|---|---|\n| 1 | 2 |"} size="page" onLink={() => {}} t={t} />,
    );
    await screen.findByText("1");
    expect(container.firstElementChild).toHaveClass("text-md", "leading-[1.75]");
    // one border, the table's own box: no outer frame around it
    const table = container.querySelector("table");
    expect(table?.parentElement).toHaveClass("border", "rounded-lg");
    expect(container.querySelectorAll(".border")).toHaveLength(1);
    expect(screen.getByText("a")).toHaveClass("bg-sidebar", "font-medium");
  });

  it("[n] opens what cite(n) gives; one it has none for, code and no cite stay text", async () => {
    const user = userEvent.setup();
    const open = vi.fn();
    const cite = vi.fn((n: number) => (n === 1 ? open : undefined));
    const text = "see [1] and [9], `[1]`";
    const { unmount } = render(<Response text={text} onLink={() => {}} t={t} cite={cite} />);
    await user.click(await screen.findByRole("link", { name: "[1]" }));
    expect(open).toHaveBeenCalledOnce();
    expect(screen.getAllByRole("link")).toHaveLength(1); // not [9], not the code's
    expect(screen.getByText("[1]", { selector: "code" })).toBeInTheDocument();
    unmount();
    render(<Response text={text} onLink={() => {}} t={t} />);
    await screen.findByText(/see/);
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("a model-written #cite- link shows as [n], whatever its text", async () => {
    const cite = (n: number) => (n === 1 ? () => {} : undefined);
    render(<Response text="[open your bank](#cite-1)" onLink={() => {}} t={t} cite={cite} />);
    expect(await screen.findByRole("link", { name: "[1]" })).toBeInTheDocument();
    expect(screen.queryByText("open your bank")).toBeNull();
  });
});

describe("answer hardening", () => {
  it("renders no image, script or raw element; only http(s)/mailto links", async () => {
    const { container } = render(<Response text={md} onLink={() => {}} t={t} />);
    await screen.findByText("b");
    expect(container.querySelector("img, script, b, iframe")).toBeNull();
    expect(container.querySelector("[onerror]")).toBeNull();
    expect(container.textContent).toContain("<b>bold</b>"); // shown as text
    expect(container.textContent).toContain("<script>alert(1)</script>");
    expect(container.querySelector("a, [href]")).toBeNull(); // nothing navigates by itself
    expect(screen.getAllByRole("link").map((l) => l.textContent)).toEqual(["b", "m"]);
    expect(screen.getByText("a").closest("[role=link]")).toBeNull();
    expect(screen.getByText("f").closest("[role=link]")).toBeNull();
  });

  it.each([
    ["b", "https://ok.org/p"],
    ["m", "mailto:x@y.z"],
  ])("link %s opens the confirmation; only 打开 calls onLink", async (label, url) => {
    const user = userEvent.setup();
    const onLink = vi.fn();
    render(<Response text={md} onLink={onLink} t={t} />);
    await user.click(await screen.findByText(label));
    expect(await screen.findByText("打开这个链接？")).toBeInTheDocument();
    expect(onLink).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "取消" }));
    await waitFor(() => expect(screen.queryByText("打开这个链接？")).toBeNull());
    expect(onLink).not.toHaveBeenCalled();
    await user.click(screen.getByText(label));
    await user.click(await screen.findByRole("button", { name: "打开" }));
    expect(onLink).toHaveBeenCalledWith(url);
  });

  it("bolds the link's host", async () => {
    const user = userEvent.setup();
    render(<Response text="[b](https://ok.org/p)" onLink={() => {}} t={t} />);
    await user.click(await screen.findByText("b"));
    expect((await screen.findByText("ok.org")).tagName).toBe("STRONG");
  });

  it("opens the confirmation from the keyboard", async () => {
    const user = userEvent.setup();
    render(<Response text="[b](https://ok.org/p)" onLink={() => {}} t={t} />);
    (await screen.findByRole("link", { name: "b" })).focus();
    await user.keyboard("{Enter}");
    expect(await screen.findByText("打开这个链接？")).toBeInTheDocument();
  });
});
