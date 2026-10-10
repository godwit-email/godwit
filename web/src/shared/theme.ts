// Theme mirrors webui.Theme (Go): the native tokens as shadcn's CSS variables.
export type Theme = {
  mode: "system" | "light" | "dark";
  light: Record<string, string>;
  dark: Record<string, string>;
  fontSize: number;
};

/** Sets the theme's variables on root and returns a cleanup that stops following the system. */
export function applyTheme(t: Theme, root: HTMLElement = document.documentElement): () => void {
  const set = (dark: boolean) => {
    // Clear both maps first: a key only the other appearance sets must not linger.
    for (const k of [...Object.keys(t.light), ...Object.keys(t.dark)]) root.style.removeProperty(k);
    for (const [k, v] of Object.entries(dark ? t.dark : t.light)) root.style.setProperty(k, v);
    root.classList.toggle("dark", dark);
  };
  root.style.fontSize = `${t.fontSize}px`;
  if (t.mode !== "system") {
    set(t.mode === "dark");
    return () => {};
  }
  const mql = matchMedia("(prefers-color-scheme: dark)");
  const update = () => set(mql.matches);
  update();
  mql.addEventListener("change", update);
  return () => mql.removeEventListener("change", update);
}
