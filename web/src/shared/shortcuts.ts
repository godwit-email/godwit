/**
 * Forwards ⌘[ / ⌘] to navigate(-1 / 1): native shortcuts miss keys while an embedded page has the
 * keyboard. Not while an IME composes. Returns the remover.
 */
export function forwardShortcuts(navigate: (dir: number) => Promise<unknown>): () => void {
  const on = (e: KeyboardEvent) => {
    if (!e.metaKey || e.ctrlKey || e.altKey || e.shiftKey || e.isComposing) return;
    const dir = e.key === "[" ? -1 : e.key === "]" ? 1 : 0;
    if (!dir) return;
    e.preventDefault();
    navigate(dir).catch(() => {}); // a refused navigation leaves the page as it is
  };
  window.addEventListener("keydown", on);
  return () => window.removeEventListener("keydown", on);
}
