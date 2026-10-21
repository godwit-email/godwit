import { isMyGo, onLocaleChange, locale as runtimeLocale } from "mygo-runtime";
import { useEffect, useMemo, useState } from "react";
import { messages, messagesIn } from "@/i18n";

export type T = (key: string, ...args: (string | number)[]) => string;

/** Keep dynamic page keys while the generated catalog owns arguments and plural rules. */
export function makeT(locale?: string): T {
  const catalog = (locale ? messagesIn(locale) : messages) as unknown as Record<
    string,
    (...args: (string | number)[]) => string
  >;
  return (key, ...args) => {
    const name = key.replace(/[.-]/g, "_");
    return Object.hasOwn(catalog, name) ? catalog[name](...args) : key;
  };
}

export function currentLocale(): string {
  if (isMyGo()) {
    const l = runtimeLocale();
    if (l) return l;
  }
  return navigator.language || "en";
}

export function useT(locale?: string): T {
  const [changed, setChanged] = useState<string | undefined>(() =>
    isMyGo() ? runtimeLocale() : undefined,
  );
  useEffect(() => {
    const l = changed ?? locale ?? currentLocale();
    if (typeof document !== "undefined" && l) {
      document.documentElement.lang = l;
    }
  }, [changed, locale]);
  useEffect(() => {
    if (!isMyGo()) return;
    const off = onLocaleChange(setChanged);
    setChanged(runtimeLocale());
    return off;
  }, []);
  return useMemo(() => makeT(changed ?? locale), [changed, locale]);
}
