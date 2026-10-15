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
  },
}));

import { SignaturePage as api } from "@/mygo";
import { SignatureApp } from "./Signature";

afterEach(() => vi.unstubAllGlobals());

test("a language change relabels the toolbar without reloading the signature", async () => {
  const theme = { mode: "light", light: {}, dark: {}, fontSize: 14, bodyFontSize: 14 };
  vi.mocked(api.state).mockResolvedValue({
    account: 1,
    locale: "zh-CN",
    theme,
  } as never);
  vi.stubGlobal("mygo", {
    locale: "zh-CN",
    on: (name: string, fn: (v: never) => void) => h.on(name)(fn),
  });
  render(<SignatureApp />);
  expect(await screen.findByRole("button", { name: "加粗" })).toBeInTheDocument();
  await act(async () => {
    for (const fn of h.subs["mygo:locale"] ?? []) fn({ locale: "en", dir: "ltr" } as never);
  });
  expect(screen.getByRole("button", { name: "Bold" })).toBeInTheDocument();
  expect(api.load).toHaveBeenCalledOnce();
  expect(screen.getByText("Bob")).toBeInTheDocument();
});
