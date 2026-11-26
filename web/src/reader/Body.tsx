import { RiRefreshLine } from "@remixicon/react";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/button";
import {
  ReaderPage as api,
  events,
  type ReaderBinding,
  type ReaderBody,
  type ReaderGlassRect,
  type Theme,
} from "@/mygo";
import { useT } from "@/shared/i18n";
import { forwardShortcuts } from "@/shared/shortcuts";
import { applyTheme, type Theme as PageTheme } from "@/shared/theme";
import { adaptDarkMode, brightness, type Color, over, parse } from "./dark";
import { buildSrcdoc, foldQuotes, frameScheme, isDark } from "./frame";

const ignore = () => {};
let nextDocument = 0;

// Go refuses a taller body (webui maxBodyHeight): the page clamps to it.
const MAX_HEIGHT = 1 << 20;
const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

type LoadFailure = { binding: number; error: string; retry: () => void };

function readerShortcuts(binding: number, win: Window) {
  return forwardShortcuts(
    (dir) => api.navigate(binding, dir),
    win,
    () => api.search(binding),
    () => api.save(binding),
    () => api.print(binding),
  );
}

/** The trusted page stays loaded; each assignment owns a fresh mail document. */
export function ReaderApp() {
  const [page, setPage] = useState<{ view?: ReaderBody | LoadFailure; theme?: Theme }>({});
  const latest = useRef(page); // native events can supersede a queued rAF before React commits

  useEffect(() => {
    let live = true;
    let binding: ReaderBinding | undefined;
    let request: AbortController | undefined;
    let version = 0;
    let pushed: Theme | undefined; // a theme event is newer than any body's theme
    const show = (view?: ReaderBody | LoadFailure, theme = latest.current.theme) => {
      latest.current = { view, theme };
      setPage(latest.current);
    };
    const owns = (id: number) => live && binding?.active && binding.id === id;
    const apply = (b: ReaderBody) => {
      if (!owns(b.binding) || b.version <= version) return;
      version = b.version;
      show(b, pushed ?? b.theme);
    };
    const load = (id: number) => {
      if (!owns(id) || version) return;
      request?.abort();
      request = new AbortController();
      const { signal } = request;
      show();
      api.load
        .with({ signal })(id)
        .then(
          (b) => {
            if (!signal.aborted && b.binding === id) apply(b);
          },
          (e) => {
            if (!signal.aborted && owns(id) && !version)
              show({ binding: id, error: message(e), retry: () => load(id) });
          },
        );
    };
    const assign = (next: ReaderBinding) => {
      if (!live || (binding && next.id < binding.id)) return;
      // Idle wins for the same id, including over a late bootstrap.
      if (next.id === binding?.id && (!binding.active || next.active)) return;
      binding = next;
      version = 0;
      if (next.active) load(next.id);
      else {
        request?.abort();
        show();
      }
    };
    // Listen before asking: mygo drops events sent before a listener exists.
    const offs = [
      events.readerBinding.on(assign),
      events.readerBody.on(apply),
      events.theme.on((t) => {
        pushed = t;
        show(latest.current.view, t);
      }),
    ];
    api.binding().then(assign, ignore);
    return () => {
      live = false;
      request?.abort();
      for (const off of offs) off();
    };
  }, []);

  const { view, theme } = page;
  const current = useCallback(() => latest.current === page, [page]);
  const id = view?.binding;
  useEffect(() => (id === undefined ? undefined : readerShortcuts(id, window)), [id]);
  useEffect(() => (theme ? applyTheme(theme as PageTheme) : undefined), [theme]);

  if (view && "error" in view) return <Failed key={view.binding} {...view} current={current} />;
  if (!view || !theme) return <main />;
  return (
    <main key={view.binding}>
      <Body body={view} theme={theme} current={current} />
      <Glass binding={view.binding} />
    </main>
  );
}

// The blur of the native reader's frosted header and floating reply card (kit.Bar)
// when system native glass is unavailable: each visibly covered strip blurs here,
// directly under its translucent native tint.
const glassStyle = {
  backdropFilter: "blur(16px)",
  WebkitBackdropFilter: "blur(16px)",
  display: "none",
  opacity: "1",
  willChange: "transform, opacity",
  contain: "strict",
} as const;

const emptyRect: ReaderGlassRect = { x: 0, y: 0, w: 0, h: 0, r: 0 };
const isVisible = (r?: ReaderGlassRect): r is ReaderGlassRect => !!r && r.w > 0 && r.h > 0;

// During a fast scroll fling, async cross-process IPC lags behind the native
// WKWebView frame by 1–2 frames. Fading the fallback blur while scrolling fast
// prevents blurred strips from visibly sticking out past the native card/header
// (whose translucent tint still tracks synchronously at 0 ms lag).
const FAST_SCROLL_DY = 24;
const FAST_SCROLL_VELOCITY = 1.5; // px/ms
const GLASS_SETTLE_MS = 90;

function applyBand(el: HTMLDivElement | null, r: ReaderGlassRect, prev: ReaderGlassRect) {
  if (!el) return;
  if (!isVisible(r)) {
    if (el.style.display !== "none") el.style.display = "none";
    return;
  }
  if (el.style.display !== "block") el.style.display = "block";
  el.style.transform = `translate3d(${r.x}px, ${r.y}px, 0)`;
  if (r.w !== prev.w) el.style.width = `${r.w}px`;
  if (r.h !== prev.h) el.style.height = `${r.h}px`;
  if (r.r !== prev.r) el.style.borderRadius = `${r.r}px`;
}

/**
 * Blurs what the reader's frosted header and floating reply card cover of the page,
 * mutating DOM refs directly via GPU translate3d (zero React re-renders during scroll)
 * and fading briefly during fast flings so async IPC never leaves trailing ghosts.
 */
export function Glass({ binding }: { binding: number }) {
  const topRef = useRef<HTMLDivElement>(null);
  const botRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let prevTop = emptyRect;
    let prevBot = emptyRect;
    let prevTime = 0;
    let faded = false;
    let settle: ReturnType<typeof setTimeout> | undefined;

    const setOpacity = (opacity: string, transition: string) => {
      for (const el of [topRef.current, botRef.current]) {
        if (!el) continue;
        el.style.transition = transition;
        el.style.opacity = opacity;
      }
    };

    const off = events.readerGlass.on((g) => {
      if (g.binding !== binding) return;
      const top = g.top ?? emptyRect;
      const bot = g.bottom ?? emptyRect;
      const now = performance.now();

      let dy = 0;
      if (isVisible(top) && isVisible(prevTop)) dy = Math.max(dy, Math.abs(top.y - prevTop.y));
      if (isVisible(bot) && isVisible(prevBot)) dy = Math.max(dy, Math.abs(bot.y - prevBot.y));

      const dt = prevTime > 0 ? Math.max(now - prevTime, 4) : 16;
      const fast = dy >= FAST_SCROLL_DY || (dy > 0 && dy / dt >= FAST_SCROLL_VELOCITY);

      applyBand(topRef.current, top, prevTop);
      applyBand(botRef.current, bot, prevBot);
      prevTop = top;
      prevBot = bot;
      prevTime = now;

      if (!isVisible(top) && !isVisible(bot)) {
        if (settle !== undefined) {
          clearTimeout(settle);
          settle = undefined;
        }
        if (faded) {
          faded = false;
          setOpacity("1", "none");
        }
        return;
      }

      if (fast) {
        if (!faded) {
          faded = true;
          setOpacity("0", "opacity 40ms linear");
        }
        if (settle !== undefined) clearTimeout(settle);
        settle = setTimeout(() => {
          settle = undefined;
          faded = false;
          setOpacity("1", "opacity 120ms ease-out");
        }, GLASS_SETTLE_MS);
      }
    });

    return () => {
      off();
      if (settle !== undefined) clearTimeout(settle);
    };
  }, [binding]);

  return (
    <>
      <div
        ref={topRef}
        data-glass="top"
        className="pointer-events-none fixed top-0 left-0 overflow-hidden"
        style={glassStyle}
      />
      <div
        ref={botRef}
        data-glass="bottom"
        className="pointer-events-none fixed top-0 left-0 overflow-hidden"
        style={glassStyle}
      />
    </>
  );
}

// A locale change can rewire the same mail document, but it has only one first paint.
const painted = new WeakSet<Element>();

function bodyLayout(binding: number, element: Element, current: () => boolean) {
  let live = true;
  let frame = 0;
  const pending = () => live && element.isConnected && current() && !painted.has(element);
  return {
    async height(px: number) {
      if (!live || !current()) return;
      try {
        await api.height(binding, px);
      } catch {
        return; // the assignment may already be released
      }
      // Later resizes still report, but cannot postpone the first drawable frame.
      if (frame || !pending()) return;
      frame = requestAnimationFrame(() => {
        // rAF runs before paint. Leave a frame at the acknowledged size before the handoff.
        frame = requestAnimationFrame(() => {
          frame = 0;
          if (!pending()) return;
          painted.add(element);
          api.painted(binding).catch(ignore);
        });
      });
    },
    stop() {
      live = false;
      cancelAnimationFrame(frame);
    },
  };
}

function Failed({ binding, error, retry, current }: LoadFailure & { current: () => boolean }) {
  const t = useT();
  const ref = useRef<HTMLElement>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    let last = 0;
    const layout = bodyLayout(binding, el, () => current() && last > 0);
    const measure = () => {
      const px = Math.ceil(el.getBoundingClientRect().height);
      if (px === last) return;
      last = px;
      if (px > 0) layout.height(px);
    };
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    measure();
    return () => {
      ro.disconnect();
      layout.stop();
    };
  }, [binding, current]);
  return (
    <main ref={ref} className="flex items-center gap-2 p-3 text-xs text-muted-foreground">
      <span>{t("error.unknown", error)}</span>
      <Button variant="ghost" size="icon-sm" aria-label={t("common.retry")} onClick={retry}>
        <RiRefreshLine />
      </Button>
    </main>
  );
}

/** Whether the page's appearance is dark, following it. */
function useSystemDark(): boolean {
  const query = "(prefers-color-scheme: dark)";
  const [dark, setDark] = useState(
    () => typeof matchMedia === "function" && matchMedia(query).matches,
  );
  useEffect(() => {
    if (typeof matchMedia !== "function") return;
    const mql = matchMedia(query);
    const update = () => setDark(mql.matches);
    mql.addEventListener("change", update);
    return () => mql.removeEventListener("change", update);
  }, []);
  return dark;
}

/**
 * A body in a sandboxed iframe (the srcdoc's own CSP first, which allows no script: Go strips them
 * too). allow-scripts is for WebKit, which does not fire a listener on a document whose frame may
 * not run scripts, even one the page added: without it no click or key reached the page
 * (and loading="lazy" is ignored). The page wires it on each
 * load: its height (to Go and to the iframe, so nothing is clipped), ⌘[ ⌘] to Go, links
 * to OpenLink. Remote images are godwit-img: URLs the frame loads itself, from Go's image proxy.
 * The wheel is not its own: the web view scrolls the native reader around it (ScrollToContainer),
 * as over any native element, so a notch glides and a touchpad's moves follow.
 */
type QuoteTip = { x: number; y: number; arrowOffset: number; open: boolean };

export function Body({
  body,
  theme,
  current,
}: {
  body: ReaderBody;
  theme: Theme;
  current: () => boolean;
}) {
  const systemDark = useSystemDark();
  // Every update gets a fresh document marker, so a retry also reloads identical HTML and images.
  const { srcdoc, marker } = useMemo(() => {
    const marker = `godwit-reader-${++nextDocument}`;
    return { srcdoc: `${buildSrcdoc(body, theme, systemDark)}<!--${marker}-->`, marker };
  }, [body, theme, systemDark]);
  const frame = useRef<HTMLIFrameElement>(null);
  const [tip, setTip] = useState<QuoteTip | null>(null);
  const t = useT(body.locale);
  const labels = useMemo(
    () => ({ expand: t("reader.quote.expand"), collapse: t("reader.quote.collapse") }),
    [t],
  );

  const dark = isDark(theme, systemDark);
  const fold = body.collapseQuotes && body.html !== ""; // plain text folds in its srcdoc
  const wire = useCallback(
    (f: HTMLIFrameElement) => attach(f, body.binding, current, theme, dark, labels, setTip, fold),
    [body.binding, current, theme, dark, labels, fold],
  );
  useLayoutEffect(() => {
    const f = frame.current;
    if (!f) return;
    let poll = 0;
    let wired: Document | null = null;
    let detach = ignore;
    const onLoad = () => {
      const doc = f.contentDocument;
      if (!doc || doc.lastChild?.nodeValue !== marker || doc.readyState === "loading") return false;
      if (wired !== doc) {
        detach();
        detach = wire(f);
        wired = doc;
      }
      return true;
    };
    // Images can delay load; expose the parsed document as soon as its current marker is present.
    // about:blank or the preceding document can fire load first; only our marker ends the poll.
    const ready = () => {
      if (!onLoad()) poll = requestAnimationFrame(ready);
    };
    const loaded = () => {
      if (onLoad()) cancelAnimationFrame(poll);
    };
    f.addEventListener("load", loaded);
    ready();
    return () => {
      cancelAnimationFrame(poll);
      f.removeEventListener("load", loaded);
      detach();
    };
  }, [wire, marker]);

  return (
    <>
      <iframe
        ref={frame}
        title={t("reader.body")}
        sandbox="allow-same-origin allow-scripts"
        scrolling="no"
        srcDoc={srcdoc}
        style={{
          border: 0,
          width: "100%",
          display: "block",
          borderRadius: 12,
          overflow: "hidden",
          colorScheme: frameScheme(body, theme, systemDark),
        }}
      />
      {tip && (
        <div
          role="tooltip"
          data-quote-tip=""
          className="pointer-events-none fixed z-10 inline-flex -translate-y-1/2 items-center rounded-md bg-primary px-2 py-[3px] text-xs leading-[15px] font-medium whitespace-nowrap text-primary-foreground shadow-[0_1px_4px_rgb(0_0_0/0.18)]"
          style={{ left: tip.x, top: tip.y }}
        >
          <span
            className="absolute top-1/2 -left-[3px] size-1.5 -translate-y-1/2 rotate-45 rounded-[1px] bg-primary"
            style={tip.arrowOffset ? { marginTop: tip.arrowOffset } : undefined}
          />
          <span className="relative">{tip.open ? labels.collapse : labels.expand}</span>
        </div>
      )}
    </>
  );
}

/** Wires a loaded body document; returns the unwiring. */
function attach(
  frame: HTMLIFrameElement,
  binding: number,
  current: () => boolean,
  theme: Theme,
  darkCanvas: boolean,
  labels: { expand: string; collapse: string },
  setTip: (tip: QuoteTip | null) => void,
  fold: boolean,
): () => void {
  const doc = frame.contentDocument;
  const win = frame.contentWindow;
  const wrap = doc?.getElementById("godwit-body");
  if (!doc || !win || !wrap) return ignore; // the initial about:blank
  // On the rendered document, where the mail's stylesheet applies; before the dark pass reads the
  // light baseline and before the first height. godwit-auto only hides the body until that dark
  // pass, which drops it first thing in this same task: the fold must see the text it hides.
  if (fold) {
    wrap.classList.remove("godwit-auto");
    foldQuotes(wrap);
  }
  const offs: (() => void)[] = [adaptDarkMode(frame, theme)];
  const on = <E extends Event>(
    t: EventTarget,
    type: string,
    f: (e: E) => void,
    o?: AddEventListenerOptions,
  ) => {
    t.addEventListener(type, f as EventListener, o);
    offs.push(() => t.removeEventListener(type, f as EventListener, o));
  };

  // Quoted-text fold controls: sync accessible labels and contrast on custom-background mail
  // surfaces (including responsive resize updates).
  // The colour each control was last given, so an unchanged surface writes (and recalcs) nothing.
  const tones = new WeakMap<HTMLElement, string>();
  const syncFoldControls = () => {
    for (const fold of doc.querySelectorAll<HTMLDetailsElement>("details.godwit-quote-fold")) {
      const summary = fold.querySelector<HTMLElement>("summary");
      const rail = fold.querySelector<HTMLElement>(".godwit-quote-rail");
      summary?.setAttribute("aria-label", fold.open ? labels.collapse : labels.expand);
      const chain: HTMLElement[] = [];
      for (let p = fold.parentElement; p; p = p.parentElement) chain.unshift(p);
      let bg: Color = darkCanvas ? [10, 10, 10, 1] : [255, 255, 255, 1];
      // A backdrop not modelled here (an image, a table column's paint): leave the mail's colour.
      let unknownBackdrop = false;
      const painted = (cs: CSSStyleDeclaration) =>
        (parse(cs.backgroundColor)?.[3] ?? 0) > 0 ||
        (!!cs.backgroundImage && cs.backgroundImage !== "none");
      for (const el of chain) {
        // A cell's columns paint under it, as dark.ts protects them.
        if (
          (el.tagName === "TD" || el.tagName === "TH") &&
          [...(el.closest("table")?.querySelectorAll("col, colgroup") ?? [])].some((c) =>
            painted(win.getComputedStyle(c)),
          )
        )
          unknownBackdrop = true;
        const cs = win.getComputedStyle(el);
        const c = parse(cs.backgroundColor);
        if (c && c[3] > 0) {
          if (c[3] >= 1) unknownBackdrop = false; // an opaque colour covers what is under it
          bg = over(c, bg);
        }
        if (cs.backgroundImage && cs.backgroundImage !== "none") unknownBackdrop = true;
      }
      // The fold carries the controls' inherited colour; only summary and rail get overrides.
      const fg = unknownBackdrop ? null : parse(win.getComputedStyle(fold).color);
      let tone = "";
      if (fg) {
        const bgL = brightness(bg);
        const fgL = brightness(over(fg, bg)); // a translucent colour shows as much of the surface
        tone = bgL < 110 && fgL < 140 ? "#fafafa" : bgL > 145 && fgL > 160 ? "#0a0a0a" : "";
        // Whatever the surface, a colour too close to it (a transparent one) gets a readable one.
        if (!tone && Math.abs(bgL - fgL) < 60) tone = bgL < 128 ? "#fafafa" : "#0a0a0a";
      }
      for (const el of [summary, rail]) {
        if (el && tones.get(el) !== tone) {
          tones.set(el, tone);
          el.style.color = tone;
        }
      }
    }
  };
  syncFoldControls();

  // Height: the wrapper's own (not scrollHeight, which never shrinks below the viewport), applied
  // to the iframe so nothing is clipped. One call per frame, when it changed, clamped to Go's limit.
  // Mail sized by the viewport (vh) grows with the iframe: after the page resizes the iframe by Δ, a
  // change of ≈Δ with no content change since (an image, the width) is that echo, so the last
  // height holds instead of chasing it.
  let last = -1;
  let echo = 0; // the iframe's last change made by the page
  let changed = true; // the content changed since then
  let queued = 0;
  const layout = bodyLayout(binding, wrap, () => current() && frame.contentDocument === doc);
  const measure = () => {
    syncFoldControls();
    const cs = win.getComputedStyle(doc.body);
    const ws = win.getComputedStyle(wrap);
    const extra = [
      cs.marginTop,
      cs.marginBottom,
      cs.paddingTop,
      cs.paddingBottom,
      ws.marginTop,
      ws.marginBottom,
    ].reduce((a, v) => a + (Number.parseFloat(v) || 0), 0);
    const px = Math.min(MAX_HEIGHT, Math.ceil(wrap.getBoundingClientRect().height + extra));
    if (px === last) return;
    if (!changed && echo !== 0 && Math.abs(px - last - echo) <= 1) return;
    changed = false;
    echo = px - frame.getBoundingClientRect().height;
    last = px;
    frame.style.height = `${px}px`;
    layout.height(px);
  };
  const contentChanged = () => {
    changed = true;
    schedule();
  };
  const schedule = () => {
    if (queued) return;
    queued = requestAnimationFrame(() => {
      queued = 0;
      measure();
    });
  };
  const ro = new ResizeObserver(schedule);
  ro.observe(wrap);
  offs.push(() => ro.disconnect());
  let lastWidth = frame.getBoundingClientRect().width || 0;
  // Setting the iframe and placing the native WebView both fire resize. Only a width change
  // defeats the vh echo guard; either resize can change fold-control contrast.
  const resized = () => {
    const w = frame.getBoundingClientRect().width || 0;
    if (w !== lastWidth) {
      lastWidth = w;
      contentChanged();
    } else {
      schedule();
    }
  };
  on(window, "resize", resized);
  on(win, "resize", resized);
  for (const img of doc.images) {
    on(img, "load", contentChanged);
    on(img, "error", contentChanged);
  }
  schedule();
  // Keep the 21 px tooltip inside the tight-fitted native WebView viewport at both edges.
  const placeTip = (x: number, anchorY: number, open: boolean) => {
    const vh = window.innerHeight;
    const y = vh >= 28 ? Math.max(12, Math.min(vh - 13, anchorY)) : anchorY;
    const arrowOffset = Math.max(-5, Math.min(5, Math.round(anchorY - y)));
    setTip({ x: Math.round(x), y: Math.round(y), arrowOffset, open });
  };
  const showPillTip = (pill: Element) => {
    const f = frame.getBoundingClientRect();
    const r = pill.getBoundingClientRect();
    placeTip(f.left + r.right + 7, f.top + r.top + r.height / 2, !!pill.closest("details")?.open);
  };
  const showRailTip = (rail: Element, clientY: number) => {
    const f = frame.getBoundingClientRect();
    const r = rail.getBoundingClientRect();
    const cy =
      r.height > 24 ? Math.max(r.top + 11, Math.min(r.bottom - 11, clientY)) : r.top + r.height / 2;
    placeTip(f.left + r.right + 4, f.top + cy, true);
  };
  on(
    doc,
    "toggle",
    () => {
      syncFoldControls();
      setTip(null);
      contentChanged();
    },
    { capture: true },
  );
  on<PointerEvent>(doc, "pointerover", (e) => {
    const el = e.target as Element | null;
    const pill = el?.closest?.(".godwit-quote-pill");
    if (pill) return showPillTip(pill);
    const rail = el?.closest?.(".godwit-quote-rail");
    if (rail) showRailTip(rail, e.clientY);
  });
  on<PointerEvent>(doc, "pointermove", (e) => {
    const rail = (e.target as Element | null)?.closest?.(".godwit-quote-rail");
    if (rail) showRailTip(rail, e.clientY);
  });
  on<PointerEvent>(doc, "pointerout", (e) => {
    const from = (e.target as Element | null)?.closest?.(".godwit-quote-pill, .godwit-quote-rail");
    if (!from) return;
    const to = e.relatedTarget as Node | null;
    if (!to || !from.contains(to)) setTip(null);
  });
  on(doc.documentElement, "pointerleave", () => setTip(null));
  on(doc, "pointerdown", () => setTip(null));
  on<FocusEvent>(doc, "focusin", (e) => {
    const summary = (e.target as Element | null)?.closest?.("details.godwit-quote-fold > summary");
    const pill = summary?.querySelector(".godwit-quote-pill");
    if (pill) showPillTip(pill);
  });
  on(doc, "focusout", () => setTip(null));

  // Printing: expand closed quote folds and resize the iframe synchronously so printed/PDF output
  // includes the full message, then restore collapsed state afterwards.
  let printFolds: HTMLDetailsElement[] | null = null;
  const enterPrint = () => {
    if (printFolds) return;
    setTip(null);
    printFolds = [];
    for (const fold of doc.querySelectorAll<HTMLDetailsElement>("details.godwit-quote-fold")) {
      if (!fold.open) {
        printFolds.push(fold);
        fold.open = true;
      }
    }
    if (printFolds.length > 0) {
      changed = true;
      measure();
    }
  };
  const exitPrint = () => {
    if (!printFolds) return;
    const closed = printFolds;
    printFolds = null;
    for (const fold of closed) fold.open = false;
    if (closed.length > 0) {
      changed = true;
      measure();
    }
  };
  for (const w of [window, win]) {
    on(w, "beforeprint", enterPrint);
    on(w, "afterprint", exitPrint);
    if (typeof w.matchMedia === "function") {
      const mql = w.matchMedia("print");
      if (typeof mql?.addEventListener === "function") {
        on<MediaQueryListEvent>(mql, "change", (e) => (e.matches ? enterPrint() : exitPrint()));
      }
    }
  }

  // Links: data-href only (Go's sanitizer); nothing else navigates.
  const linkOf = (t: EventTarget | null) =>
    (t as Element | null)?.closest?.("[data-href]")?.getAttribute("data-href") ?? null;
  for (const el of doc.querySelectorAll("[data-href]")) {
    el.setAttribute("tabindex", "0");
    el.setAttribute("role", "link");
  }
  const open = (href: string) => void api.openLink(binding, href).catch(ignore);
  on<MouseEvent>(doc, "click", (e) => {
    const rail = (e.target as Element | null)?.closest?.(".godwit-quote-rail");
    if (rail) {
      const fold = rail.closest("details");
      if (fold) fold.open = false;
      setTip(null);
      return;
    }
    if ((e.target as Element | null)?.closest?.("[data-godwit-ui]")) return;
    const href = linkOf(e.target);
    if (href !== null) open(href);
    if (href !== null || (e.target as Element | null)?.closest?.("a,area")) e.preventDefault();
  });
  on<KeyboardEvent>(doc, "keydown", (e) => {
    if (e.key !== "Enter" || e.isComposing) return;
    if ((e.target as Element | null)?.closest?.("[data-godwit-ui]")) return;
    const href = linkOf(e.target);
    if (href === null) return;
    e.preventDefault();
    open(href);
  });

  offs.push(readerShortcuts(binding, win));

  return () => {
    cancelAnimationFrame(queued);
    layout.stop();
    setTip(null);
    for (const off of offs) off();
  };
}
