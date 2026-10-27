import type { Theme } from "@/mygo";

type Color = [number, number, number, number];
const clamp = (n: number, min = 0, max = 1) => Math.max(min, Math.min(max, n));
const rounded = (c: Color): Color => [Math.round(c[0]), Math.round(c[1]), Math.round(c[2]), c[3]];
const css = (c: Color) => `rgba(${c.slice(0, 3).map(Math.round).join(",")},${c[3]})`;
const brightness = (c: Color) => (c[0] * 299 + c[1] * 587 + c[2] * 114) / 1000;
const neutral = (c: Color) => Math.max(...c.slice(0, 3)) - Math.min(...c.slice(0, 3)) <= 12;
const mix = (a: Color, b: Color, t: number): Color => [
  a[0] + (b[0] - a[0]) * t,
  a[1] + (b[1] - a[1]) * t,
  a[2] + (b[2] - a[2]) * t,
  1,
];
const over = (front: Color, back: Color) => mix(back, front, front[3]);

function parse(value: string): Color | null {
  if (value === "transparent") return [0, 0, 0, 0];
  const hex = /^#([\da-f]{3,8})$/i.exec(value)?.[1];
  if (hex && [3, 4, 6, 8].includes(hex.length)) {
    const h = hex.length < 5 ? [...hex].map((c) => c + c).join("") : hex;
    return [
      Number.parseInt(h.slice(0, 2), 16),
      Number.parseInt(h.slice(2, 4), 16),
      Number.parseInt(h.slice(4, 6), 16),
      h.length === 8 ? Number.parseInt(h.slice(6, 8), 16) / 255 : 1,
    ];
  }
  if (!/^rgba?\(/i.test(value)) return null;
  const parts = value
    .slice(value.indexOf("(") + 1, -1)
    .split(/[\s,/]+/)
    .filter(Boolean);
  if (parts.length < 3 || parts.length > 4) return null;
  const values = parts.map(
    (part, i) => Number.parseFloat(part) * (part.endsWith("%") ? (i === 3 ? 0.01 : 2.55) : 1),
  );
  if (values.some((n) => !Number.isFinite(n))) return null;
  return [
    clamp(values[0], 0, 255),
    clamp(values[1], 0, 255),
    clamp(values[2], 0, 255),
    clamp(values[3] ?? 1),
  ];
}

function luminance(c: Color): number {
  const linear = c.slice(0, 3).map((v) => {
    const s = v / 255;
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722;
}

function contrast(fg: Color, bg: Color): number {
  const a = luminance(over(fg, bg));
  const b = luminance(bg);
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

const lightness = (c: Color) => (Math.max(...c.slice(0, 3)) + Math.min(...c.slice(0, 3))) / 510;

function withLightness(c: Color, value: number): Color {
  const min = Math.min(...c.slice(0, 3)) / 255;
  const max = Math.max(...c.slice(0, 3)) / 255;
  const chroma = max - min;
  const saturation = chroma === 0 ? 0 : chroma / (1 - Math.abs(max + min - 1));
  const nextChroma = (1 - Math.abs(2 * value - 1)) * saturation;
  const channel = (v: number) =>
    255 * (value - nextChroma / 2 + (chroma === 0 ? 0 : ((v / 255 - min) * nextChroma) / chroma));
  return rounded([channel(c[0]), channel(c[1]), channel(c[2]), c[3]]);
}

function readable(fg: Color, bg: Color, ratio: number): Color | null {
  fg = rounded(fg);
  bg = rounded(bg);
  if (contrast(fg, bg) >= ratio) return fg;
  const start = lightness(fg);
  let result: Color | null = null;
  for (const end of [0, 1]) {
    if (contrast(withLightness(fg, end), bg) < ratio) continue;
    let low = 0;
    let high = 1;
    for (let i = 0; i < 12; i++) {
      const middle = (low + high) / 2;
      const candidate = withLightness(fg, start + (end - start) * middle);
      if (contrast(candidate, bg) >= ratio) high = middle;
      else low = middle;
    }
    const candidate = withLightness(fg, start + (end - start) * high);
    if (!result || Math.abs(lightness(candidate) - start) < Math.abs(lightness(result) - start))
      result = candidate;
  }
  return result;
}

function shadow(value: string, fg: Color, bg: Color): string | null {
  if (!value || value === "none") return null;
  const shadows: string[] = [];
  for (const part of value.split(/,(?![^()]*\))/)) {
    const token = part.match(/rgba?\([^)]*\)|#[\da-f]{3,8}\b|currentcolor/i)?.[0];
    const color = !token || token.toLowerCase() === "currentcolor" ? fg : parse(token);
    const geometry = (token ? part.replace(token, "") : part).trim();
    if (!color || !/^(?:inset\s+)?(?:-?(?:\d*\.)?\d+(?:px)?\s*){2,4}(?:\s+inset)?$/.test(geometry))
      return null;
    shadows.push(
      `${geometry} ${css(withLightness(color, Math.min(lightness(color), lightness(bg) / 2)))}`,
    );
  }
  return shadows.join(", ");
}

type Snapshot = {
  el: HTMLElement;
  bg: Color;
  fg: Color;
  source: Color;
  target: Color;
  protected: boolean;
  mediaProtected: boolean;
  visible: boolean;
  opacity: number;
  ratio: number;
  image: string;
  edges: { property: string; color: Color; outline: boolean }[];
  shadows: { property: string; value: string }[];
};

/** The trusted reader owns these temporary overrides; the message document runs no code. */
export function adaptDarkMode(frame: HTMLIFrameElement, theme: Theme): () => void {
  const doc = frame.contentDocument;
  const win = frame.contentWindow;
  const root = doc?.getElementById("godwit-body");
  if (!doc || !win || !root?.hasAttribute("data-auto-dark")) return () => {};
  const light: Color = parse(theme.light?.["--background"] ?? "") ?? [255, 255, 255, 1];
  const dark: Color = parse(theme.dark?.["--background"] ?? "") ?? [10, 10, 10, 1];
  const primary: Color = parse(theme.dark?.["--foreground"] ?? "") ?? [250, 250, 250, 1];
  const secondary: Color = parse(theme.dark?.["--muted-foreground"] ?? "") ?? [163, 163, 163, 1];
  const surface: Color = parse(theme.dark?.["--muted"] ?? "") ?? [38, 38, 38, 1];
  const lightText: Color = parse(theme.light?.["--foreground"] ?? "") ?? [10, 10, 10, 1];
  const originals = new Map<HTMLElement, string | null>();
  const setValue = (el: HTMLElement, key: string, value: string) => {
    if (!originals.has(el)) originals.set(el, el.getAttribute("style"));
    el.style.setProperty(key, value, "important");
  };
  const set = (el: HTMLElement, key: string, value: Color) => setValue(el, key, css(value));
  const restore = () => {
    for (const [el, style] of originals) {
      if (style === null) el.removeAttribute("style");
      else el.setAttribute("style", style);
    }
    originals.clear();
  };

  const run = () => {
    restore();
    root.classList.remove("godwit-auto");
    doc.documentElement.style.colorScheme = "light";
    try {
      const snapshots = new Map<HTMLElement, Snapshot>();
      // Read the entire light baseline before any overrides can affect inheritance.
      for (const el of [root, ...root.querySelectorAll<HTMLElement>("*")]) {
        if (["STYLE", "META", "LINK", "BR"].includes(el.tagName)) continue;
        const parent = snapshots.get(el.parentElement as HTMLElement);
        const computed = win.getComputedStyle(el);
        const bg = parse(computed.backgroundColor || "transparent");
        const fg = parse(computed.color) ?? (el === root ? lightText : parent?.fg);
        if (!bg || !fg) throw new Error("Unsupported mail colour");
        const source = over(bg, parent?.source ?? light);
        const opacity =
          (Number.parseFloat(computed.opacity) || (computed.opacity === "0" ? 0 : 1)) *
          (parent?.opacity ?? 1);
        const size = Number.parseFloat(computed.fontSize);
        const visible =
          parent?.visible !== false &&
          computed.display !== "none" &&
          computed.visibility !== "hidden";
        const edges: Snapshot["edges"] = [];
        for (const edge of [
          "border-top",
          "border-right",
          "border-bottom",
          "border-left",
          "outline",
        ]) {
          const outline = edge === "outline";
          const style = computed.getPropertyValue(`${edge}-style`);
          const active =
            style !== "none" &&
            style !== "hidden" &&
            Number.parseFloat(computed.getPropertyValue(`${edge}-width`)) > 0;
          if (active || (outline && el.hasAttribute("data-href"))) {
            const color = (active && parse(computed.getPropertyValue(`${edge}-color`))) || fg;
            if (color[3] > 0) edges.push({ property: `${edge}-color`, color, outline });
          }
        }
        snapshots.set(el, {
          el,
          bg,
          fg,
          source,
          target: source,
          protected: false,
          mediaProtected: false,
          visible,
          opacity,
          ratio:
            size >= 24 || (size >= 18.66 && Number.parseInt(computed.fontWeight, 10) >= 700)
              ? 3
              : 4.5,
          image: computed.backgroundImage,
          edges,
          shadows: [
            { property: "box-shadow", value: computed.boxShadow },
            { property: "text-shadow", value: computed.textShadow },
          ],
        });
        // Group opacity composites against the outside backdrop, not the group's own fill.
        if (visible && opacity > 0 && opacity < 1 && (parent?.opacity ?? 1) === 1) {
          if (!parent) throw new Error("Translucent mail root");
          parent.protected = true;
        }
      }
      for (const node of snapshots.values()) {
        const parent = snapshots.get(node.el.parentElement as HTMLElement);
        if (!node.visible || node.opacity === 0) continue;
        const ownBackground = node.el === root || node.bg[3] > 0;
        const mapped = mix(dark, surface, clamp((255 - brightness(node.source)) / 32));
        node.target = ownBackground
          ? brightness(node.source) < 100
            ? node.source
            : neutral(node.source)
              ? mapped
              : withLightness(node.source, lightness(mapped))
          : (parent?.target ?? dark);
        const fixedProtection =
          node.protected ||
          (parent?.protected === true && !parent.mediaProtected) ||
          node.opacity < 1;
        // An opaque fill covers ancestor artwork; translucent descendants still need its pairing.
        node.mediaProtected =
          !fixedProtection &&
          ((!!node.image && node.image !== "none") ||
            (parent?.mediaProtected === true && node.bg[3] < 1));
        node.protected = fixedProtection || node.mediaProtected || node.el.tagName === "IMG";
        let text = node.fg;
        if (!node.protected && node.fg[3] > 0) {
          const candidate =
            brightness(node.source) < 100 || !neutral(node.fg)
              ? node.fg
              : mix(primary, secondary, clamp(brightness(node.fg) / 160));
          candidate[3] = node.fg[3];
          text = readable(candidate, node.target, node.ratio) ?? node.fg;
          if (contrast(text, node.target) < node.ratio) node.protected = true;
        }
        if (node.protected) {
          node.target = node.source;
          if (!parent?.protected) {
            set(node.el, "background-color", node.source);
            if (node.el.tagName === "IMG" && (!node.image || node.image === "none"))
              setValue(node.el, "background-clip", "content-box");
            set(node.el, "color", node.fg);
          }
        } else {
          if (ownBackground) set(node.el, "background-color", node.target);
          if (node.fg[3] > 0) set(node.el, "color", text);
          for (const edge of node.edges) {
            const backdrop = edge.outline ? (parent?.target ?? dark) : node.target;
            const ratio = edge.outline ? 3 : clamp(contrast(edge.color, node.source), 1.5, 3);
            const candidate = neutral(edge.color)
              ? withLightness(edge.color, lightness(backdrop))
              : edge.color;
            const color = readable(candidate, backdrop, ratio);
            if (color) set(node.el, edge.property, color);
          }
          for (const item of node.shadows) {
            const value = shadow(item.value, node.fg, node.target);
            if (value) setValue(node.el, item.property, value);
          }
        }
      }
    } catch {
      restore();
      set(doc.body, "background-color", light);
    } finally {
      doc.documentElement.style.colorScheme = "dark";
      root.classList.remove("godwit-auto");
    }
  };
  run();
  let live = true;
  let queued = false;
  let width = frame.getBoundingClientRect().width;
  const resized = () => {
    const next = frame.getBoundingClientRect().width;
    if (next === width) return;
    width = next;
    if (queued) return;
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      if (live) run();
    });
  };
  const observer = new ResizeObserver(resized);
  observer.observe(frame);
  window.addEventListener("resize", resized);
  win.addEventListener("resize", resized);
  return () => {
    live = false;
    observer.disconnect();
    window.removeEventListener("resize", resized);
    win.removeEventListener("resize", resized);
    restore();
  };
}
