import { act, fireEvent, render, screen } from "@testing-library/react";

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
    editHTML: vi.fn(async () => {}),
    save: vi.fn(async () => {}),
    saved: vi.fn(async () => {}),
  },
  events: {
    signatureShow: { on: h.on("show") },
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
    signature: 1,
    locale: "zh-Hans",
    theme,
  } as never);
  vi.stubGlobal("mygo", {
    locale: "zh-Hans",
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

test("the HTML toggle saves the edit, then asks native for the HTML mode", async () => {
  vi.mocked(api.state).mockResolvedValue({ signature: 1, locale: "en", theme: undefined } as never);
  vi.stubGlobal("mygo", { locale: "en", on: () => () => {} });
  render(<SignatureApp />);
  fireEvent.click(await screen.findByRole("button", { name: "HTML code" }));
  await vi.waitFor(() => expect(api.editHTML).toHaveBeenCalledOnce());
  expect(screen.getByText("Bob")).toBeInTheDocument();
});
