/**
 * A signature as loaded: its editor HTML (code: the stored HTML, for the HTML mode), or why it
 * could not be read.
 */
export type Doc = { id: number; html: string; code?: boolean } | { id: number; error: string };

/** Why Go refused a save for good: too many images, or an image it cannot take. */
export type Refusal = "count" | "image";

type Api = {
  load(id: number): Promise<string>;
  code(id: number): Promise<string>;
  save(id: number, html: string): Promise<void>;
  saved(token: number, ok: boolean): Promise<void>;
};

const DELAY = 600;
/** What Go's SignaturePage.Save answers for a signature that no longer exists (webui.ErrSignatureGone). */
const GONE = "signature gone";
/** Go's core.ErrSignatureImageCount and core.ErrSignatureImage: the same HTML is refused again. */
const REFUSALS: [string, Refusal][] = [
  ["a signature has at most 4 images", "count"],
  ["a signature image must be", "image"],
];

/**
 * The signature page's protocol. An edit is saved as the signature its editor showed, 600 ms after
 * typing pauses, and saves run one after another. show(B) first saves the shown signature's
 * pending edit, then loads B (its stored HTML, with code); a Load older than the newest show is
 * dropped, and edits for any signature but the shown one are ignored. flush saves at once and
 * answers Go with the token; a failed save stays pending for its signature, and every save (a
 * flush, the next debounce) tries it again, unless the signature is gone or Go refused the HTML
 * (onRefused): that failure is final and the edit is dropped. A save of the shown signature that
 * works clears the refusal.
 */
export function signatureSaver(
  api: Api,
  onDoc: (d: Doc | undefined) => void,
  onRefused: (r: Refusal | undefined) => void = () => {},
) {
  let shown = 0;
  let gen = 0;
  const pending = new Map<number, string>(); // per signature: its edit not saved yet
  let timer: ReturnType<typeof setTimeout> | undefined;
  let last = Promise.resolve(true);

  // Saves every signature's pending edit, after the saves before; true if all of them saved. A
  // failed one stays pending for its signature (unless that signature has a newer edit) and is tried
  // again by the next save.
  const save = () => {
    clearTimeout(timer);
    timer = undefined;
    // Nothing new: the last round's answer, unless its failures were all dropped (refused, gone).
    if (!pending.size) return last.then((ok) => ok || !pending.size);
    let round = last.then(() => true);
    for (const [acc, html] of pending) {
      pending.delete(acc);
      round = round.then((ok) =>
        api.save(acc, html).then(
          () => {
            if (acc === shown) onRefused(undefined);
            return ok;
          },
          (e) => {
            const msg = String(e instanceof Error ? e.message : e);
            // The signature is gone: retrying can never work, so the edit is dropped as saved.
            if (msg.includes(GONE)) return ok;
            const refused = REFUSALS.find(([m]) => msg.includes(m))?.[1];
            if (refused) {
              if (acc === shown) onRefused(refused); // said, never retried
              return false;
            }
            if (!pending.has(acc)) pending.set(acc, html);
            return false;
          },
        ),
      );
    }
    last = round;
    return last;
  };

  return {
    gen: () => gen,
    edit(acc: number, html: string) {
      if (acc !== shown) return;
      pending.set(acc, html);
      clearTimeout(timer);
      timer = setTimeout(save, DELAY);
    },
    show(id: number, code = false) {
      const saved = save();
      if (id !== shown) onRefused(undefined);
      shown = id;
      const g = ++gen;
      onDoc(undefined);
      saved
        .then(() => (g === gen ? (code ? api.code(id) : api.load(id)) : undefined))
        .then(
          (html) =>
            g === gen && html !== undefined && onDoc(code ? { id, html, code } : { id, html }),
          (e) => g === gen && onDoc({ id, error: e instanceof Error ? e.message : String(e) }),
        );
    },
    flush(token: number) {
      save().then((ok) => api.saved(token, ok).catch(() => {}));
    },
  };
}
