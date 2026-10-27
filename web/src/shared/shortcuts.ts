/**
 * Forwards ⌘[ / ⌘] to navigate(-1 / 1) (and ⌘F to search, ⌘S to save, ⌘P to print, when given): native shortcuts miss keys while an embedded page has the
 * keyboard. Not while an IME composes. Listens on win (default this window; the reader also
 * passes its body's iframe window). Returns the remover.
 */
export function forwardShortcuts(
  navigate: (dir: number) => Promise<unknown>,
  win: Window = window,
  search?: () => Promise<unknown>,
  save?: () => Promise<unknown>,
  print?: () => Promise<unknown>,
): () => void {
  const on = (e: KeyboardEvent) => {
    if (!e.metaKey || e.ctrlKey || e.altKey || e.shiftKey || e.isComposing) return;
    if (search && e.key.toLowerCase() === "f") {
      e.preventDefault();
      search().catch(() => {});
      return;
    }
    if (save && e.key.toLowerCase() === "s") {
      e.preventDefault();
      save().catch(() => {});
      return;
    }
    if (print && e.key.toLowerCase() === "p") {
      e.preventDefault();
      print().catch(() => {});
      return;
    }
    const dir = e.key === "[" ? -1 : e.key === "]" ? 1 : 0;
    if (!dir) return;
    e.preventDefault();
    navigate(dir).catch(() => {}); // a refused navigation leaves the page as it is
  };
  win.addEventListener("keydown", on);
  return () => win.removeEventListener("keydown", on);
}
