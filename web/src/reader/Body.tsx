import { RiRefreshLine } from "@remixicon/react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { ReaderPage as api, events, type ReaderBody, type ReaderGlass, type Theme } from "@/mygo";
import { useT } from "@/shared/i18n";
import { forwardShortcuts } from "@/shared/shortcuts";
import { applyTheme, type Theme as PageTheme } from "@/shared/theme";
import { adaptDarkMode } from "./dark";
import { buildSrcdoc, frameScheme } from "./frame";
import { wheelForwarder } from "./wheel";

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
    <main ref={ref} className="flex items-center gap-2 p-3 text-sm text-muted-foreground">
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
 * not run scripts, even one the page added: without it no click, wheel or key reached the page
 * (and loading="lazy" is ignored). The page wires it on each
 * load: its height (to Go and to the iframe, so nothing is clipped), wheels and ⌘[ ⌘] to Go, links
 * to OpenLink. Remote images are godwit-img: URLs the frame loads itself, from Go's image proxy.
 */
export function Body({ body, theme }: { body: ReaderBody; theme: Theme }) {
  const systemDark = useSystemDark();
  // Every update gets a fresh document marker, so a retry also reloads identical HTML and images.
  const { srcdoc, marker } = useMemo(() => {
    const marker = `godwit-reader-${++nextDocument}`;
    return { srcdoc: `${buildSrcdoc(body, theme, systemDark)}<!--${marker}-->`, marker };
  }, [body, theme, systemDark]);
  const frame = useRef<HTMLIFrameElement>(null);
  const t = useT(body.locale);

  // One forwarder for both windows, so a frame's wheels are summed once.
  const wheel = useMemo(() => wheelForwarder((dy) => void api.wheel(dy).catch(ignore)), []);
  useEffect(() => {
    addEventListener("wheel", wheel, { passive: false });
    return () => removeEventListener("wheel", wheel);
  }, [wheel]);

  const wire = useCallback((f: HTMLIFrameElement) => attach(f, wheel, theme), [wheel, theme]);
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
  );
}

/** Wires a loaded body document; returns the unwiring. */
function attach(
  frame: HTMLIFrameElement,
  wheel: (e: WheelEvent) => void,
  theme: Theme,
): () => void {
  const doc = frame.contentDocument;
  const win = frame.contentWindow;
  const wrap = doc?.getElementById("godwit-body");
  if (!doc || !win || !wrap) return ignore; // the initial about:blank
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
  on(window, "resize", contentChanged);
  for (const img of doc.images) {
    on(img, "load", contentChanged);
    on(img, "error", contentChanged);
  }
  schedule();

  // Links: data-href only (Go's sanitizer); nothing else navigates.
  const linkOf = (t: EventTarget | null) =>
    (t as Element | null)?.closest?.("[data-href]")?.getAttribute("data-href") ?? null;
  for (const el of doc.querySelectorAll("[data-href]")) {
    el.setAttribute("tabindex", "0");
    el.setAttribute("role", "link");
  }
  const open = (href: string) => void api.openLink(href).catch(ignore);
  on<MouseEvent>(doc, "click", (e) => {
    const href = linkOf(e.target);
    if (href !== null) open(href);
    if (href !== null || (e.target as Element | null)?.closest?.("a,area")) e.preventDefault();
  });
  on<KeyboardEvent>(doc, "keydown", (e) => {
    if (e.key !== "Enter" || e.isComposing) return;
    const href = linkOf(e.target);
    if (href === null) return;
    e.preventDefault();
    open(href);
  });

  on(win, "wheel", wheel, { passive: false });
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
    for (const off of offs) off();
  };
}
