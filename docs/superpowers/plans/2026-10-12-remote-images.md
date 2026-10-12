# Remote images: proxy, cache, prefetch

**Goal:** remote images in received HTML mail just show, as in Apple Mail Privacy Protection. Privacy is enforced by a Go proxy (no cookies, no Referer, generic UA, public hosts only), not by asking per message. Today every image of a message goes through `ReaderPage.Image` as a data: URL on 4 workers with one 60 s deadline for the whole message and a 20 MB budget; a 40-image digest times out and breaks. There is no cache.

## WebKit probe (scripts/imgproto, run on macOS WKWebView via mygo)

A page loaded from a custom scheme holds a `srcdoc` iframe with the real frame CSP (`default-src 'none'; style-src 'unsafe-inline'; img-src godwit-img:`) and 3 eager images, 40 `loading="lazy"` images 3000 px down, a `background-image: url(godwit-img://…)` div, an `https:` image and a `data:` image.

| Question | Result |
| --- | --- |
| srcdoc iframe loads `<img>` from a mygo custom scheme under `img-src godwit-img:` | **Yes**, both for `sandbox="allow-same-origin"` and `"allow-same-origin allow-scripts"`. The request reaches the Go `http.Handler`; it carries no Referer / Origin. |
| CSS `background-image: url(godwit-img://…)` under that CSP | **Yes** (requested and applied). |
| CSP blocks `https:` and `data:` images in the frame | **Yes** (not loaded, no request). |
| `loading="lazy"` in the frame | **Only with `allow-scripts`.** Without it (what the repo has: `sandbox="allow-same-origin"`) WebKit loads all 40 at once. With it, lazy honours the *top-level* viewport (clipped by the outer page), and loads on scroll. |
| Does that help the reader? | **No.** The reader page is a native web view sized to the full body height and scrolled natively (`ui.Scroll` + `ui.WebView().Height(px)`), so every image is always "in the viewport". |

Consequences: the browser loads everything in parallel by itself. The Go proxy bounds the upstream work (per-host and global concurrency, per-image timeout), and the disk cache makes the second load free. WebKit lazy loading is not used; a viewport-driven lazy loader would need the native scroll offset pushed into the page and is a later step, not part of this change. `allow-scripts` is not added.

## Design

1. **Protocol.** `godwit-img://<message>/<token>`, registered with `mygo.Protocol.Handle` before the windows exist. `token` is the first 16 bytes of sha256(URL), hex. Rewriting happens in `webui.prepareBody` (not in the sanitizer, which stays pure): each `<img src=http(s)…>` and each `data-bg` (from a `background` / `background-image` `url(http…)` in an inline style, extracted by `core/mail.Sanitize`) becomes a godwit-img URL and the message's Reader state records token → URL. Frame CSP and the page CSP (srcdoc inherits it) gain `godwit-img:` in `img-src`; nothing else changes, so the page still cannot reach the network.
2. **Handler.** Looks up the message's state and token (404 otherwise), requires the message's images to be allowed (policy, or the user's 加载图片), then `imagecache.Get(url)`. The response is served with the sniffed `image/*` type, `Cache-Control: private, max-age=86400`, `X-Content-Type-Options: nosniff`.
3. **`internal/imagecache`.** Files named by sha256(URL) in `<dataDir>/cache/images`, total cap 256 MB, LRU by access time (in-memory index rebuilt from the directory on start), `singleflight` per URL, a short in-memory negative cache (5 min) so a dead URL is not retried per render. Fetching uses `imagefetch` (public hosts, no cookies/Referer, redirect limit, content-sniffed PNG/JPEG/GIF/WebP, ≤ 5 MB, ≤ 40 MP, 15 s per image) plus a global concurrency of 8 and 3 per host. Dimensions are read from the cached header, so `Dims(url)` is free.
4. **No layout jump.** `prepareBody` fills `width`/`height` from `Dims` when the `<img>` has neither (the frame CSS already has `height:auto; max-width:100%`).
5. **Policy.** `Settings.RemoteImages`: `auto` (default) / `trusted` / `ask`. `Service.ImagesAllowed(message)`: false in a Junk folder always; `auto` → true; `trusted` → the sender is in TrustedImageSenders; `ask` → false. The existing 加载图片 / 始终信任 UI stays for the false cases.
6. **Tracking pixels.** `mail.Sanitize` drops, without counting or requesting them, `<img>` with a declared width or height ≤ 1 and URLs matching a small known-tracker list (e.g. TikTok `/edm/catcher`).
7. **Prefetch.** `core.Service` listens for `MessageReceived` and, for each new message that `ImagesAllowed`, sanitizes it, collects the image URLs and `Get`s them (≤ 30 images / 15 MB per message, one message at a time, background context). Opening is instant and the sender sees the fetch at arrival, not at reading time.
8. **Settings UI.** A "Remote images" select, the cache size and a clear button (general settings), zh and en strings.
9. **Removed.** `ReaderPage.Image`, `ErrImageDeadline`, the per-message `imgs` / `sem` / `budget` / `imageDeadline`, the JS `api.image` loop in `Body.tsx`. The per-message `Budget` stays for inline data: images only.

## Order

1. Protocol + rewrite + CSP, fetching straight through `imagefetch` (no cache). 2. Disk cache. 3. Policy setting, tracker filter, dims, prefetch, settings UI.
