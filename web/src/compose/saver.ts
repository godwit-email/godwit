import type { ComposeEdit, SaveResult } from "@/mygo";

export type SaveStatus = "saved" | "saving" | "unsaved" | "conflict";

type Outcome = { revision: number; conflict: boolean };

export interface Saver {
  /** Takes an edit (its revision is the saver's); false when refused because an operation is running. */
  edit(e: ComposeEdit): boolean;
  setComposing(on: boolean): void;
  /**
   * Queued: settles input (the settle hook), then saves what is pending, even while composing.
   * Input is frozen from the settling's end until `after` (the close answer) is done. Resolves
   * false if the save failed or conflicted, or the settling did not finish (it is saved anyway).
   */
  flush(after?: (saved: boolean) => Promise<void> | void): Promise<boolean>;
  /**
   * Queued: flushes (false → null, op not run), then runs op frozen with the current revision and
   * adopts its result's; a conflict stops saving. Unfreezes whatever op does.
   */
  run<R extends Outcome>(op: (revision: number) => Promise<R>): Promise<R | null>;
  /** The page's input-settling hook: commit pending addresses, finish the IME composition. */
  setSettle(fn: () => Promise<boolean>): void;
  /** Called when an edit arrives while frozen. */
  onRefused(fn: () => void): void;
  /** After a conflict: saves the latest local edit at latestRevision; responses to older saves are ignored. */
  keepMine(latestRevision: number): Promise<boolean>;
  /** After a conflict: drops pending edits and in-flight responses, adopts revision, reads as saved. */
  useLatest(revision: number): void;
  readonly status: SaveStatus;
  readonly frozen: boolean;
  onChange(fn: () => void): () => void;
  /** Cancels the timer and blocks any further scheduling. */
  dispose(): void;
}

/**
 * Saves the latest edit `delay` ms after the last one: never while composing, one save in flight,
 * none after a conflict. Flushes and operations run one at a time, in order.
 */
export function createSaver(
  save: (e: ComposeEdit) => Promise<SaveResult>,
  opts: { delay?: number; revision?: number } = {},
): Saver {
  const delay = opts.delay ?? 1000;
  let revision = opts.revision ?? 0;
  let status: SaveStatus = "saved";
  let pending: ComposeEdit | undefined;
  let last: ComposeEdit | undefined; // the newest edit taken, for keepMine
  let composing = false;
  let frozen = false;
  let lastInput = 0;
  let disposed = false;
  let epoch = 0; // bumped by keepMine / useLatest: older saves' responses no longer count
  let timer: ReturnType<typeof setTimeout> | undefined;
  let inflight: Promise<boolean> | undefined;
  let queue: Promise<unknown> = Promise.resolve();
  let settle = async () => true;
  const listeners = new Set<() => void>();
  const refused = new Set<() => void>();

  const notify = () => {
    for (const f of listeners) f();
  };
  const set = (s: SaveStatus) => {
    status = s;
    notify();
  };
  const freeze = (on: boolean) => {
    frozen = on;
    notify();
  };
  const schedule = () => {
    clearTimeout(timer);
    if (disposed || composing || status === "conflict") return;
    // Only what is left of the delay since the last input.
    timer = setTimeout(() => void saveNow(), Math.max(0, delay - (Date.now() - lastInput)));
  };
  // saveNow saves the pending edit; it resolves true unless the save failed.
  const saveNow = async (): Promise<boolean> => {
    clearTimeout(timer);
    if (inflight) return inflight;
    const e = pending;
    if (!e || status === "conflict") return true;
    pending = undefined;
    set("saving");
    const mine = epoch;
    const p = save({ ...e, revision })
      .then((r) => {
        if (mine !== epoch) return true;
        if (r.conflict) {
          pending = undefined;
          set("conflict");
        } else {
          revision = r.revision;
          set(pending ? "unsaved" : "saved");
        }
        return true;
      })
      .catch(() => {
        if (mine !== epoch) return true;
        pending ??= e; // a newer edit supersedes the failed one
        set("unsaved");
        return false;
      })
      .finally(() => {
        if (inflight === p) inflight = undefined;
      });
    inflight = p;
    const ok = await p;
    if (ok && pending) schedule(); // edits arrived while saving
    return ok;
  };
  const saveAll = async () => {
    clearTimeout(timer);
    while (inflight || (pending && status !== "conflict")) if (!(await saveNow())) return false;
    return status !== "conflict";
  };
  const enqueue = <R>(fn: () => Promise<R>): Promise<R> => {
    const p = queue.then(fn);
    queue = p.catch(() => {});
    return p;
  };
  // exclusive settles, freezes, saves, and runs then with the outcome; it unfreezes in any case.
  const exclusive = <R>(then: (saved: boolean) => Promise<R>) =>
    enqueue(async () => {
      const settled = await settle().catch(() => false);
      freeze(true);
      try {
        const saved = (await saveAll()) && settled;
        return await then(saved);
      } finally {
        freeze(false);
      }
    });

  return {
    edit(e) {
      if (frozen) {
        for (const f of refused) f();
        return false;
      }
      last = e;
      if (status === "conflict") return true;
      pending = e;
      lastInput = Date.now();
      if (status !== "saving") set("unsaved");
      schedule();
      return true;
    },
    setComposing(on) {
      composing = on;
      if (on) clearTimeout(timer);
      else if (pending) {
        lastInput = Date.now(); // the composition's end is input too
        schedule();
      }
    },
    flush(after) {
      return exclusive(async (saved) => {
        await after?.(saved);
        return saved;
      });
    },
    run(op) {
      return exclusive(async (saved) => {
        if (!saved) return null;
        const r = await op(revision);
        if (r.conflict) set("conflict");
        else revision = r.revision;
        return r;
      });
    },
    setSettle(fn) {
      settle = fn;
    },
    onRefused(fn) {
      refused.add(fn);
    },
    keepMine(latestRevision) {
      return enqueue(async () => {
        epoch++;
        inflight = undefined;
        revision = latestRevision;
        pending = last;
        set(pending ? "unsaved" : "saved");
        return saveAll();
      });
    },
    useLatest(r) {
      epoch++;
      inflight = undefined;
      clearTimeout(timer);
      pending = undefined;
      revision = r;
      set("saved");
    },
    get status() {
      return status;
    },
    get frozen() {
      return frozen;
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
