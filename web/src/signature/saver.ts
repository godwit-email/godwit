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
 * waits for the next flush.
 */
export function signatureSaver(api: Api, onDoc: (d: Doc | undefined) => void) {
  let account = 0;
  let gen = 0;
  let pending: { account: number; html: string } | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let last = Promise.resolve(true);

  const save = () => {
    clearTimeout(timer);
    timer = undefined;
    if (pending) {
      const p = pending;
      pending = undefined;
      last = last.then(() =>
        api.save(p.account, p.html).then(
          () => true,
          () => {
            pending ??= p; // the next flush tries it again, unless a newer edit replaced it
            return false;
          },
        ),
      );
    }
    return last;
  };

  return {
    gen: () => gen,
    edit(acc: number, html: string) {
      if (acc !== account) return;
      pending = { account: acc, html };
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
