/** An account's signature as loaded: its editor HTML, or why it could not be read. */
export type Doc = { account: number; html: string } | { account: number; error: string };

type Api = {
  load(account: number): Promise<string>;
  save(account: number, html: string): Promise<void>;
  saved(token: number, ok: boolean): Promise<void>;
};

const DELAY = 600;

/**
 * The signature page's protocol. An edit is saved as the account its editor showed, 600 ms after
 * typing pauses, and saves run one after another. show(B) first saves the shown account's pending
 * edit, then loads B; a Load older than the newest show is dropped, and edits for any account but
 * the shown one are ignored. flush saves at once and answers Go with the token; a failed save
 * stays pending for its account, and every save (a flush, the next debounce) tries it again.
 */
export function signatureSaver(api: Api, onDoc: (d: Doc | undefined) => void) {
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
    if (!pending.size) return last;
    let round = last.then(() => true);
    for (const [acc, html] of pending) {
      pending.delete(acc);
      round = round.then((ok) =>
        api.save(acc, html).then(
          () => ok,
          () => {
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
    show(id: number) {
      const saved = save();
      account = id;
      const g = ++gen;
      onDoc(undefined);
      saved
        .then(() => (g === gen ? api.load(id) : undefined))
        .then(
          (html) => g === gen && html !== undefined && onDoc({ account: id, html }),
          (e) =>
            g === gen && onDoc({ account: id, error: e instanceof Error ? e.message : String(e) }),
        );
    },
    flush(token: number) {
      save().then((ok) => api.saved(token, ok).catch(() => {}));
    },
  };
}
