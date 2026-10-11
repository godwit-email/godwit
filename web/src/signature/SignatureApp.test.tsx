import { act, render, screen } from "@testing-library/react";

const h = vi.hoisted(() => {
  const subs: Record<string, ((v: never) => void)[]> = {};
  const on = (name: string) => (fn: (v: never) => void) => {
    subs[name] = [...(subs[name] ?? []), fn];
    return () => {};
  };
  return { subs, on };
});

vi.mock("@/mygo", () => ({
  SignaturePage: {
    state: vi.fn(),
    load: vi.fn(async () => "<p>Bob</p>"),
    save: vi.fn(async () => {}),
    saved: vi.fn(async () => {}),
  },
  events: {
    signatureAccount: { on: h.on("account") },
    signatureFlush: { on: h.on("flush") },
    theme: { on: h.on("theme") },
    signatureText: { on: h.on("text") },
  },
}));

import { SignaturePage as api } from "@/mygo";
import { SignatureApp } from "./Signature";

test("a language change relabels the toolbar", async () => {
  const theme = { mode: "light", light: {}, dark: {}, fontSize: 14, bodyFontSize: 14 };
  vi.mocked(api.state).mockResolvedValue({
    account: 1,
    text: { "compose.bold": "加粗" },
    theme,
  } as never);
  render(<SignatureApp />);
  expect(await screen.findByRole("button", { name: "加粗" })).toBeInTheDocument();
  await act(async () => {
    for (const fn of h.subs.text ?? []) fn({ "compose.bold": "Bold" } as never);
  });
  expect(screen.getByRole("button", { name: "Bold" })).toBeInTheDocument();
});
