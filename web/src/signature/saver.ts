/**
 * An account's signature as loaded: its editor HTML (code: the stored HTML, for the HTML mode), or
 * why it could not be read.
 */
export type Doc =
  | { account: number; html: string; code?: boolean }
  | { account: number; error: string };

/** Why Go refused a save for good: too many images, or an image it cannot take. */
export type Refusal = "count" | "image";

type Api = {
  load(account: number): Promise<string>;
  code(account: number): Promise<string>;
  save(account: number, html: string): Promise<void>;
  saved(token: number, ok: boolean): Promise<void>;
};

const DELAY = 600;
/** What Go's SignaturePage.Save answers for an account that no longer exists (webui.ErrSignatureAccountGone). */
const ACCOUNT_GONE = "signature account gone";
/** Go's core.ErrSignatureImageCount and core.ErrSignatureImage: the same HTML is refused again. */
const REFUSALS: [string, Refusal][] = [
  ["a signature has at most 4 images", "count"],
  ["a signature image must be", "image"],
];

/**
 * The signature page's protocol. An edit is saved as the account its editor showed, 600 ms after
 * typing pauses, and saves run one after another. show(B) first saves the shown account's pending
 * edit, then loads B (its stored HTML, with code); a Load older than the newest show is dropped,
 * and edits for any account but the shown one are ignored. flush saves at once and answers Go with
 * the token; a failed save stays pending for its account, and every save (a flush, the next
 * debounce) tries it again, unless the account is gone or Go refused the HTML (onRefused): that
 * failure is final and the edit is dropped. A save of the shown account that works clears the
 * refusal.
 */
export function signatureSaver(
  api: Api,
  onDoc: (d: Doc | undefined) => void,
  onRefused: (r: Refusal | undefined) => void = () => {},
) {
  let account = 0;
  let gen = 0;
  const pending = new Map<number, string>(); // per account: its edit not saved yet
  let timer: ReturnType<typeof setTimeout> | undefined;
  let last = Promise.resolve(true);

  // Saves every account's pending edit, after the saves before; true if all of them saved. A
  // failed one stays pending for its account (unless that account has a newer edit) and is tried
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
            if (acc === account) onRefused(undefined);
            return ok;
          },
          (e) => {
            const msg = String(e instanceof Error ? e.message : e);
            // The account is gone: retrying can never work, so the edit is dropped as saved.
            if (msg.includes(ACCOUNT_GONE)) return ok;
            const refused = REFUSALS.find(([m]) => msg.includes(m))?.[1];
            if (refused) {
              if (acc === account) onRefused(refused); // said, never retried
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
      if (acc !== account) return;
      pending.set(acc, html);
      clearTimeout(timer);
      timer = setTimeout(save, DELAY);
    },
    show(id: number, code = false) {
      const saved = save();
      if (id !== account) onRefused(undefined);
      account = id;
      const g = ++gen;
      onDoc(undefined);
      saved
        .then(() => (g === gen ? (code ? api.code(id) : api.load(id)) : undefined))
        .then(
          (html) =>
            g === gen &&
            html !== undefined &&
            onDoc(code ? { account: id, html, code } : { account: id, html }),
          (e) =>
            g === gen && onDoc({ account: id, error: e instanceof Error ? e.message : String(e) }),
        );
    },
    flush(token: number) {
      save().then((ok) => api.saved(token, ok).catch(() => {}));
    },
  };
}
