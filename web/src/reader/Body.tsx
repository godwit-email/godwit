import { RiRefreshLine } from "@remixicon/react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { ReaderPage as api, events, type ReaderBody, type ReaderGlass, type Theme } from "@/mygo";
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

/**
 * The reader page: one expanded message's body (role "reader"), embedded in the native reader.
 *
 * Listeners first (readerBody, theme), then load: mygo drops events sent before a listener
 * exists, and only a body newer than the one shown applies (a load resolving after a readerBody is
 * dropped). A theme event wins over the theme of a body that arrives later.
 */
export function ReaderApp() {
  const [body, setBody] = useState<ReaderBody>();
  const [theme, setTheme] = useState<Theme>();
  const [failed, setFailed] = useState<string>();
  const load = useRef<() => void>(ignore);

  useEffect(() => {
    let live = true;
    let version = 0;
    let pushed = false; // a theme event came: newer than any body's theme
    const apply = (b: ReaderBody) => {
      if (!live || b.version <= version) return;
      version = b.version;
      setBody(b);
      setFailed(undefined);
      if (!pushed) setTheme(b.theme);
    };
    const offs = [
      events.readerBody.on(apply),
      events.theme.on((t) => {
        pushed = true;
        setTheme(t);
      }),
    ];
    load.current = () => {
      setFailed(undefined);
      api.load().then(apply, (e) => {
        if (live && !version) setFailed(message(e));
      });
    };
    load.current();
    return () => {
      live = false;
      for (const off of offs) off();
    };
  }, []);

  useEffect(
    () =>
      forwardShortcuts(
        (dir) => api.navigate(dir),
        window,
        () => api.search(),
        () => api.save(),
        () => api.print(),
      ),
    [],
  );
  useEffect(() => (theme ? applyTheme(theme as PageTheme) : undefined), [theme]);

  if (failed !== undefined)
    return <Failed error={failed} retry={() => load.current()} locale={body?.locale} />;
  if (!body || !theme) return <main />;
  return (
    <main>
      <Body body={body} theme={theme} />
      <Glass />
    </main>
  );
}

// The blur of the native reader's frosted bars (kit.Bar: 2× theme.BackdropBlur), which cannot see
// this page: what they cover of it blurs here, under their tint.
const blur = { backdropFilter: "blur(8px)", WebkitBackdropFilter: "blur(8px)" };

/** Blurs what the reader's bars cover of the page, from its top and its bottom, as Go tells it. */
export function Glass() {
  const [glass, setGlass] = useState<ReaderGlass>({ top: 0, bottom: 0 });
  useEffect(() => events.readerGlass.on(setGlass), []);
  return (
    <>
      {glass.top > 0 && (
        <div
          data-glass="top"
          className="pointer-events-none fixed inset-x-0 top-0"
          style={{ ...blur, height: glass.top }}
        />
      )}
      {glass.bottom > 0 && (
        <div
          data-glass="bottom"
          className="pointer-events-none fixed inset-x-0 bottom-0"
          style={{ ...blur, height: glass.bottom }}
        />
      )}
    </>
  );
}

function Failed({ error, retry, locale }: { error: string; retry: () => void; locale?: string }) {
  const t = useT(locale);
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    const px = Math.ceil(ref.current?.getBoundingClientRect().height ?? 0);
    if (px > 0) api.height(px).catch(ignore);
  }, []);
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

export function Body({ body, theme }: { body: ReaderBody; theme: Theme }) {
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
    (f: HTMLIFrameElement) => attach(f, theme, dark, labels, setTip, fold),
    [theme, dark, labels, fold],
  );
  useEffect(() => {
    const f = frame.current;
    if (!f) return;
    let polling = true;
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
    // Polling ends at the first load: from then on load events alone track the document.
    const ready = () => {
      if (polling && !onLoad()) requestAnimationFrame(ready);
    };
    const loaded = () => {
      polling = false;
      onLoad();
    };
    f.addEventListener("load", loaded);
    ready();
    return () => {
      polling = false;
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
  let live = true;
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
  let queued = false;
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
    api.height(px).catch(ignore);
  };
  const contentChanged = () => {
    changed = true;
    schedule();
  };
  const schedule = () => {
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      if (live) measure();
    });
  };
  const ro = new ResizeObserver(schedule);
  ro.observe(wrap);
  offs.push(() => ro.disconnect());
  let lastWidth = frame.getBoundingClientRect().width || 0;
  on(window, "resize", () => {
    lastWidth = frame.getBoundingClientRect().width || 0;
    contentChanged();
  });
  // Updating frame.style.height fires resize on win; keep changed=false on height-only resizes so
  // the vh echo guard holds, while still scheduling measure() to refresh fold-control contrast.
  on(win, "resize", () => {
    const w = frame.getBoundingClientRect().width || 0;
    if (w !== lastWidth) {
      lastWidth = w;
      contentChanged();
    } else {
      schedule();
    }
  });
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
  const open = (href: string) => void api.openLink(href).catch(ignore);
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

  offs.push(
    forwardShortcuts(
      (dir) => api.navigate(dir),
      win,
      () => api.search(),
      () => api.save(),
      () => api.print(),
    ),
  );

  return () => {
    live = false;
    setTip(null);
    for (const off of offs) off();
  };
}
