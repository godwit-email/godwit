import { afterEach, describe, expect, it, vi } from "vitest";
import { applyTheme, type Theme } from "./theme";

// A matchMedia stub whose result the test can flip and whose listeners it can count.
function stubMedia(dark: boolean) {
  const listeners = new Set<() => void>();
  const mql = {
    matches: dark,
    addEventListener: (_: string, f: () => void) => listeners.add(f),
    removeEventListener: (_: string, f: () => void) => listeners.delete(f),
  };
  vi.stubGlobal("matchMedia", () => mql);
  return {
    listeners,
    set(v: boolean) {
      mql.matches = v;
      for (const f of listeners) f();
    },
  };
}

const theme = (mode: Theme["mode"]): Theme => ({
  mode,
  light: { "--background": "rgb(255 255 255 / 1)" },
  dark: { "--background": "rgb(10 10 10 / 1)" },
  fontSize: 14,
  bodyFontSize: 14,
});
const bg = (el: HTMLElement) => el.style.getPropertyValue("--background");

afterEach(() => vi.unstubAllGlobals());

describe("applyTheme", () => {
  it("follows the system appearance", () => {
    const m = stubMedia(true);
    const root = document.createElement("div");
    const stop = applyTheme(theme("system"), root);
    expect(bg(root)).toBe("rgb(10 10 10 / 1)");
    expect(root.classList.contains("dark")).toBe(true);
    expect(root.style.fontSize).toBe("14px");
    m.set(false);
    expect(bg(root)).toBe("rgb(255 255 255 / 1)");
    expect(root.classList.contains("dark")).toBe(false);
    stop();
    expect(m.listeners.size).toBe(0);
  });

  it("ignores the media query for an explicit mode", () => {
    const m = stubMedia(true);
    const root = document.createElement("div");
    applyTheme(theme("light"), root);
    expect(bg(root)).toBe("rgb(255 255 255 / 1)");
    expect(root.classList.contains("dark")).toBe(false);
    expect(m.listeners.size).toBe(0);
  });

  it("leaves no variable of the other appearance behind", () => {
    const m = stubMedia(false);
    const root = document.createElement("div");
    applyTheme(
      {
        mode: "system",
        light: { "--a": "1" },
        dark: { "--b": "2" },
        fontSize: 14,
        bodyFontSize: 14,
      },
      root,
    );
    expect([root.style.getPropertyValue("--a"), root.style.getPropertyValue("--b")]).toEqual([
      "1",
      "",
    ]);
    m.set(true);
    expect([root.style.getPropertyValue("--a"), root.style.getPropertyValue("--b")]).toEqual([
      "",
      "2",
    ]);
    m.set(false);
    expect([root.style.getPropertyValue("--a"), root.style.getPropertyValue("--b")]).toEqual([
      "1",
      "",
    ]);
  });
});
