import type { ComposeEdit, SaveResult } from "@/mygo";

export type SaveStatus = "saved" | "saving" | "unsaved" | "conflict";

export interface Saver {
  edit(e: ComposeEdit): void;
  setComposing(on: boolean): void;
  /** Saves any pending edit now, even while composing; resolves once nothing is left to save or a save failed. */
  flush(): Promise<void>;
  readonly status: SaveStatus;
  onChange(fn: () => void): () => void;
  /** Cancels the timer and blocks any further scheduling. */
  dispose(): void;
}

/** Saves the latest edit `delay` ms after the last one: never while composing, one save in flight, none after a conflict. */
export function createSaver(
  save: (e: ComposeEdit) => Promise<SaveResult>,
  opts: { delay?: number } = {},
): Saver {
  const delay = opts.delay ?? 1000;
  let status: SaveStatus = "saved";
  let pending: ComposeEdit | undefined;
  let composing = false;
  let lastInput = 0;
  let disposed = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let inflight: Promise<boolean> | undefined;
  const listeners = new Set<() => void>();

  const set = (s: SaveStatus) => {
    status = s;
    for (const f of listeners) f();
  };
  const schedule = () => {
    clearTimeout(timer);
    if (disposed || composing || status === "conflict") return;
    // Only what is left of the delay since the last input.
    timer = setTimeout(() => void run(), Math.max(0, delay - (Date.now() - lastInput)));
  };
  // run saves the pending edit; it resolves true unless the save failed.
  const run = async (): Promise<boolean> => {
    clearTimeout(timer);
    if (inflight) return inflight;
    const e = pending;
    if (!e || status === "conflict") return true;
    pending = undefined;
    set("saving");
    inflight = save(e)
      .then((r) => {
        if (r.conflict) {
          pending = undefined;
          set("conflict");
        } else set(pending ? "unsaved" : "saved");
        return true;
      })
      .catch(() => {
        pending ??= e; // a newer edit supersedes the failed one
        set("unsaved");
        return false;
      })
      .finally(() => {
        inflight = undefined;
      });
    const ok = await inflight;
    if (ok && pending) schedule(); // edits arrived while saving
    return ok;
  };

  return {
    edit(e) {
      if (status === "conflict") return;
      pending = e;
      lastInput = Date.now();
      if (status !== "saving") set("unsaved");
      schedule();
    },
    setComposing(on) {
      composing = on;
      if (on) clearTimeout(timer);
      else if (pending) {
        lastInput = Date.now(); // the composition's end is input too
        schedule();
      }
    },
    async flush() {
      clearTimeout(timer);
      while (inflight || (pending && status !== "conflict")) if (!(await run())) return;
    },
    get status() {
      return status;
    },
    dispose() {
      disposed = true;
      clearTimeout(timer);
    },
    onChange(fn) {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
  };
}
