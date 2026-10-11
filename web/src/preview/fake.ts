import type { Platform, Runtime } from "mygo-runtime";

type Handler = (...args: never[]) => unknown;
type Listener = (payload: never) => void;

/** A stand-in for the `window.mygo` the desktop shell injects: just what mygo-runtime's index.js calls. */
export function installFake(handlers: Record<string, Handler>, platform: Platform = "darwin") {
  const listeners = new Map<string, Set<Listener>>();
  const on = (name: string, l: Listener) => {
    const set = listeners.get(name) ?? new Set();
    listeners.set(name, set.add(l));
    return () => void set.delete(l);
  };
  const noop = async () => {};
  const rt = {
    call: async (method: string, ...args: unknown[]) => {
      const h = handlers[method];
      if (!h) throw new Error(`preview: ${method} is not faked`);
      return (h as (...a: unknown[]) => unknown)(...args);
    },
    channel: () => {
      throw new Error("preview: channels are not faked");
    },
    on,
    once: (name: string, l: Listener) => {
      const off = on(name, (p) => {
        off();
        l(p);
      });
      return off;
    },
    platform,
    windowId: 1,
    version: "preview",
    window: {
      minimize: noop,
      maximize: noop,
      unmaximize: noop,
      toggleMaximize: noop,
      isMaximized: async () => false,
      toggleFullScreen: noop,
      close: noop,
      setTitle: async (title: string) => {
        document.title = title;
      },
    },
  } as unknown as Runtime;
  (window as { mygo?: Runtime }).mygo = rt;
  /** Fires a Go event at the page's listeners. */
  return (name: string, payload: unknown) => {
    for (const l of [...(listeners.get(name) ?? [])]) (l as (p: unknown) => void)(payload);
  };
}
