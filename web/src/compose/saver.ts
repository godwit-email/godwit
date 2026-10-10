import type { ComposeEdit, SaveResult } from "@/mygo";

export type SaveStatus = "saved" | "saving" | "unsaved" | "conflict";

type Outcome = { revision: number; conflict: boolean };

export interface Saver {
  /** Takes an edit (its revision is the saver's); false when refused because an operation is running. */
  edit(e: ComposeEdit): boolean;
  setComposing(on: boolean): void;
  /**
   * Queued: settles input (the settle hook), then saves what is pending. Input is frozen from the
   * settling's end until `after` (the close answer) is done. Resolves false, and `after` gets
   * false, if the settling did not finish (nothing is saved then) or the save failed or conflicted.
   */
  flush(after?: (saved: boolean) => Promise<void> | void): Promise<boolean>;
  /**
   * Queued: flushes (false → null, op not run), then runs op frozen with the current revision and
   * adopts its result's; a conflict stops saving. Unfreezes whatever op does. Inside op, use the
   * `flush` it is given: the saver's own queued methods would wait for op itself.
   */
  run<R extends Outcome>(
    op: (revision: number, flush: () => Promise<boolean>) => Promise<R>,
  ): Promise<R | null>;
  /** The page's input-settling hook: commit pending addresses, finish the IME composition. */
  setSettle(fn: () => Promise<boolean>): void;
  /** Called when an edit arrives while frozen. */
  onRefused(fn: () => void): void;
  /** Queued, settled and frozen as run: after a conflict, saves the local snapshot at latestRevision. */
  keepMine(latestRevision: number): Promise<boolean>;
  /** After a conflict: drops pending edits and in-flight responses, adopts revision and snapshot (the latest draft), reads as saved. */
  useLatest(revision: number, snapshot: ComposeEdit): void;
  readonly status: SaveStatus;
  readonly frozen: boolean;
  onChange(fn: () => void): () => void;
  /** Cancels the timer and blocks any further scheduling. */
  dispose(): void;
}

/**
 * Saves the latest edit `delay` ms after the last one: never while composing, none after a
 * conflict. Autosaves, flushes and operations all run one at a time through one queue; autosaves
 * neither settle nor freeze. `snapshot` is the draft as loaded.
 */
export function createSaver(
  save: (e: ComposeEdit) => Promise<SaveResult>,
  opts: { delay?: number; revision?: number; snapshot?: ComposeEdit } = {},
): Saver {
  const delay = opts.delay ?? 1000;
  let revision = opts.revision ?? 0;
  let snapshot = opts.snapshot; // the full local content, for keepMine
  let status: SaveStatus = "saved";
  let pending: ComposeEdit | undefined;
  let composing = false;
  let frozen = false;
  let lastInput = 0;
  let disposed = false;
  let epoch = 0; // bumped by useLatest: saves already in flight no longer count
  let timer: ReturnType<typeof setTimeout> | undefined;
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
  const enqueue = <R>(fn: () => Promise<R>): Promise<R> => {
    const p = queue.then(fn);
    queue = p.catch(() => {});
    return p;
  };
  const schedule = () => {
    clearTimeout(timer);
    if (disposed || composing || status === "conflict") return;
    // Only what is left of the delay since the last input; the queue re-checks everything when its turn comes.
    timer = setTimeout(
      () => void enqueue(async () => !disposed && saveNow()),
      Math.max(0, delay - (Date.now() - lastInput)),
    );
  };
  // saveNow saves the pending edit, from inside the queue; false if it failed or a composition is open.
  const saveNow = async (): Promise<boolean> => {
    const e = pending;
    if (!e || status === "conflict") return true;
    if (composing) return false;
    pending = undefined;
    set("saving");
    const mine = epoch;
    try {
      const r = await save({ ...e, revision });
      if (mine !== epoch) return true;
      if (r.conflict) {
        pending = undefined;
        set("conflict");
      } else {
        revision = r.revision;
        set(pending ? "unsaved" : "saved");
      }
      return true;
    } catch {
      if (mine !== epoch) return true;
      pending ??= e; // a newer edit supersedes the failed one
      set("unsaved");
      return false;
    }
  };
  const saveAll = async () => {
    clearTimeout(timer);
    while (pending && status !== "conflict") if (!(await saveNow())) return false;
    return status !== "conflict";
  };
  // exclusive settles input, freezes, runs body with whether the settling finished, and unfreezes in any case.
  const exclusive = <R>(body: (settled: boolean) => Promise<R>) =>
    enqueue(async () => {
      const settled = await settle().catch(() => false);
      freeze(true);
      try {
        return await body(settled);
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
      snapshot = e;
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
      return exclusive(async (settled) => {
        const saved = settled && (await saveAll());
        await after?.(saved);
        return saved;
      });
    },
    run(op) {
      return exclusive(async (settled) => {
        if (!settled || !(await saveAll())) return null;
        const r = await op(revision, saveAll);
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
      return exclusive(async (settled) => {
        if (!settled) return false;
        revision = latestRevision;
        pending = snapshot;
        set(pending ? "unsaved" : "saved");
        return saveAll();
      });
    },
    useLatest(r, latest) {
      epoch++;
      clearTimeout(timer);
      pending = undefined;
      revision = r;
      snapshot = latest;
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
