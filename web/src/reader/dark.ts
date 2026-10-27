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

function gradient(value: string, backdrop: Color): Color | null {
  // shortcut: single gradients with percentage stops only; preserve other layers until supported.
  const match = /^(linear|radial)-gradient\((.+)\)$/i.exec(value);
  if (!match) return null;
  const stops = match[2].split(/,(?![^()]*\))/).map((part) => part.trim());
  const direction =
    match[1] === "linear"
      ? /^(?:-?[\d.]+(?:deg|grad|rad|turn)|to (?:left|right|top|bottom)(?: (?:left|right|top|bottom))?)$/i
      : /^(?:circle|ellipse)(?: at (?:center|[\d.]+% [\d.]+%))?$/i;
  if (direction.test(stops[0])) stops.shift();
  if (stops.length < 2) return null;
  const colors: Color[] = [];
  const positions: number[] = [];
  for (const stop of stops) {
    const token = /^(rgba?\([^()]+\)|#[\da-f]{3,8}|transparent)(?:\s+([\d.]+)%|\s+(0))?$/i.exec(
      stop,
    );
    if (!token) return null;
    const color = parse(token[1]);
    if (!color) return null;
    colors.push(over(color, backdrop));
    positions.push(token[2] ? Number(token[2]) / 100 : token[3] ? 0 : Number.NaN);
  }
  if (Number.isNaN(positions[0])) positions[0] = 0;
  if (Number.isNaN(positions[positions.length - 1])) positions[positions.length - 1] = 1;
  let anchor = 0;
  for (let i = 1; i < positions.length; i++) {
    if (Number.isNaN(positions[i])) continue;
    if (positions[i] < positions[anchor] || positions[i] > 1) return null;
    for (let j = anchor + 1; j < i; j++)
      positions[j] =
        positions[anchor] + ((positions[i] - positions[anchor]) * (j - anchor)) / (i - anchor);
    anchor = i;
  }
  const result: Color = [0, 0, 0, 1];
  for (let i = 0; i < colors.length; i++) {
    const before = i === 0 ? positions[i] : (positions[i] - positions[i - 1]) / 2;
    const after =
      i === colors.length - 1 ? 1 - positions[i] : (positions[i + 1] - positions[i]) / 2;
    for (let channel = 0; channel < 3; channel++)
      result[channel] += colors[i][channel] * (before + after);
  }
  return result;
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
  hasText: boolean;
  needsBackdrop: boolean;
  gradientOwner: HTMLElement | null;
  edges: { property: string; color: Color; outline: boolean }[];
  shadows: { property: string; value: string }[];
};

function textPseudoRegions(root: HTMLElement): Set<HTMLElement> {
  const regions = new Set<HTMLElement>();
  const doc = root.ownerDocument;
  const visit = (rules: CSSRuleList) => {
    for (const rule of rules) {
      if ("cssRules" in rule) {
        if (
          rule.type === CSSRule.MEDIA_RULE &&
          doc.defaultView?.matchMedia &&
          !doc.defaultView.matchMedia((rule as CSSMediaRule).conditionText).matches
        )
          continue;
        visit((rule as CSSGroupingRule).cssRules);
      }
      if (!("selectorText" in rule)) continue;
      const selector = (rule as CSSStyleRule).selectorText;
      let start = 0;
      let depth = 0;
      // Keep selector-list commas, quoted attributes and functional selectors distinct.
      for (const token of selector.matchAll(
        /\\.|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[()[\],]|::?first-(?:line|letter)\b|::marker\b/gi,
      )) {
        const value = token[0];
        if (value === "(" || value === "[") depth++;
        else if (value === ")" || value === "]") depth--;
        else if (depth === 0 && value === ",") start = token.index + 1;
        else if (depth === 0 && value.startsWith(":")) {
          let owner = selector.slice(start, token.index);
          if (!owner || /[\s>+~]$/.test(owner)) owner += "*";
          for (const el of doc.querySelectorAll<HTMLElement>(owner)) {
            // Outside markers can paint in the list's margin, beyond its own background.
            const region =
              value.toLowerCase() === "::marker"
                ? (el.closest("ul,ol")?.parentElement ?? el.parentElement ?? root)
                : el;
            if (root.contains(region)) regions.add(region);
          }
        }
      }
    }
  };
  for (const sheet of doc.styleSheets) visit(sheet.cssRules);
  return regions;
}

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

  const run = (preserved = new Set<HTMLElement>(), retry = true) => {
    restore();
    root.classList.remove("godwit-auto");
    doc.documentElement.style.colorScheme = "light";
    try {
      const snapshots = new Map<HTMLElement, Snapshot>();
      // shortcut: styled text pseudos keep their complete region until pseudo colours are mapped.
      const textRegions = textPseudoRegions(root);
      let retryNeeded = false;
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
          protected: textRegions.has(el),
          mediaProtected: false,
          visible,
          opacity,
          ratio:
            size >= 24 || (size >= 18.66 && Number.parseInt(computed.fontWeight, 10) >= 700)
              ? 3
              : 4.5,
          image: computed.backgroundImage,
          hasText:
            visible &&
            opacity > 0 &&
            fg[3] > 0 &&
            size !== 0 &&
            [...el.childNodes].some((child) => child.nodeType === 3 && child.textContent?.trim()),
          needsBackdrop: preserved.has(el),
          gradientOwner: null,
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
        // shortcut: column paint is outside cell ancestry; preserve its table until modelled.
        if (
          visible &&
          opacity > 0 &&
          (el.tagName === "COL" || el.tagName === "COLGROUP") &&
          (bg[3] > 0 || (computed.backgroundImage && computed.backgroundImage !== "none"))
        ) {
          const table = snapshots.get(el.closest("table") as HTMLElement);
          if (table) table.protected = true;
        }
      }
      for (const node of [...snapshots.values()].reverse()) {
        const parent = snapshots.get(node.el.parentElement as HTMLElement);
        if (parent && node.hasText) parent.hasText = true;
        // Preserved artwork needs its original gradient backdrop up to an opaque boundary.
        if (
          parent &&
          node.visible &&
          node.opacity > 0 &&
          node.bg[3] < 1 &&
          (node.needsBackdrop ||
            node.protected ||
            node.el.tagName === "IMG" ||
            (node.image &&
              node.image !== "none" &&
              (!node.hasText || !gradient(node.image, node.source))))
        )
          parent.needsBackdrop = true;
      }
      for (const node of snapshots.values()) {
        const parent = snapshots.get(node.el.parentElement as HTMLElement);
        if (!node.visible || node.opacity === 0) continue;
        const fixedProtection =
          node.protected ||
          (parent?.protected === true && !parent.mediaProtected) ||
          node.opacity < 1;
        const source = over(node.bg, parent?.source ?? light);
        const flattened =
          !fixedProtection &&
          !node.needsBackdrop &&
          !(parent?.mediaProtected && node.bg[3] < 1) &&
          node.hasText
            ? gradient(node.image, source)
            : null;
        node.source = flattened ?? source;
        node.gradientOwner = flattened
          ? node.el
          : node.bg[3] === 1
            ? null
            : (parent?.gradientOwner ?? null);
        const ownBackground = node.el === root || node.bg[3] > 0 || !!flattened;
        const mapped = mix(dark, surface, clamp((255 - brightness(node.source)) / 32));
        node.target = ownBackground
          ? brightness(node.source) < 100
            ? node.source
            : neutral(node.source)
              ? mapped
              : withLightness(node.source, lightness(mapped))
          : (parent?.target ?? dark);
        // An opaque fill covers ancestor artwork; translucent descendants still need its pairing.
        node.mediaProtected =
          !fixedProtection &&
          ((!!node.image && node.image !== "none" && !flattened) ||
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
          if (contrast(text, node.target) < node.ratio) {
            node.protected = true;
            if (node.gradientOwner) {
              preserved.add(node.gradientOwner);
              retryNeeded = true;
            }
            if (flattened) {
              node.source = source;
              node.mediaProtected = true;
            }
          }
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
          if (flattened) setValue(node.el, "background-image", "none");
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
      // A failed descendant needs the original artwork; one bounded retry restores its region.
      if (retry && retryNeeded) run(preserved, false);
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
