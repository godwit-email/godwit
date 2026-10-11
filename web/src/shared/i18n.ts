/** Looks up a native i18n key; `%s` / `%d` take the args in order. A missing key reads as itself. */
export type T = (key: string, ...args: (string | number)[]) => string;

/** The T over a page's copy (`ComposeState.Text`: key → text in the UI language). */
export function makeT(text: Record<string, string>): T {
  return (key, ...args) => {
    let i = 0;
    return (text[key] ?? key).replace(/%[sd]/g, (m) => (i < args.length ? String(args[i++]) : m));
  };
}
