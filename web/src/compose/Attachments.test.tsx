import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { makeT } from "@/shared/i18n";
import { Attachments } from "./Attachments";

const t = makeT({
  "compose.remove": "移除 %s",
  "compose.attach.too_large": "附件超过了 %s 的上限",
});

test("lists, removes and reports oversize", async () => {
  const onRemove = vi.fn();
  render(
    <Attachments
      items={[{ id: 7, filename: "a.pdf", size: 1536 }]}
      tooLarge={["big.zip"]}
      frozen={false}
      onRemove={onRemove}
      t={t}
    />,
  );
  expect(screen.getByText("a.pdf")).toBeTruthy();
  expect(screen.getByText("2 KB")).toBeTruthy();
  expect(screen.getByRole("alert").textContent).toContain("附件超过了 25.0 MB 的上限");
  await userEvent.click(screen.getByRole("button", { name: "移除 a.pdf" }));
  expect(onRemove).toHaveBeenCalledWith(7);
  expect(screen.getByText("big.zip")).toBeTruthy();
});

test("frozen: remove is inert", async () => {
  const onRemove = vi.fn();
  render(
    <Attachments
      items={[{ id: 7, filename: "a.pdf", size: 9 }]}
      tooLarge={[]}
      frozen
      onRemove={onRemove}
      t={t}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: "移除 a.pdf" }));
  expect(onRemove).not.toHaveBeenCalled();
});
