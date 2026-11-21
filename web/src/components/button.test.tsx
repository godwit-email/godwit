import { render, screen } from "@testing-library/react";
import { Button } from "@/components/button";

function classes(props: Parameters<typeof Button>[0] = {}) {
  render(<Button {...props}>go</Button>);
  const list = screen.getByRole("button").classList;
  return (token: string) => list.contains(token);
}

test("outline danger: its border and text on Background, no shadow, its fill on hover", () => {
  const has = classes({ variant: "outline", tone: "danger" });
  expect(has("bg-background")).toBe(true);
  expect(has("border-(color:--danger-border)")).toBe(true);
  expect(has("text-destructive")).toBe(true);
  expect(has("hover:bg-(color:--danger-bg)")).toBe(true);
  expect(has("shadow-xs")).toBe(false);
  expect(has("border-transparent")).toBe(false);
});

test.each(["warning", "info", "success"] as const)(
  "outline %s: its border, text and fill, pressed halfway to the border",
  (tone) => {
    const has = classes({ variant: "outline", tone });
    expect(has(`border-(color:--${tone}-border)`)).toBe(true);
    expect(has(`text-(color:--${tone}-text)`)).toBe(true);
    expect(has(`hover:bg-(color:--${tone}-bg)`)).toBe(true);
    expect(
      has(`active:bg-[color:color-mix(in_srgb,var(--${tone}-bg),var(--${tone}-border))]`),
    ).toBe(true);
    expect(has("shadow-xs")).toBe(false);
  },
);

test("outline neutral keeps the input border and the shadow", () => {
  const has = classes({ variant: "outline" });
  expect(has("border-input")).toBe(true);
  expect(has("shadow-xs")).toBe(true);
  expect(has("border-transparent")).toBe(false);
  expect(has("disabled:opacity-100")).toBe(true);
});

test("ghost danger has danger text and fill", () => {
  const has = classes({ variant: "ghost", tone: "danger" });
  expect(has("text-destructive")).toBe(true);
  expect(has("hover:bg-(color:--danger-bg)")).toBe(true);
  expect(has("text-foreground")).toBe(false);
});

test("a lone ghost icon rests muted and turns foreground on hover", () => {
  const has = classes({ variant: "ghost", size: "icon-sm" });
  expect(has("text-muted-foreground")).toBe(true);
  expect(has("hover:text-foreground")).toBe(true);
  expect(has("text-foreground")).toBe(false);
});

test("a tone the look does not take is neutral", () => {
  const ghost = classes({ variant: "ghost", tone: "warning" });
  expect(ghost("hover:bg-(color:--hover-tint)")).toBe(true);
  expect(ghost("text-(color:--warning-text)")).toBe(false);
});

test("link is inline text: no height, padding, radius or fill, underlined, muted on hover", () => {
  const has = classes({ variant: "link", size: "sm" });
  for (const token of [
    "h-auto",
    "p-0",
    "rounded-none",
    "font-normal",
    "underline",
    "text-foreground",
  ]) {
    expect(has(token)).toBe(true);
  }
  expect(has("hover:text-muted-foreground")).toBe(true);
  expect(has("active:text-muted-foreground")).toBe(true);
  expect(has("disabled:text-(color:--idle)")).toBe(true);
  for (const token of ["h-7", "px-2.5", "font-medium", "bg-primary"])
    expect(has(token)).toBe(false);
});

test("primary is filled, mixed toward Background on hover, and half opacity disabled", () => {
  const has = classes();
  expect(has("bg-primary")).toBe(true);
  expect(has("hover:bg-[color:color-mix(in_srgb,var(--primary),var(--background)_10%)]")).toBe(
    true,
  );
  expect(has("active:bg-[color:color-mix(in_srgb,var(--primary),var(--background)_20%)]")).toBe(
    true,
  );
  expect(has("disabled:opacity-50")).toBe(true);
  expect(has("disabled:opacity-100")).toBe(false);
});

test("sizes are the control scale: md is the default 32, xs 24, lg 36", () => {
  expect(classes()("h-8")).toBe(true);
});
