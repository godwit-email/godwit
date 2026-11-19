import { act, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { Toaster, toast } from "./toast";

afterEach(() => act(() => toast.dismiss()));

test("a toast with an action stays longer, and a persistent one has a close button", async () => {
  render(<Toaster closeLabel="Close toast" />);
  act(() => {
    toast("Saved", { action: { label: "Undo", onClick: vi.fn() } });
    toast.error("Failed", { duration: Infinity });
  });
  expect(await screen.findByText("Saved")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Undo" })).toBeInTheDocument();
  expect(screen.getAllByRole("button", { name: "Close toast" })).toHaveLength(1);
});

test("at most three toasts show at once", async () => {
  render(<Toaster closeLabel="Close toast" />);
  act(() => {
    for (const n of [1, 2, 3, 4]) toast(`Toast ${n}`);
  });
  await screen.findByText("Toast 4");
  expect(document.querySelectorAll("[data-sonner-toast][data-visible='true']")).toHaveLength(3);
});
