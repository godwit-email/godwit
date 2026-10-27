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
import { readerColored } from "@/preview/fixtures";
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

test("a retry's body, the same HTML in a new version, loads the frame anew", async () => {
  await start(body(1, '<img src="godwit-img://m1/abc">', { remote: true }));
  const srcdoc = () => document.querySelector("iframe")?.getAttribute("srcdoc");
  const before = srcdoc();
  expect(before).toBeTruthy();
  act(() => {
    for (const f of h.body) f(body(2, '<img src="godwit-img://m1/abc">', { remote: true }));
  });
  expect(srcdoc()).not.toBe(before);
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

const darkTheme: Theme = {
  ...theme,
  mode: "dark",
  light: { "--background": "#fff", "--foreground": "#111" },
  dark: {
    "--background": "#0a0a0a",
    "--foreground": "#fafafa",
    "--muted-foreground": "#a3a3a3",
    "--muted": "#262626",
  },
};

test("dark neutral mail keeps readable text and distinct card and secondary text colours", async () => {
  const html = `<div id="paper" style="background:#fff;color:#111">
    <p id="primary">Order received</p>
    <p id="secondary" style="color:#888">Delivery details</p>
    <div id="card" style="background:#eee">Your order</div>
  </div>`;
  const input = body(1, html, { theme: darkTheme });
  const { doc, win } = await start(input);
  const style = (id: string) => win.getComputedStyle(doc.getElementById(id) as HTMLElement);
  const red = (value: string) => Number(value.match(/[\d.]+/)?.[0]);
  expect(red(style("paper").backgroundColor)).toBeLessThan(50);
  expect(red(style("primary").color)).toBeGreaterThan(200);
  expect(red(style("secondary").color)).toBeGreaterThan(140);
  expect(red(style("secondary").color)).toBeLessThan(red(style("primary").color));
  expect(style("card").backgroundColor).not.toBe(style("paper").backgroundColor);
  expect(style("paper").filter).not.toContain("invert");
  expect(input.html).toBe(html);
});

test("resizing adapted mail reads the new light baseline instead of its previous overrides", async () => {
  const { frame, doc, win } = await start(
    body(
      1,
      `<style id="responsive">
    #godwit-body .panel { background:#fff; color:#111 }
    </style><div class="panel">Responsive content</div>`,
      { theme: darkTheme },
    ),
  );
  const panel = doc.querySelector(".panel") as HTMLElement;
  expect(win.getComputedStyle(panel).backgroundColor).not.toBe("rgb(255, 255, 255)");
  // jsdom has no media-query layout: supply the rule a narrower viewport would activate.
  const sheet = doc.getElementById("responsive") as HTMLStyleElement;
  sheet.textContent = "#godwit-body .panel { background:#222; color:#ddd }";
  frame.getBoundingClientRect = () => ({ width: 400, height: 100 }) as DOMRect;
  act(() => window.dispatchEvent(new Event("resize")));
  flush();
  expect(win.getComputedStyle(panel).backgroundColor).toBe("rgb(34, 34, 34)");
  expect(win.getComputedStyle(panel).color).toBe("rgb(221, 221, 221)");
});

test("translucent groups keep their original outside backdrop without revealing hidden text", async () => {
  const { doc, win } = await start(
    body(
      1,
      `
    <div id="group" style="background:#fff;color:#111">
      <p style="opacity:.2"><span id="dim">Terms</span></p>
      <span id="hidden" style="color:rgba(0,0,0,0)">Hidden</span>
    </div>
      <div style="display:none;opacity:.2">Hidden preheader</div>
    <p id="outside" style="background:#fff;color:#111">Readable elsewhere</p>`,
      { theme: darkTheme },
    ),
  );
  const style = (id: string) => win.getComputedStyle(doc.getElementById(id) as HTMLElement);
  expect(style("group").backgroundColor).toBe("rgb(255, 255, 255)");
  expect(style("group").color).toBe("rgb(17, 17, 17)");
  expect(style("hidden").color).toBe("rgba(0, 0, 0, 0)");
  expect(style("outside").backgroundColor).not.toBe("rgb(255, 255, 255)");
});

test("natural dark surfaces keep their colour while light cards adapt; author dark CSS takes precedence", async () => {
  const content = `<div id="canvas" style="background:#222;color:#ddd">
    <p>Dark surface</p><div id="card" style="background:#fff;color:#111">Light card</div></div>`;
  let current = await start(
    body(1, `<style data-dark-canvas=""></style>${content}`, { theme: darkTheme }),
  );
  expect(
    current.win.getComputedStyle(current.doc.getElementById("canvas") as HTMLElement)
      .backgroundColor,
  ).toBe("rgb(34, 34, 34)");
  expect(
    current.win.getComputedStyle(current.doc.getElementById("card") as HTMLElement).backgroundColor,
  ).not.toBe("rgb(255, 255, 255)");
  act(() => {
    for (const receive of h.body)
      receive(
        body(
          2,
          `<style data-dark="">#godwit-body.godwit-dark #canvas{background:#222}</style>${content}`,
          { theme: darkTheme },
        ),
      );
  });
  current = await loaded();
  expect(
    current.win.getComputedStyle(current.doc.getElementById("card") as HTMLElement).backgroundColor,
  ).toBe("rgb(255, 255, 255)");
});

test("a theme round trip restores original light declarations and a newer document wins over queued work", async () => {
  const html = '<div id="mail" style="background:#fff;color:#111!important">First</div>';
  let current = await start(body(1, html, { theme: darkTheme }));
  const original = new DOMParser()
    .parseFromString(html, "text/html")
    .getElementById("mail")
    ?.getAttribute("style");
  act(() => {
    for (const receive of h.theme) receive({ ...darkTheme, mode: "light" });
  });
  current = await loaded();
  expect(current.doc.getElementById("mail")?.getAttribute("style")).toBe(original);
  act(() => {
    for (const receive of h.theme) receive(darkTheme);
  });
  current = await loaded();
  const firstDark = current.win.getComputedStyle(
    current.doc.getElementById("mail") as HTMLElement,
  ).color;
  current.frame.getBoundingClientRect = () => ({ width: 350, height: 100 }) as DOMRect;
  act(() => {
    window.dispatchEvent(new Event("resize"));
    for (const receive of h.body)
      receive(body(2, html.replace("First", "Second"), { theme: darkTheme }));
  });
  current = await loaded();
  flush();
  expect(current.doc.getElementById("mail")?.textContent).toBe("Second");
  expect(
    current.win.getComputedStyle(current.doc.getElementById("mail") as HTMLElement).color,
  ).toBe(firstDark);
});

test("image regions keep their original foreground and inherited backdrop", async () => {
  const { doc, win } = await start(
    body(
      1,
      `<div style="background:#fff;color:#111">
    <div id="art" style="background-image:url(godwit-img://m9/art)">Caption</div>
    <span id="clear" style="color:rgba(0,0,0,0)">Hidden</span>
    <div id="ordinary">Reading</div></div>`,
      { theme: darkTheme },
    ),
  );
  const style = (id: string) => win.getComputedStyle(doc.getElementById(id) as HTMLElement);
  expect(style("art").color).toBe("rgb(17, 17, 17)");
  expect(style("art").backgroundColor).toBe("rgb(255, 255, 255)");
  expect(style("art").backgroundImage).toContain("godwit-img://m9/art");
  expect(style("clear").color).toBe("rgba(0, 0, 0, 0)");
  expect(style("ordinary").color).not.toBe("rgb(17, 17, 17)");
});

const channels = (value: string) => (value.match(/[\d.]+/g) ?? []).map(Number);
function renderedContrast(
  el: HTMLElement,
  win: Window,
  foreground = win.getComputedStyle(el).color,
) {
  const layers: HTMLElement[] = [];
  for (let node: HTMLElement | null = el; node; node = node.parentElement) layers.unshift(node);
  const composite = (front: number[], back: number[]) =>
    front.slice(0, 3).map((v, i) => v * (front[3] ?? 1) + back[i] * (1 - (front[3] ?? 1)));
  let bg = [255, 255, 255];
  for (const layer of layers) {
    const value = win.getComputedStyle(layer).backgroundColor;
    if (value && value !== "transparent") bg = composite(channels(value), bg);
  }
  const fg = composite(channels(foreground), bg);
  const light = (rgb: number[]) =>
    rgb
      .map((n) => {
        const s = n / 255;
        return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
      })
      .reduce((sum, n, i) => sum + n * [0.2126, 0.7152, 0.0722][i], 0);
  const [a, b] = [light(fg), light(bg)];
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

test("coloured surfaces, brand text and CTA keep their meaning and readable final colours", async () => {
  const html = `<div style="background:#fff;color:#111">
    <div id="warning" style="background:#fff0cc;color:#a31515">Payment needs attention</div>
    <div id="success" style="background:#dcfce7;color:#166534">Payment received</div>
    <span id="brand" style="color:#c22b4c">Northline</span>
    <div style="background:#171717"><span id="ready" style="color:#ff6b6b">Brand red</span></div>
    <span id="cta" data-href="https://example.com/order" style="background:#be185d;color:#fff">View order</span>
  </div>`;
  const input = body(1, html, { theme: darkTheme });
  const { doc, win } = await start(input);
  const element = (id: string) => doc.getElementById(id) as HTMLElement;
  const style = (id: string) => win.getComputedStyle(element(id));
  expect(
    channels(style("warning").backgroundColor)
      .slice(0, 3)
      .every((n) => n < 160),
  ).toBe(true);
  expect(channels(style("success").backgroundColor)[1]).toBeGreaterThan(
    channels(style("success").backgroundColor)[0],
  );
  expect(channels(style("brand").color)[0]).toBeGreaterThan(channels(style("brand").color)[1]);
  expect(style("brand").backgroundColor).toBe("rgba(0, 0, 0, 0)");
  expect(style("ready").color).toBe("rgb(255, 107, 107)");
  expect(style("cta").backgroundColor).toBe("rgb(190, 24, 93)");
  for (const id of ["warning", "success", "brand", "ready", "cta"])
    expect(renderedContrast(element(id), win)).toBeGreaterThanOrEqual(4.5);
  element("cta").focus();
  element("cta").dispatchEvent(
    new win.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
  );
  expect(api.openLink).toHaveBeenCalledExactlyOnceWith("https://example.com/order");
  expect(input.html).toBe(html);
});

test("coloured text uses final alpha composition and large-text thresholds without revealing invisible text", async () => {
  const html = `<div style="background:#fff;color:#111">
    <div style="background:#173348"><span id="alpha" style="color:rgba(187,22,78,.7)">Transparent brand</span></div>
    <p id="normal" style="background:#234;color:#426bba">Normal text</p>
    <p id="large" style="background:#234;color:#426bba;font-size:24px">Large text</p>
    <p id="bold" style="background:#234;color:#426bba;font-size:18.7px;font-weight:bold">Large bold</p>
    <div style="background:rgba(255,200,100,.5)">
      <span id="fallback" style="color:rgba(130,10,40,.05)">Keep original pairing</span>
      <span id="hidden" style="color:rgba(130,10,40,0)">Hidden</span>
    </div>
  </div>`;
  const { doc, win } = await start(body(1, html, { theme: darkTheme }));
  const element = (id: string) => doc.getElementById(id) as HTMLElement;
  const style = (id: string) => win.getComputedStyle(element(id));
  for (const id of ["alpha", "normal"])
    expect(renderedContrast(element(id), win)).toBeGreaterThanOrEqual(4.5);
  for (const id of ["large", "bold"]) {
    expect(renderedContrast(element(id), win)).toBeGreaterThanOrEqual(3);
    expect(renderedContrast(element(id), win)).toBeLessThan(4.5);
  }
  expect(channels(style("alpha").color)[3]).toBe(0.7);
  expect(style("fallback").color).toBe("rgba(130, 10, 40, 0.05)");
  expect(style("fallback").backgroundColor).toBe("rgb(255, 228, 178)");
  expect(style("hidden").color).toBe("rgba(130, 10, 40, 0)");
});

test("borders and focus stay visible while shadow geometry and link behaviour are preserved", async () => {
  const html = `<div id="paper" style="background:#fff;color:#111">
    <div id="card" style="background:#eee;border:2px solid #ddd;box-shadow:0 4px 22px rgba(46,92,255,.1),inset 1px 2px 3px rgba(255,255,255,.66)">Card</div>
    <span id="link" data-href="https://example.com" style="outline:2px solid #111;color:#163a80;text-shadow:1px 2px 3px rgba(255,255,255,.6)">Open order</span>
  </div>`;
  const { doc, win } = await start(body(1, html, { theme: darkTheme }));
  const card = doc.getElementById("card") as HTMLElement;
  const link = doc.getElementById("link") as HTMLElement;
  const cardStyle = win.getComputedStyle(card);
  expect(cardStyle.borderTopWidth).toBe("2px");
  expect(cardStyle.borderTopStyle).toBe("solid");
  expect(channels(cardStyle.borderTopColor)[0]).toBeLessThan(130);
  expect(renderedContrast(card, win, cardStyle.borderTopColor)).toBeGreaterThanOrEqual(1.5);
  link.focus();
  const linkStyle = win.getComputedStyle(link);
  // jsdom does not expand outline's shorthand into computed width and style.
  expect(link.style.outline).toBe("2px solid #111");
  expect(
    renderedContrast(doc.getElementById("paper") as HTMLElement, win, linkStyle.outlineColor),
  ).toBeGreaterThanOrEqual(3);
  const geometry = (value: string) =>
    value
      .replace(/rgba?\([^)]*\)/g, "")
      .replace(/\s+/g, " ")
      .replace(/\s*,\s*/g, ", ")
      .trim();
  expect(geometry(cardStyle.boxShadow)).toBe("0 4px 22px, inset 1px 2px 3px");
  expect(geometry(linkStyle.textShadow)).toBe("1px 2px 3px");
  for (const color of `${cardStyle.boxShadow} ${linkStyle.textShadow}`.match(/rgba?\([^)]*\)/g) ??
    [])
    expect(Math.max(...channels(color).slice(0, 3))).toBeLessThan(50);
  link.click();
  expect(api.openLink).toHaveBeenCalledExactlyOnceWith("https://example.com");
});

test("the coloured preview restores light styling and recomputes stable colours on resize and replacement", async () => {
  let current = await start(body(1, readerColored, { theme: darkTheme }));
  const colours = () =>
    ["colored-brand", "colored-warning", "colored-success", "colored-cta"].map((id) => {
      const style = current.win.getComputedStyle(current.doc.getElementById(id) as HTMLElement);
      return [style.color, style.backgroundColor, style.borderTopColor];
    });
  const first = colours();
  current.frame.getBoundingClientRect = () => ({ width: 380, height: 100 }) as DOMRect;
  act(() => window.dispatchEvent(new Event("resize")));
  flush();
  expect(colours()).toEqual(first);
  act(() => {
    for (const receive of h.theme) receive({ ...darkTheme, mode: "light" });
  });
  current = await loaded();
  expect(
    current.win.getComputedStyle(current.doc.getElementById("colored-brand") as HTMLElement).color,
  ).toBe("rgb(194, 43, 76)");
  expect(
    current.win.getComputedStyle(current.doc.getElementById("colored-warning") as HTMLElement)
      .backgroundColor,
  ).toBe("rgb(255, 240, 204)");
  act(() => {
    for (const receive of h.theme) receive(darkTheme);
    for (const receive of h.body)
      receive(
        body(2, readerColored.replace("Your delivery", "Updated delivery"), { theme: darkTheme }),
      );
  });
  current = await loaded();
  expect(colours()).toEqual(first);
  expect(current.doc.querySelector("h1")?.textContent).toBe("Updated delivery, in full colour");
});

test("the final rendered neutral text meets the promised contrast, including channel rounding", async () => {
  const html = [30, 40, 50, 60, 70, 80, 90]
    .map(
      (n) =>
        `<p style="background:rgb(${n},${n},${n});color:rgb(${n + 1},${n + 1},${n + 1})">Readable</p>`,
    )
    .join("");
  const { doc, win } = await start(body(1, html, { theme: darkTheme }));
  const luminance = (value: string) => {
    const rgb = (value.match(/[\d.]+/g) ?? [])
      .slice(0, 3)
      .map(Number)
      .map((n) => {
        const s = n / 255;
        return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
      });
    return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
  };
  for (const paragraph of doc.querySelectorAll("p")) {
    const style = win.getComputedStyle(paragraph);
    const fg = luminance(style.color);
    const bg = luminance(style.backgroundColor);
    expect((Math.max(fg, bg) + 0.05) / (Math.min(fg, bg) + 0.05)).toBeGreaterThanOrEqual(4.5);
  }
});
