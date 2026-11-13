import { isMyGo, runtime } from "mygo-runtime";

/**
 * The URL a page loads godwit-img://<host><path> at (Go's image proxy, webui.ImageScheme). WebView2
 * (Windows) requests no custom scheme a page names: MyGo serves an app scheme there at
 * http://<host>.<scheme>.localhost<path>, a loopback origin, and hands Go the godwit-img: URL. The Go
 * side's imageURL builds the body's image URLs the same way.
 */
export function imageURL(host: string, path = "/"): string {
  return onWebView2()
    ? `http://${host}.godwit-img.localhost${path}`
    : `godwit-img://${host}${path === "/" ? "" : path}`;
}

/** The img-src sources of imageURL's images: only Windows needs the loopback http form. */
export function imageSources(): string {
  return onWebView2() ? "godwit-img: http://*.godwit-img.localhost" : "godwit-img:";
}

const onWebView2 = () => isMyGo() && runtime().platform === "win32";
