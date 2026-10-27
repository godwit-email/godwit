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
import { readerColored, readerGradients, readerMixed } from "@/preview/fixtures";
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
async function loaded(fireLoad = true) {
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
  for (const node of [...doc.childNodes])
    if (node.nodeType === Node.COMMENT_NODE) doc.removeChild(node);
  for (const node of parsed.childNodes)
    if (node.nodeType === Node.COMMENT_NODE) doc.appendChild(doc.importNode(node, true));
  if (fireLoad) {
    act(() => {
      frame.dispatchEvent(new Event("load"));
    });
  }
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

test("the mixed preview preserves artwork and fallback alongside readable independent regions", async () => {
  const input = body(1, readerMixed);
  let current = await start(input);
  const light = current.doc.querySelector(".mixed-mail")?.innerHTML;
  const element = (id: string) => current.doc.getElementById(id) as HTMLElement;
  const style = (id: string) => current.win.getComputedStyle(element(id));
  const art = style("mixed-art").backgroundImage;
  const gradient = style("mixed-responsive").backgroundImage;
  const logo = element("mixed-logo").getAttribute("src");
  act(() => {
    for (const receive of h.theme) receive(darkTheme);
  });
  current = await loaded();
  expect(style("mixed-art").backgroundImage).toBe(art);
  expect(style("mixed-art").backgroundColor).toBe("rgb(255, 255, 255)");
  expect(style("mixed-caption").color).toBe("rgb(17, 17, 17)");
  expect(style("mixed-solid").backgroundColor).not.toBe("rgb(220, 252, 231)");
  expect(style("mixed-cta").backgroundColor).toBe("rgb(190, 24, 93)");
  expect(style("mixed-gradient").backgroundImage).toBe("none");
  expect(style("mixed-responsive").backgroundImage).toBe(gradient);
  expect(style("mixed-responsive").backgroundColor).toBe("rgb(255, 255, 255)");
  expect(style("mixed-faint").color).toBe("rgba(130, 10, 40, 0.05)");
  expect(style("mixed-logo").backgroundColor).toBe("rgb(255, 255, 255)");
  expect(element("mixed-logo").getAttribute("src")).toBe(logo);
  for (const id of [
    "mixed-primary",
    "mixed-secondary",
    "mixed-solid-text",
    "mixed-cta",
    "mixed-gradient",
  ])
    expect(renderedContrast(element(id), current.win)).toBeGreaterThanOrEqual(4.5);
  expect(style("mixed-neutral").backgroundColor).not.toBe(style("mixed-art").backgroundColor);
  act(() => {
    for (const receive of h.theme) receive(theme);
  });
  current = await loaded();
  expect(current.doc.querySelector(".mixed-mail")?.innerHTML).toBe(light);
  expect(input.html).toBe(readerMixed);
});

test("the mixed preview leaves and re-enters fallback when the active width rules change", async () => {
  const { frame, doc, win } = await start(body(1, readerMixed, { theme: darkTheme }));
  const sheet = doc.getElementById("mixed-rules") as HTMLStyleElement;
  const rules = sheet.textContent;
  const element = (id: string) => doc.getElementById(id) as HTMLElement;
  const style = (id: string) => win.getComputedStyle(element(id));
  const pairing = () => [
    style("mixed-responsive").backgroundImage,
    style("mixed-responsive").backgroundColor,
    style("mixed-faint").color,
  ];
  const fallback = pairing();
  const cta = style("mixed-cta").color;
  const resize = (width: number, activeRules: string) => {
    // jsdom has no media-query layout; the real preview exercises these rules at each width.
    sheet.textContent = rules + activeRules;
    frame.getBoundingClientRect = () => ({ width, height: 100 }) as DOMRect;
    act(() => window.dispatchEvent(new Event("resize")));
    flush();
  };
  const narrowRules =
    "#godwit-body .mixed-responsive {background-image:linear-gradient(rgb(230,240,255),rgb(190,210,255))} #godwit-body .mixed-faint {color:#532c78}";
  resize(380, narrowRules);
  const adapted = pairing();
  expect(adapted[0]).toBe("none");
  expect(renderedContrast(element("mixed-faint"), win)).toBeGreaterThanOrEqual(4.5);
  resize(740, "");
  expect(pairing()).toEqual(fallback);
  resize(380, narrowRules);
  expect(pairing()).toEqual(adapted);
  expect(style("mixed-cta").color).toBe(cta);
});

test("system appearance, font and competing mixed bodies settle on the latest input without old listeners", async () => {
  const system = Object.assign(new EventTarget(), { matches: true });
  vi.stubGlobal("matchMedia", () => system);
  const systemTheme = { ...darkTheme, mode: "system" };
  let current = await start(body(1, readerMixed, { theme: systemTheme }));
  const oldImage = current.doc.getElementById("mixed-logo") as HTMLImageElement;
  const oldLink = current.doc.getElementById("mixed-cta") as HTMLElement;
  const original = current.win.getComputedStyle(
    current.doc.getElementById("mixed-primary") as HTMLElement,
  ).color;
  current.frame.getBoundingClientRect = () => ({ width: 380, height: 100 }) as DOMRect;
  act(() => {
    window.dispatchEvent(new Event("resize"));
    oldImage.dispatchEvent(new Event("load"));
    for (const receive of h.theme) {
      receive(theme);
      receive({ ...systemTheme, bodyFontSize: 18 });
    }
    for (const receive of h.body) {
      receive(body(3, readerMixed.replace("The complete delivery note", "Translated delivery")));
      receive(body(2, "<p>Stale original body</p>"));
      receive(body(4, readerMixed.replace("The complete delivery note", "Latest delivery")));
    }
  });
  flush();
  current = await loaded(false);
  flush();
  flush();
  expect(current.doc.querySelector("h1")?.textContent).toBe("Latest delivery");
  expect(current.win.getComputedStyle(current.doc.documentElement).fontSize).toBe("18px");
  expect(
    current.win.getComputedStyle(current.doc.getElementById("mixed-primary") as HTMLElement).color,
  ).toBe(original);
  expect(
    current.win.getComputedStyle(current.doc.getElementById("godwit-body") as HTMLElement)
      .visibility,
  ).toBe("visible");
  vi.mocked(api.height).mockClear();
  oldImage.dispatchEvent(new Event("load"));
  oldLink.click();
  oldLink.dispatchEvent(new current.win.KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  flush();
  expect(api.height).not.toHaveBeenCalled();
  expect(api.openLink).not.toHaveBeenCalled();
  const link = current.doc.getElementById("mixed-cta") as HTMLElement;
  link.dispatchEvent(
    new current.win.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
  );
  expect(api.openLink).toHaveBeenCalledExactlyOnceWith("https://example.com/delivery");
  act(() => {
    system.matches = false;
    system.dispatchEvent(new Event("change"));
  });
  current = await loaded();
  expect(
    current.win.getComputedStyle(current.doc.querySelector(".mixed-mail") as HTMLElement)
      .backgroundColor,
  ).toBe("rgb(255, 255, 255)");
  act(() => {
    system.matches = true;
    system.dispatchEvent(new Event("change"));
  });
  current = await loaded();
  expect(current.doc.querySelector("h1")?.textContent).toBe("Latest delivery");
  expect(current.win.getComputedStyle(current.doc.documentElement).fontSize).toBe("18px");
  expect(
    current.win.getComputedStyle(current.doc.getElementById("mixed-primary") as HTMLElement).color,
  ).toBe(original);
});

test("text-bearing gradients become readable solid surfaces while decoration keeps its gradient", async () => {
  const html = `<div style="background:#fff;color:#111">
    <div id="direct" style="background-image:linear-gradient(160deg,rgb(255,240,204),rgb(255,210,160));padding:16px">Your delivery</div>
    <div id="nested" style="background-image:linear-gradient(to right,rgb(240,240,240),rgb(210,210,210))">
      <p><span id="nested-text">The delivery details</span></p>
    </div>
    <div id="decoration" style="background-image:linear-gradient(rgb(255,240,204),rgb(255,210,160));height:12px">
      <span style="display:none">Hidden preheader</span><span style="color:rgba(0,0,0,0)">Invisible</span><span style="font-size:0">Zero-size preheader</span>
    </div>
  </div>`;
  const { doc, win } = await start(body(1, html, { theme: darkTheme }));
  const element = (id: string) => doc.getElementById(id) as HTMLElement;
  const style = (id: string) => win.getComputedStyle(element(id));
  for (const id of ["direct", "nested"]) {
    expect(style(id).backgroundImage).toBe("none");
    expect(Math.max(...channels(style(id).backgroundColor).slice(0, 3))).toBeLessThan(100);
  }
  for (const id of ["direct", "nested-text"])
    expect(renderedContrast(element(id), win)).toBeGreaterThanOrEqual(4.5);
  expect(style("decoration").backgroundImage).toContain("linear-gradient");
  expect(style("decoration").height).toBe("12px");
  expect(style("direct").padding).toBe("16px");
});

test("gradient stops and alpha share the solid-surface contrast rules and inherited backdrop", async () => {
  const html = `<div style="background:#ccc;color:#111">
    <div id="gradient" style="background-image:linear-gradient(120deg,rgba(255,255,255,.4) 0%,rgba(100,140,220,.6) 100%)">
      <span id="gradient-text" style="color:rgba(130,10,40,.7)">Transparent gradient</span>
    </div>
    <div id="solid" style="background:rgb(183,195,219)">
      <span id="solid-text" style="color:rgba(130,10,40,.7)">Matching solid surface</span>
    </div>
    <div id="radial" style="background-image:radial-gradient(circle at center,rgb(255,240,204) 20%,rgb(255,210,160) 80%)">Radial heading</div>
  </div>`;
  const { doc, win } = await start(body(1, html, { theme: darkTheme }));
  const element = (id: string) => doc.getElementById(id) as HTMLElement;
  const style = (id: string) => win.getComputedStyle(element(id));
  expect(style("gradient").backgroundImage).toBe("none");
  expect(style("gradient").backgroundColor).toBe(style("solid").backgroundColor);
  expect(style("gradient-text").color).toBe(style("solid-text").color);
  expect(channels(style("gradient-text").color)[3]).toBe(0.7);
  expect(renderedContrast(element("gradient-text"), win)).toBeGreaterThanOrEqual(4.5);
  expect(style("radial").backgroundImage).toBe("none");
  expect(renderedContrast(element("radial"), win)).toBeGreaterThanOrEqual(4.5);
});

test("complex gradient combinations preserve their original pairing and the required ancestor artwork", async () => {
  const html = `<div style="background:#fff;color:#111">
    <div id="outer" style="background-image:linear-gradient(rgb(255,255,255),rgb(200,200,200))">
      <p id="caption">Original outer caption</p>
      <div id="complex" style="background-image:linear-gradient(rgba(255,220,160,.5),transparent),linear-gradient(90deg,transparent,rgb(255,255,255))">Layered caption</div>
      <div id="solid-card" style="background:#fff"><span id="card-text">Independent card</span></div>
    </div>
    <div id="url" style="background-image:linear-gradient(rgb(255,255,255),rgb(200,200,200)),url(godwit-img://m9/art)">Image caption</div>
    <div id="length-stops" style="background-image:linear-gradient(rgb(255,255,255) 10px,rgb(200,200,200) 80px)">Unresolved lengths</div>
    <div id="opacity-parent" style="background:#fff">
      <div style="opacity:.4">
        <div id="opacity-gradient" style="background:#fff;background-image:linear-gradient(rgb(255,255,255),rgb(200,200,200))">Dim caption</div>
      </div>
    </div>
  </div>`;
  const { doc, win } = await start(body(1, html, { theme: darkTheme }));
  const element = (id: string) => doc.getElementById(id) as HTMLElement;
  const style = (id: string) => win.getComputedStyle(element(id));
  for (const id of ["outer", "complex", "url", "length-stops", "opacity-gradient"]) {
    expect(style(id).backgroundImage).toContain("gradient");
    expect(style(id).color).toBe("rgb(17, 17, 17)");
  }
  for (const id of ["outer", "url", "length-stops", "opacity-parent"])
    expect(style(id).backgroundColor).toBe("rgb(255, 255, 255)");
  expect(style("url").backgroundImage).toContain("godwit-img://m9/art");
  expect(style("solid-card").backgroundColor).not.toBe("rgb(255, 255, 255)");
  expect(renderedContrast(element("card-text"), win)).toBeGreaterThanOrEqual(4.5);
});

test("opacity protected text keeps the ancestor gradient through transparent intermediate elements", async () => {
  const html = `<div style="background:#fff;color:#111">
    <div id="gradient" style="background-image:linear-gradient(rgb(255,255,255),rgb(100,100,100))">
      <div id="transparent"><span id="dim" style="opacity:.2">Dim caption</span></div>
    </div>
  </div>`;
  const { doc, win } = await start(body(1, html, { theme: darkTheme }));
  const style = (id: string) => win.getComputedStyle(doc.getElementById(id) as HTMLElement);
  expect(style("gradient").backgroundImage).toContain("linear-gradient");
  expect(style("gradient").backgroundColor).toBe("rgb(255, 255, 255)");
  expect(style("transparent").backgroundColor).toBe("rgba(0, 0, 0, 0)");
  expect(style("dim").color).toBe("rgb(17, 17, 17)");
  expect(style("dim").opacity).toBe("0.2");
});

test("unreachable text contrast restores its original ancestor gradient without hiding the document", async () => {
  const html = `<div style="background:#fff;color:#111">
    <div id="gradient" style="background-image:linear-gradient(rgb(255,255,255),rgb(100,100,100))">
      <div><span id="faint" style="color:rgba(0,0,0,.05)">Original faint caption</span></div>
      <div id="card" style="background:#fff">Independent card</div>
    </div>
    <p id="ordinary">Ordinary text still adapts.</p>
  </div>`;
  const { doc, win } = await start(body(1, html, { theme: darkTheme }));
  const style = (id: string) => win.getComputedStyle(doc.getElementById(id) as HTMLElement);
  expect(style("gradient").backgroundImage).toContain("linear-gradient");
  expect(style("gradient").backgroundColor).toBe("rgb(255, 255, 255)");
  expect(style("faint").color).toBe("rgba(0, 0, 0, 0.05)");
  expect(style("faint").backgroundColor).toBe("rgba(0, 0, 0, 0)");
  expect(style("card").backgroundColor).not.toBe("rgb(255, 255, 255)");
  expect(style("ordinary").color).not.toBe("rgb(17, 17, 17)");
  expect(doc.getElementById("godwit-body")?.classList.contains("godwit-auto")).toBe(false);
});

test("the gradient preview restores light artwork and recomputes from each current body and width", async () => {
  const input = body(1, readerGradients, { theme: darkTheme });
  let current = await start(input);
  const style = (id: string) =>
    current.win.getComputedStyle(current.doc.getElementById(id) as HTMLElement);
  const colours = () =>
    ["gradient-heading", "gradient-nested", "gradient-alpha"].map((id) => [
      style(id).backgroundImage,
      style(id).backgroundColor,
      style(id).color,
    ]);
  const first = colours();
  expect(first.every(([image]) => image === "none")).toBe(true);
  current.frame.getBoundingClientRect = () => ({ width: 380, height: 100 }) as DOMRect;
  // jsdom has no media-query layout: supply the rule a narrower viewport would activate.
  const sheet = current.doc.getElementById("gradient-rules") as HTMLStyleElement;
  sheet.textContent +=
    "#godwit-body .gradient-responsive {background-image:linear-gradient(rgb(45,60,90),rgb(20,35,60));color:#ddd}";
  act(() => window.dispatchEvent(new Event("resize")));
  flush();
  expect(colours()).toEqual(first);
  expect(style("gradient-responsive").backgroundImage).toBe("none");
  expect(style("gradient-responsive").color).toBe("rgb(221, 221, 221)");
  expect(
    renderedContrast(current.doc.getElementById("gradient-responsive") as HTMLElement, current.win),
  ).toBeGreaterThanOrEqual(4.5);
  act(() => {
    for (const receive of h.theme) receive({ ...darkTheme, mode: "light" });
  });
  current = await loaded();
  for (const id of ["gradient-heading", "gradient-nested", "gradient-alpha"])
    expect(style(id).backgroundImage).toContain("gradient");
  expect(style("gradient-heading").color).toBe("rgb(17, 17, 17)");
  act(() => {
    for (const receive of h.theme) receive(darkTheme);
    for (const receive of h.body)
      receive(
        body(2, readerGradients.replace("A softer reading surface", "Updated delivery"), {
          theme: darkTheme,
        }),
      );
  });
  current = await loaded();
  expect(colours()).toEqual(first);
  expect(current.doc.getElementById("gradient-heading")?.textContent).toBe("Updated delivery");
  const link = current.doc.getElementById("gradient-tail") as HTMLElement;
  link.dispatchEvent(
    new current.win.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
  );
  expect(api.openLink).toHaveBeenCalledExactlyOnceWith("https://example.com/delivery");
  expect(input.html).toBe(readerGradients);
});

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

test("image backdrops stay inside the image content box without changing its pixels or dimensions", async () => {
  const src =
    "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAACklEQVR4nGMAAQAABQABDQottAAAAABJRU5ErkJggg==";
  const html = `<div id="paper" style="background:#fff;color:#111">
    <div style="background:rgba(0,0,0,.2)">
      <img id="logo" src="${src}" width="96" height="32" style="padding:12px;border:2px solid #111" alt="Transparent logo">
    </div><p id="nearby">Ordinary text</p></div>`;
  const { doc, win } = await start(body(1, html, { theme: darkTheme }));
  const img = doc.getElementById("logo") as HTMLImageElement;
  const style = win.getComputedStyle(img);
  expect(style.backgroundColor).toBe("rgb(204, 204, 204)");
  expect(style.backgroundClip).toBe("content-box");
  expect(style.padding).toBe("12px");
  expect(style.borderWidth).toBe("2px");
  expect(style.filter).not.toMatch(/invert|hue-rotate/);
  expect(img.getAttribute("src")).toBe(src);
  expect(img.getAttribute("width")).toBe("96");
  expect(img.getAttribute("height")).toBe("32");
  expect(img.parentElement?.children).toHaveLength(1);
  expect(win.getComputedStyle(doc.getElementById("paper") as HTMLElement).backgroundColor).not.toBe(
    "rgb(255, 255, 255)",
  );
});

test("background artwork keeps its composed backdrop and text while opaque cards and siblings adapt", async () => {
  const { doc, win } = await start(
    body(
      1,
      `<div style="background:#fff;color:#111">
    <div style="background:rgba(0,0,0,.2)">
      <div id="art" style="background-image:url(godwit-img://m9/art);background-size:24px 16px;background-position:5px 7px;background-repeat:repeat-x">
        <p id="caption">Original caption</p>
        <div id="tint" style="background:rgba(255,255,255,.5)"><p id="tinted-caption">Translucent inset</p></div>
        <div id="card" style="background:#fff"><p id="card-text">Independent solid card</p></div>
      </div>
    </div><p id="ordinary">Neighbouring text</p></div>`,
      { theme: darkTheme },
    ),
  );
  const style = (id: string) => win.getComputedStyle(doc.getElementById(id) as HTMLElement);
  expect(style("art").backgroundColor).toBe("rgb(204, 204, 204)");
  expect(style("art").backgroundImage).toContain("godwit-img://m9/art");
  expect(style("art").backgroundSize).toBe("24px 16px");
  expect(style("art").backgroundPosition).toBe("5px 7px");
  expect(style("art").backgroundRepeat).toBe("repeat-x");
  expect(style("caption").color).toBe("rgb(17, 17, 17)");
  expect(style("tint").backgroundColor).toBe("rgba(255, 255, 255, 0.5)");
  expect(style("tinted-caption").color).toBe("rgb(17, 17, 17)");
  expect(style("card").backgroundColor).not.toBe("rgb(255, 255, 255)");
  expect(style("card-text").color).not.toBe("rgb(17, 17, 17)");
  expect(style("ordinary").color).not.toBe("rgb(17, 17, 17)");
});

test("opaque coloured cards escape media protection but retain opacity protection", async () => {
  const { doc, win } = await start(
    body(
      1,
      `<div style="background:#fff;color:#111">
      <div style="background-image:url(godwit-img://m9/art)">
        <div id="card" style="background:#dcfce7;color:#166534">Independent coloured card</div>
      </div>
      <div style="background:#fff0cc;color:#a31515">
        <div style="opacity:.5;background-image:url(godwit-img://m9/art)">
          <div id="faded" style="background:#dcfce7;color:#166534">Keep original pairing</div>
        </div>
      </div>
    </div>`,
      { theme: darkTheme },
    ),
  );
  const card = doc.getElementById("card") as HTMLElement;
  const faded = doc.getElementById("faded") as HTMLElement;
  expect(win.getComputedStyle(card).backgroundColor).not.toBe("rgb(220, 252, 231)");
  expect(renderedContrast(card, win)).toBeGreaterThanOrEqual(4.5);
  expect(win.getComputedStyle(faded).backgroundColor).toBe("rgb(220, 252, 231)");
  expect(win.getComputedStyle(faded).color).toBe("rgb(22, 101, 52)");
});

test("parsed dark mail is readable before delayed images finish loading", async () => {
  vi.mocked(api.load).mockResolvedValue(
    body(
      1,
      `<div style="background:#fff;color:#111">
    <p id="waiting-text">Read while the image is pending</p><img id="waiting-image" src="godwit-img://m9/slow" width="320" height="180">
  </div>`,
      { theme: darkTheme, remote: true },
    ),
  );
  render(<ReaderApp />);
  const { doc, win } = await loaded(false);
  Object.defineProperty(doc, "readyState", { configurable: true, value: "interactive" });
  flush();
  const wrap = doc.getElementById("godwit-body") as HTMLElement;
  expect(win.getComputedStyle(wrap).visibility).toBe("visible");
  expect(win.getComputedStyle(doc.getElementById("waiting-text") as HTMLElement).color).not.toBe(
    "rgb(17, 17, 17)",
  );
  expect(
    win.getComputedStyle(doc.getElementById("waiting-image") as HTMLElement).backgroundColor,
  ).toBe("rgb(255, 255, 255)");
  flush();
  expect(api.height).toHaveBeenCalled();
});

test("image permission, failure and retry keep the backdrop and detach the old document", async () => {
  const markup = (source: string) => `<div style="background:#fff;color:#111">
    <img id="image" ${source} width="96" height="32" alt="Company logo">
    <p data-href="https://example.com">Open details</p></div>`;
  let current = await start(body(1, markup(""), { theme: darkTheme, blocked: 1 }));
  expect(current.doc.querySelector("img")?.getAttribute("src")).toBeNull();
  const oldImage = current.doc.querySelector("img") as HTMLImageElement;
  const allowed = markup('src="godwit-img://m9/logo"');
  act(() => {
    for (const receive of h.body) receive(body(2, allowed, { theme: darkTheme, remote: true }));
  });
  current = await loaded(false);
  flush();
  const image = current.doc.querySelector("img") as HTMLImageElement;
  const wrap = current.doc.getElementById("godwit-body") as HTMLElement;
  wrap.getBoundingClientRect = () => ({ height: 120 }) as DOMRect;
  image.dispatchEvent(new Event("error"));
  flush();
  expect(current.win.getComputedStyle(image).backgroundColor).toBe("rgb(255, 255, 255)");
  expect(api.height).toHaveBeenLastCalledWith(120);
  act(() => current.frame.dispatchEvent(new Event("load")));
  (current.doc.querySelector("[data-href]") as HTMLElement).click();
  expect(api.openLink).toHaveBeenCalledExactlyOnceWith("https://example.com");

  const before = current.frame.getAttribute("srcdoc");
  act(() => {
    for (const receive of h.body) receive(body(3, allowed, { theme: darkTheme, remote: true }));
  });
  expect(current.frame.getAttribute("srcdoc")).not.toBe(before);
  flush(); // The previous document cannot satisfy the new document's readiness check.
  current = await loaded(false);
  flush();
  flush();
  expect(
    current.win.getComputedStyle(current.doc.querySelector("img") as HTMLElement).backgroundClip,
  ).toBe("content-box");
  vi.mocked(api.height).mockClear();
  oldImage.dispatchEvent(new Event("load"));
  image.dispatchEvent(new Event("load"));
  flush();
  expect(api.height).not.toHaveBeenCalled();

  act(() => {
    for (const receive of h.theme) receive({ ...darkTheme, mode: "light" });
  });
  current = await loaded();
  expect(current.doc.querySelector("img")?.getAttribute("style")).toBeNull();
  expect(current.doc.querySelector("img")?.getAttribute("src")).toBe("godwit-img://m9/logo");
});
