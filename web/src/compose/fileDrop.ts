import { type FileDrop, isMyGo, onFileDrop } from "mygo-runtime";
import { useEffect, useRef, useState } from "react";

/**
 * Whether files from the file manager are being dragged over the page; each drop goes to onDrop
 * with the files' paths, which only MyGo tells (mygo-runtime's onFileDrop), DOM drops do not.
 * The page's own drag and drop is left alone: nothing here cancels or stops a DOM event, so the
 * editor still gets its drops. While !enabled no drag shows and drops are ignored.
 */
export function useFileDrop(enabled: boolean, onDrop: (drop: FileDrop) => void): boolean {
  const [over, setOver] = useState(false);
  const latest = useRef(onDrop);
  useEffect(() => {
    latest.current = onDrop;
  });
  useEffect(() => {
    setOver(false);
    if (!enabled) return;
    let depth = 0; // dragenter/dragleave pairs, one per element crossed
    let inner = false; // a drag that started in the page carries no files of the file manager's
    const files = (e: DragEvent) =>
      !inner && Array.from(e.dataTransfer?.types ?? []).includes("Files");
    const reset = () => {
      depth = 0;
      setOver(false);
    };
    const on: [string, (e: DragEvent) => void][] = [
      ["dragstart", () => (inner = true)],
      ["dragend", () => (inner = false)],
      ["dragenter", (e) => files(e) && ++depth && setOver(true)],
      ["dragleave", (e) => files(e) && --depth <= 0 && reset()],
      ["drop", reset],
    ];
    // Capture: a drop handler that stops propagation (the editor's) still ends the drag here.
    for (const [type, f] of on) window.addEventListener(type, f as EventListener, true);
    const off = isMyGo() ? onFileDrop((d) => latest.current(d)) : () => {};
    return () => {
      for (const [type, f] of on) window.removeEventListener(type, f as EventListener, true);
      off();
    };
  }, [enabled]);
  return over;
}
