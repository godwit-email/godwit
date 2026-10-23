import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { makeT } from "@/shared/i18n";
import { avatarTone, MailCard, senderName } from "./MailCard";

const t = makeT("zh-Hans");

describe("MailCard", () => {
  it("names the sender as the reader does: the display name, else the address", () => {
    expect(senderName("Lena Fischer <lena@x.org>")).toBe("Lena Fischer");
    expect(senderName('"Fischer, Lena" <lena@x.org>')).toBe("Fischer, Lena");
    expect(senderName("<lena@x.org>")).toBe("lena@x.org");
    expect(senderName("lena@x.org")).toBe("lena@x.org");
  });

  it("gives a sender the avatar colour internal/kit Avatar gives them", () => {
    // fnv-1a of the name over the label colours but gray, as Go computes it
    expect(avatarTone("Lena Fischer")).toContain("bg-sky-100");
    expect(avatarTone("Bob")).toContain("bg-lime-100");
    expect(avatarTone("Acme Cloud")).toContain("bg-lime-100");
    expect(avatarTone("陈小明")).toContain("bg-amber-100");
  });

  it("shows the mail and its actions", async () => {
    const user = userEvent.setup();
    const onOpen = vi.fn();
    const onQuote = vi.fn();
    render(
      <MailCard
        from="Lena Fischer <lena@x.org>"
        date={new Date(new Date().getFullYear(), 9, 21).getTime()}
        subject="Re: Q4 partnership proposal"
        snippet="Would the second week of November work?"
        onOpen={onOpen}
        onQuote={onQuote}
        t={t}
      />,
    );
    expect(screen.getByText("L")).toHaveClass("bg-sky-100");
    expect(screen.getByText("Lena Fischer")).toBeInTheDocument();
    expect(screen.getByText("10/21")).toBeInTheDocument();
    expect(screen.getByText("Re: Q4 partnership proposal")).toBeInTheDocument();
    expect(screen.getByText("Would the second week of November work?")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "查看邮件" }));
    expect(onOpen).toHaveBeenCalledOnce();
    await user.click(screen.getByRole("button", { name: "引用邮件" }));
    expect(onQuote).toHaveBeenCalledOnce();
  });

  it("offers only the actions it has", () => {
    render(<MailCard from="Bob" date={0} subject="Hi" snippet="" t={t} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.queryByRole("toolbar")).toBeNull();
  });

  it("as a card in an answer: framed, its actions on a toolbar that shows on hover or focus", () => {
    render(
      <MailCard
        card
        from="Bob"
        date={0}
        subject="Hi"
        snippet="s"
        onOpen={() => {}}
        onQuote={() => {}}
        t={t}
      />,
    );
    const bar = screen.getByRole("toolbar", { name: "Hi" });
    expect(bar).toHaveClass(
      "opacity-0",
      "group-hover:opacity-100",
      "group-focus-within:opacity-100",
    );
    expect(bar.parentElement).toHaveClass("group", "border-[0.5px]");
  });
});
