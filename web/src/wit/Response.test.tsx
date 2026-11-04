import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeT } from "@/shared/i18n";
import { citedNumbers, plainCites, Response } from "./Response";

const t = makeT("zh-Hans");

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
    expect(table?.parentElement).toHaveClass("border-[0.5px]", "rounded-lg");
    expect(container.querySelectorAll(".border-\\[0\\.5px\\]")).toHaveLength(1);
    expect(screen.getByText("a")).toHaveClass("bg-sidebar", "font-medium");
  });

  it("<mail id/> cites a mail; beside it a plain [n] is text, not a citation", async () => {
    const cite = vi.fn((n: number) => <span>mail {n}</span>);
    render(<Response text={'paid <mail id="1"/>, see [2]'} onLink={() => {}} t={t} cite={cite} />);
    expect(await screen.findByRole("button", { name: "[1]" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "[2]" })).toBeNull();
    expect(screen.getByText(/see \[2\]/)).toBeInTheDocument();
    expect(screen.queryByText(/<mail/)).toBeNull();
  });

  it("<mail id card/> lines show cards, each in its place", async () => {
    const card = (n: number) => <span>card {n}</span>;
    render(
      <Response
        text={'Two mails:\n<mail id="1" card/>\n<mail id="2" card/>\nThat is all.'}
        onLink={() => {}}
        t={t}
        card={card}
      />,
    );
    expect(await screen.findByText("card 1")).toBeInTheDocument();
    expect(screen.getByText("card 2")).toBeInTheDocument();
    expect(screen.getByText("That is all.")).toBeInTheDocument();
  });

  it("while it streams, a tag not yet whole shows nothing", async () => {
    const { container } = render(
      <Response text={'paid <mail id="1'} streaming onLink={() => {}} t={t} />,
    );
    await screen.findByText(/paid/);
    expect(container.textContent).not.toContain("<mail");
  });

  it("the mail an answer cites: its tags; before tags, its [n] and [[n]]", () => {
    expect(citedNumbers('a <mail id="2"/> b <mail id="5" card/> [7]')).toEqual(new Set([2, 5]));
    expect(citedNumbers("a [1] b\n[[3]]")).toEqual(new Set([1, 3]));
    expect(citedNumbers("none")).toEqual(new Set());
  });

  it("copying an answer gives its tags as [n]", () => {
    expect(plainCites('paid <mail id="1"/> and\n<mail id="2" card/>')).toBe("paid [1] and\n[2]");
  });

  it("hovering [n] shows its mail; the marker shows n alone", async () => {
    const user = userEvent.setup();
    const cite = (n: number) => (n === 1 ? <span>the cited mail</span> : undefined);
    render(<Response text="see [1]" onLink={() => {}} t={t} cite={cite} />);
    const marker = await screen.findByRole("button", { name: "[1]" });
    expect(marker).toHaveTextContent(/^1$/);
    await user.hover(marker);
    expect(await screen.findByText("the cited mail")).toBeInTheDocument();
  });

  it("headings stay a step above the text at most", async () => {
    render(<Response text={"# One\n\n## Two\n\n### Three\n\nbody"} onLink={() => {}} t={t} />);
    expect(await screen.findByRole("heading", { level: 1 })).toHaveClass(
      "text-lg",
      "font-semibold",
    );
    expect(screen.getByRole("heading", { level: 2 })).toHaveClass("text-lg", "font-semibold");
    expect(screen.getByRole("heading", { level: 3 })).not.toHaveClass("text-lg");
  });

  it("[n] opens a popover with what cite(n) gives; one it has none for, code and no cite stay text", async () => {
    const user = userEvent.setup();
    const cite = vi.fn((n: number) => (n === 1 ? <span>the cited mail</span> : undefined));
    const text = "see [1] and [9], `[1]`";
    const { unmount } = render(<Response text={text} onLink={() => {}} t={t} cite={cite} />);
    expect(screen.queryByText("the cited mail")).toBeNull();
    await user.click(await screen.findByRole("button", { name: "[1]" }));
    expect(await screen.findByText("the cited mail")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /^\[\d\]$/ })).toHaveLength(1); // not [9], not the code's
    expect(screen.getByText("[1]", { selector: "code" })).toBeInTheDocument();
    unmount();
    render(<Response text={text} onLink={() => {}} t={t} />);
    await screen.findByText(/see/);
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("a model-written #cite- link shows as [n], whatever its text", async () => {
    const cite = (n: number) => (n === 1 ? <span>mail</span> : undefined);
    render(<Response text="[open your bank](#cite-1)" onLink={() => {}} t={t} cite={cite} />);
    expect(await screen.findByRole("button", { name: "[1]" })).toBeInTheDocument();
    expect(screen.queryByText("open your bank")).toBeNull();
  });
});

describe("mail cards", () => {
  it("[[n]] alone on a line shows what card(n) gives; otherwise it stays text", async () => {
    const card = vi.fn((n: number) => (n === 1 ? <div>the card</div> : undefined));
    const text = "Waiting on you:\n\n[[1]]\n\n[[9]]\n\nsee [[1]] inline";
    const { container } = render(
      <Response text={text} onLink={() => {}} t={t} cite={() => <span />} card={card} />,
    );
    expect(await screen.findByText("the card")).toBeInTheDocument();
    expect(screen.getAllByText("the card")).toHaveLength(1);
    expect(screen.getByText("[[9]]")).toBeInTheDocument(); // a ref it has no card for
    expect(container.querySelector("p div")).toBeNull(); // the card is a block, not in a <p>
    expect(screen.getByText(/inline/)).toBeInTheDocument();
  });

  it("without card, [[n]] stays text", async () => {
    render(<Response text="[[1]]" onLink={() => {}} t={t} />);
    expect(await screen.findByText("[[1]]")).toBeInTheDocument();
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

  it("a bare URL ends at CJK punctuation", async () => {
    const user = userEvent.setup();
    const onLink = vi.fn();
    const { container } = render(
      <Response
        text="链接：https://vishnuai.in/mediakit，并留下电话。见 www.ok.org”"
        onLink={onLink}
        confirm={false}
        t={t}
      />,
    );
    const links = await screen.findAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual(["https://vishnuai.in/mediakit", "www.ok.org"]);
    expect(container.textContent).toContain("，并留下电话。见 www.ok.org”");
    await user.click(links[0]);
    await user.click(links[1]);
    expect(onLink.mock.calls).toEqual([["https://vishnuai.in/mediakit"], ["http://www.ok.org/"]]);
  });

  it("a bare URL goes on through letters beyond ASCII and dashes", async () => {
    render(
      <Response text="https://a.in/ｶﾀｶﾅ と https://a.in/1990–91（续）" onLink={() => {}} t={t} />,
    );
    const links = await screen.findAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual(["https://a.in/ｶﾀｶﾅ", "https://a.in/1990–91"]);
  });

  it("a written link keeps its URL, CJK punctuation and all", async () => {
    const user = userEvent.setup();
    const onLink = vi.fn();
    render(
      <Response
        text="[https://a.org/文档，下载](https://a.org/文档，下载) 和 <https://b.org/x，y>"
        onLink={onLink}
        confirm={false}
        t={t}
      />,
    );
    const links = await screen.findAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual([
      "https://a.org/文档，下载",
      "https://b.org/x，y",
    ]);
    await user.click(links[0]);
    await user.click(links[1]);
    expect(onLink.mock.calls).toEqual([
      [new URL("https://a.org/文档，下载").href],
      [new URL("https://b.org/x，y").href],
    ]);
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
