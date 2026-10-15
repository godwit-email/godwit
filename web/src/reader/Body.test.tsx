import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReaderBody, Theme } from "@/mygo";

const h = vi.hoisted(() => {
  const on =
    <T,>(set: Set<(x: T) => void>) =>
    (f: (x: T) => void) => {
      set.add(f);
      return () => set.delete(f);
    };
  return {
    on,
    body: new Set<(b: ReaderBody) => void>(),
    theme: new Set<(t: Theme) => void>(),
  };
});
vi.mock("@/mygo", () => ({
  ReaderPage: {
    load: vi.fn(),
    height: vi.fn(async () => {}),
    wheel: vi.fn(async () => {}),
    openLink: vi.fn(async () => {}),
    navigate: vi.fn(async () => {}),
    search: vi.fn(async () => {}),
  },
  events: { readerBody: { on: h.on(h.body) }, theme: { on: h.on(h.theme) } },
}));

import { ReaderPage as api } from "@/mygo";
import { ReaderApp } from "./Body";

const theme: Theme = {
  mode: "light",
  light: { "--foreground": "#111" },
  dark: { "--foreground": "#eee" },
  fontSize: 14,
  bodyFontSize: 14,
};
const body = (version: number, html: string, over: Partial<ReaderBody> = {}): ReaderBody => ({
  message: 9,
  version,
  html,
  plain: "",
  remote: false,
  blocked: 0,
  cut: false,
  theme,
  locale: "en",
  ...over,
});

let frames: FrameRequestCallback[] = [];
let observers: { cb: ResizeObserverCallback; targets: Element[] }[] = [];
const flush = () =>
  act(() => {
    const fs = frames;
    frames = [];
    for (const f of fs) f(0);
  });

beforeEach(() => {
  vi.clearAllMocks();
  frames = [];
  observers = [];
  vi.stubGlobal("requestAnimationFrame", (f: FrameRequestCallback) => frames.push(f));
  vi.stubGlobal(
    "ResizeObserver",
    class {
      o: { cb: ResizeObserverCallback; targets: Element[] };
      constructor(cb: ResizeObserverCallback) {
        this.o = { cb, targets: [] };
        observers.push(this.o);
      }
      observe(t: Element) {
        this.o.targets.push(t);
      }
      disconnect() {
        this.o.targets = [];
      }
    },
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/** Loads the iframe's srcdoc into its document (jsdom doesn't) and fires load. */
async function loaded() {
  const frame = await waitFor(() => {
    const f = document.querySelector("iframe");
    if (!f?.getAttribute("srcdoc")) throw new Error("no frame yet");
    // jsdom's own about:blank load replaces the document: write ours after it.
    if (f.contentDocument?.readyState !== "complete") throw new Error("frame loading");
    return f;
  });
  // biome-ignore lint/style/noNonNullAssertion: an attached iframe has a document
  const doc = frame.contentDocument!;
  const parsed = new DOMParser().parseFromString(frame.getAttribute("srcdoc") ?? "", "text/html");
  doc.replaceChild(doc.importNode(parsed.documentElement, true), doc.documentElement);
  act(() => {
    frame.dispatchEvent(new Event("load"));
  });
  // biome-ignore lint/style/noNonNullAssertion: likewise its window
  return { frame, doc, win: frame.contentWindow! as Window & typeof globalThis };
}

async function start(b: ReaderBody) {
  vi.mocked(api.load).mockResolvedValue(b);
  render(<ReaderApp />);
  return loaded();
}

test("the iframe is sandboxed (scripts allowed for WebKit's listeners; the CSP forbids them), borderless and unscrolled", async () => {
  const { frame } = await start(body(1, "<p>x</p>"));
  expect(frame.getAttribute("sandbox")).toBe("allow-same-origin allow-scripts");
  expect(frame.getAttribute("scrolling")).toBe("no");
  expect(frame.style.border).toMatch(/^0/);
  expect(frame.style.width).toBe("100%");
  expect(frame.style.display).toBe("block");
  expect(frame.getAttribute("title")).toBe("Message body");
});

test("height doesn't run away with viewport-sized mail (vh): it converges, bounded calls", async () => {
  const { frame, doc } = await start(
    body(1, '<div style="min-height:100vh">x <img src="data:image/gif;base64,R0" alt="i"></div>'),
  );
  const wrap = doc.getElementById("godwit-body") as HTMLElement;
  const frameH = () => Number.parseFloat(frame.style.height) || 150; // 150: an iframe's default
  frame.getBoundingClientRect = () => ({ height: frameH() }) as DOMRect;
  let base = 40;
  wrap.getBoundingClientRect = () => ({ height: base + frameH() }) as DOMRect; // grows with the frame
  const ro = observers.find((o) => o.targets.includes(wrap));
  const settle = () => {
    for (let i = 0; i < 30; i++) {
      ro?.cb([], {} as ResizeObserver); // any layout change re-fires the observer
      flush();
    }
  };
  settle();
  expect(api.height).toHaveBeenCalledTimes(1);
  const stable = frame.style.height;
  expect(stable).toBe("190px");

  // A real change (an image loads) is followed, then holds again.
  base = 100;
  wrap.querySelector("img")?.dispatchEvent(new Event("load"));
  settle();
  expect(api.height).toHaveBeenCalledTimes(2);
  expect(api.height).toHaveBeenLastCalledWith(290);
  expect(frame.style.height).toBe("290px");
});

test("height is clamped to Go's maximum and stops growing there", async () => {
  const { frame, doc } = await start(body(1, "<p>x</p>"));
  const wrap = doc.getElementById("godwit-body") as HTMLElement;
  wrap.getBoundingClientRect = () => ({ height: 5_000_000 }) as DOMRect;
  flush();
  expect(api.height).toHaveBeenCalledExactlyOnceWith(1 << 20);
  expect(frame.style.height).toBe(`${1 << 20}px`);
  wrap.getBoundingClientRect = () => ({ height: 6_000_000 }) as DOMRect;
  window.dispatchEvent(new Event("resize"));
  flush();
  expect(api.height).toHaveBeenCalledTimes(1);
});

test("a failed first load uses the runtime locale before any body arrives", async () => {
  vi.stubGlobal("mygo", { locale: "zh-Hans", on: () => () => {} });
  vi.mocked(api.load).mockRejectedValueOnce(new Error("boom"));
  render(<ReaderApp />);
  const retry = await screen.findByRole("button", { name: "重试" });
  expect(screen.getByText("出错了：boom")).toBeInTheDocument();
  vi.mocked(api.load).mockResolvedValueOnce(body(1, "<p>x</p>"));
  act(() => retry.click());
  await waitFor(() => expect(document.querySelector("iframe")).not.toBeNull());
});

test("stale load ignored: readerBody v2, then load's v1 resolves → v2 stays", async () => {
  let resolve!: (b: ReaderBody) => void;
  vi.mocked(api.load).mockReturnValue(new Promise((r) => (resolve = r)));
  render(<ReaderApp />);
  expect(h.body.size).toBe(1); // listeners first
  act(() => {
    for (const f of h.body) f(body(2, "<p>two</p>"));
  });
  await act(async () => resolve(body(1, "<p>one</p>")));
  const { doc } = await loaded();
  expect(doc.getElementById("godwit-body")?.textContent).toBe("two");
});

test("height follows content: one call per frame, large to small and back", async () => {
  const { frame, doc } = await start(body(1, "<p>x</p>"));
  const wrap = doc.getElementById("godwit-body") as HTMLElement;
  let hgt = 800;
  wrap.getBoundingClientRect = () => ({ height: hgt }) as DOMRect;
  const ro = observers.find((o) => o.targets.includes(wrap));
  expect(ro).toBeDefined();
  const resize = () => ro?.cb([], {} as ResizeObserver);
  flush();
  expect(api.height).toHaveBeenLastCalledWith(800);
  expect(frame.style.height).toBe("800px");
  vi.mocked(api.height).mockClear();

  hgt = 120;
  resize();
  resize();
  resize();
  expect(frames).toHaveLength(1);
  flush();
  expect(api.height).toHaveBeenCalledExactlyOnceWith(120);
  expect(frame.style.height).toBe("120px");

  hgt = 2000.4;
  resize();
  flush();
  expect(api.height).toHaveBeenLastCalledWith(2001);

  resize();
  flush(); // unchanged → no call
  expect(api.height).toHaveBeenCalledTimes(2);
});

test("an image's load re-measures", async () => {
  const { doc } = await start(body(1, '<img src="data:image/gif;base64,R0lGOD" alt="i">'));
  const wrap = doc.getElementById("godwit-body") as HTMLElement;
  wrap.getBoundingClientRect = () => ({ height: 50 }) as DOMRect;
  flush();
  wrap.getBoundingClientRect = () => ({ height: 90 }) as DOMRect;
  doc.querySelector("img")?.dispatchEvent(new Event("load"));
  flush();
  expect(api.height).toHaveBeenLastCalledWith(90);
});

test("link click opens via Go; other navigation is prevented", async () => {
  const { doc, win } = await start(
    body(
      1,
      '<p><span data-href="https://a.example/x"><b>go</b></span> <a href="https://b.example">b</a></p>',
    ),
  );
  const link = doc.querySelector("[data-href]") as HTMLElement;
  expect(link.getAttribute("tabindex")).toBe("0");
  expect(link.getAttribute("role")).toBe("link");
  const click = new win.MouseEvent("click", { bubbles: true, cancelable: true });
  doc.querySelector("b")?.dispatchEvent(click);
  expect(click.defaultPrevented).toBe(true);
  expect(api.openLink).toHaveBeenCalledExactlyOnceWith("https://a.example/x");

  const other = new win.MouseEvent("click", { bubbles: true, cancelable: true });
  doc.querySelector("a")?.dispatchEvent(other);
  expect(other.defaultPrevented).toBe(true);
  expect(api.openLink).toHaveBeenCalledTimes(1);
});

test("keyboard link: Enter on a focused [data-href] → openLink", async () => {
  const { doc, win } = await start(body(1, '<span data-href="mailto:a@b.c">mail</span>'));
  const link = doc.querySelector("[data-href]") as HTMLElement;
  link.focus();
  const enter = new win.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
  link.dispatchEvent(enter);
  expect(enter.defaultPrevented).toBe(true);
  expect(api.openLink).toHaveBeenCalledExactlyOnceWith("mailto:a@b.c");
});

test("shortcuts from iframe: ⌘[ inside the iframe → navigate(-1)", async () => {
  const { doc, win } = await start(body(1, "<p>x</p>"));
  doc.body.dispatchEvent(
    new win.KeyboardEvent("keydown", { key: "[", metaKey: true, bubbles: true, cancelable: true }),
  );
  expect(api.navigate).toHaveBeenCalledExactlyOnceWith(-1);
  window.dispatchEvent(new KeyboardEvent("keydown", { key: "]", metaKey: true, cancelable: true }));
  expect(api.navigate).toHaveBeenLastCalledWith(1);
});

test("area[data-href] in the iframe → openLink", async () => {
  const { doc } = await start(
    body(1, '<map name="m"><area data-href="https://a.example/m" alt="m"></map>'),
  );
  (doc.querySelector("area") as HTMLElement).click();
  expect(api.openLink).toHaveBeenCalledExactlyOnceWith("https://a.example/m");
});

test("⌘F in the iframe or the page → search()", async () => {
  const { doc, win } = await start(body(1, "<p>x</p>"));
  const e = new win.KeyboardEvent("keydown", {
    key: "f",
    metaKey: true,
    bubbles: true,
    cancelable: true,
  });
  doc.body.dispatchEvent(e);
  expect(e.defaultPrevented).toBe(true);
  expect(api.search).toHaveBeenCalledOnce();
  window.dispatchEvent(new KeyboardEvent("keydown", { key: "f", metaKey: true, cancelable: true }));
  expect(api.search).toHaveBeenCalledTimes(2);
});

test("wheel in the iframe goes to Go once per frame", async () => {
  const { win } = await start(body(1, "<p>x</p>"));
  flush();
  const evs = [10, 20, 30].map(
    (dy) => new win.WheelEvent("wheel", { deltaY: dy, cancelable: true }),
  );
  for (const e of evs) win.dispatchEvent(e);
  expect(evs.every((e) => e.defaultPrevented)).toBe(true);
  flush();
  expect(api.wheel).toHaveBeenCalledExactlyOnceWith(60);
});

test("remote images are godwit-img: URLs left to the frame: the page asks Go for nothing", async () => {
  const html =
    '<img src="godwit-img://m1/abc" alt="a"><div style="background-image:url(godwit-img://m1/def)">x</div>';
  const { doc } = await start(body(1, html, { remote: true }));
  expect(doc.querySelector("img")?.getAttribute("src")).toBe("godwit-img://m1/abc");
  expect(doc.querySelector("[style]")?.getAttribute("style")).toContain("godwit-img://m1/def");
  expect(Object.keys(api)).not.toContain("image");
});

test("a theme event rebuilds plain text in the new theme", async () => {
  await start(body(1, "", { plain: "hi" }));
  act(() => {
    for (const f of h.theme) f({ ...theme, mode: "dark", bodyFontSize: 18 });
  });
  const { doc } = await loaded();
  const css = doc.querySelector("style")?.textContent ?? "";
  expect(css).toContain("#eee");
  expect(css).toContain("18px");
});
