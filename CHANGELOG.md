# Changelog

## [1.1.0](https://github.com/godwit-email/godwit/compare/v1.0.1...v1.1.0) (2026-10-15)


### Features

* sign in with ChatGPT and Grok subscriptions as model providers ([#61](https://github.com/godwit-email/godwit/issues/61)) ([08452aa](https://github.com/godwit-email/godwit/commit/08452aa7544d6728d3baaffb9441101266d3a9a7))


### Bug Fixes

* **avatars:** prioritize apple-touch-icon links over generic icon links ([#62](https://github.com/godwit-email/godwit/issues/62)) ([78c082e](https://github.com/godwit-email/godwit/commit/78c082e9b2721e44f3ef72fb7dd9a00affed22c2))

## [1.0.1](https://github.com/godwit-email/godwit/compare/v1.0.0...v1.0.1) (2026-10-14)


### Bug Fixes

* **mail:** parse mail stylesheets with tdewolff/parse instead of douceur ([#59](https://github.com/godwit-email/godwit/issues/59)) ([ed48086](https://github.com/godwit-email/godwit/commit/ed48086b1ca17050bf92a394faccb45edc892149))

## [1.0.0](https://github.com/godwit-email/godwit/compare/v0.3.0...v1.0.0) (2026-10-13)


### Features

* **windows:** pack the Windows build as an MSIX bundle for the Microsoft Store ([#51](https://github.com/godwit-email/godwit/issues/51)) ([ae7ff70](https://github.com/godwit-email/godwit/commit/ae7ff70ff52e05c9a3896e28a9290764fdd4090d))

## [0.3.0](https://github.com/godwit-email/godwit/compare/v0.2.0...v0.3.0) (2026-10-13)


### Features

* add an independent unread app icon badge setting ([#46](https://github.com/godwit-email/godwit/issues/46)) ([1c95257](https://github.com/godwit-email/godwit/commit/1c952577002b0380571770cb99af4775f0a651b5))
* add sender favicons and Gravatar avatars ([#49](https://github.com/godwit-email/godwit/issues/49)) ([6576633](https://github.com/godwit-email/godwit/commit/6576633dcbaf760669ec68081936871da74c4000))
* **labels:** email labels, set by the model and by hand ([#48](https://github.com/godwit-email/godwit/issues/48)) ([0a7b4b7](https://github.com/godwit-email/godwit/commit/0a7b4b7d8bf75552b7148bbfefedd9d36fc0a4c4))


### Bug Fixes

* **avatars:** cached icons first, a loading pulse, colored initials ([#54](https://github.com/godwit-email/godwit/issues/54)) ([e00dab7](https://github.com/godwit-email/godwit/commit/e00dab705bdbb67cf22c0468ea8473e8df783da6))
* **labels:** reorder labels with a List (ListState.Reorder) ([#53](https://github.com/godwit-email/godwit/issues/53)) ([26dcbae](https://github.com/godwit-email/godwit/commit/26dcbae24b4541b00b02fa8aa1b3e10281a30468))

## [0.2.0](https://github.com/godwit-email/godwit/compare/v0.1.0...v0.2.0) (2026-10-12)


### Features

* **menu:** add Settings… and Check for Updates… to the macOS app menu ([#43](https://github.com/godwit-email/godwit/issues/43)) ([eb175a4](https://github.com/godwit-email/godwit/commit/eb175a4575b153c944ad32526926ba157093efa4))


### Bug Fixes

* show Apache-2.0 where the app states its license ([#44](https://github.com/godwit-email/godwit/issues/44)) ([15e0b64](https://github.com/godwit-email/godwit/commit/15e0b64ada558f5af09338f995135169abfefc8d))

## [0.1.0](https://github.com/godwit-email/godwit/compare/v0.0.0...v0.1.0) (2026-10-12)


### Features

* **notify:** show sender, subject and snippet in new-mail notifications ([#37](https://github.com/godwit-email/godwit/issues/37)) ([7930e0b](https://github.com/godwit-email/godwit/commit/7930e0b98591843013cf704265259235bdaef557))
* onboarding and dialog polish (confetti, unified dialog, theme at launch) ([#38](https://github.com/godwit-email/godwit/issues/38)) ([f2a38bd](https://github.com/godwit-email/godwit/commit/f2a38bdc0a16df41a19680f093b1c245acd6d022))
* **reader:** mail styles that follow the theme, links that open; sidebar and list polish ([#40](https://github.com/godwit-email/godwit/issues/40)) ([4c217a0](https://github.com/godwit-email/godwit/commit/4c217a08f7a2ba9511398c4ef801ce96a4a4ea41))
* remote mail images through a privacy proxy, disk cache and prefetch ([#39](https://github.com/godwit-email/godwit/issues/39)) ([aecf8dc](https://github.com/godwit-email/godwit/commit/aecf8dcf5589eaa09ddf47c4607f65632210ff28))


### Bug Fixes

* **auth:** sign in to Outlook through the consumers endpoint ([#35](https://github.com/godwit-email/godwit/issues/35)) ([153a9b3](https://github.com/godwit-email/godwit/commit/153a9b3405eb626a6d64719b0002c2f9047b7b55))
* **release:** install web dependencies before building ([#29](https://github.com/godwit-email/godwit/issues/29)) ([cf80638](https://github.com/godwit-email/godwit/commit/cf8063827f5073dfb247f917a901a080dcad8c83))
* **site:** match demo window chrome to the browser platform ([#32](https://github.com/godwit-email/godwit/issues/32)) ([ca3b281](https://github.com/godwit-email/godwit/commit/ca3b281885ebc0d204f688ebbe7ab67a87e231de))

## 0.0.0 (2026-10-12)


### Features

* **ai:** AI and Wit — Genkit engine, skills, gateway, approvals, Ask and Assist ([#4](https://github.com/godwit-email/godwit/issues/4)) ([8a7a367](https://github.com/godwit-email/godwit/commit/8a7a36791854ceae238eb1b731153ea56ef4c4b1))
* **app:** inset page panel and mail sidebar matching the design ([#12](https://github.com/godwit-email/godwit/issues/12)) ([d021ecb](https://github.com/godwit-email/godwit/commit/d021ecb38f707ef3330158ea5391b83d75f2b307))
* **app:** native desktop GUI on mygo ([#6](https://github.com/godwit-email/godwit/issues/6)) ([df45cc9](https://github.com/godwit-email/godwit/commit/df45cc96ccf812a32c7120e4322d8ed68f6d8d42))
* **app:** onboarding's brand shows the Godwit logo, not a mail icon ([#10](https://github.com/godwit-email/godwit/issues/10)) ([12537b7](https://github.com/godwit-email/godwit/commit/12537b76c2914b360b5f94c38f05540b7c5fe0f5))
* **compose:** full compose in the web window, minus Wit ([#15](https://github.com/godwit-email/godwit/issues/15)) ([56942cc](https://github.com/godwit-email/godwit/commit/56942cceea599158df2e388005eef69c7319386d))
* **compose:** Wit in the compose window, opened from every entry point ([#16](https://github.com/godwit-email/godwit/issues/16)) ([1a21c5f](https://github.com/godwit-email/godwit/commit/1a21c5fa734a49b1834077d311d00070c9960447))
* **core:** account onboarding — provider detection, connection check, Outlook OAuth, re-login ([#2](https://github.com/godwit-email/godwit/issues/2)) ([014b82e](https://github.com/godwit-email/godwit/commit/014b82e14aeaa9129fe47802374a1939f2425adc))
* **core:** core mail engine — store, MIME, sync, local-first ops, outbox, workers ([#1](https://github.com/godwit-email/godwit/issues/1)) ([0cf24e5](https://github.com/godwit-email/godwit/commit/0cf24e51a5174c4969c4e6832eff94784e006e0f))
* **kit:** shadcn-style UI kit with motion, app migration, dev gallery ([#13](https://github.com/godwit-email/godwit/issues/13)) ([bb54227](https://github.com/godwit-email/godwit/commit/bb5422752472ebe4e3e6648661fe10d7ee4d5ae4))
* **models:** model services with presets, probing, stable refs, the Responses API and a small-task model ([#7](https://github.com/godwit-email/godwit/issues/7)) ([a0604a8](https://github.com/godwit-email/godwit/commit/a0604a817049a4eb4c79204f853b4c4dd78dd501))
* **plugins:** plugin runtime (goja) and MCP client ([#5](https://github.com/godwit-email/godwit/issues/5)) ([f2f3ed4](https://github.com/godwit-email/godwit/commit/f2f3ed4a7af3e426f91965f5b65e4b8cffc560c3))
* **reader:** message bodies as sandboxed web pages ([#18](https://github.com/godwit-email/godwit/issues/18)) ([d0481a2](https://github.com/godwit-email/godwit/commit/d0481a284ec5751edbe90bb844273844defebd4f))
* **rpc:** headless mode, the Local API (JSON-RPC) and godwit mcp ([#8](https://github.com/godwit-email/godwit/issues/8)) ([5b9ac77](https://github.com/godwit-email/godwit/commit/5b9ac771f907e9bff4e18d6da4bb92f87ba6bb65))
* **site:** godwit.email with updates served from R2 ([#28](https://github.com/godwit-email/godwit/issues/28)) ([15f8632](https://github.com/godwit-email/godwit/commit/15f86328784666f4d39c84aa9b37091e31b312e8))
* **ui:** align the UI 1:1 with the design, with this period's design features ([#19](https://github.com/godwit-email/godwit/issues/19)) ([2aa19dc](https://github.com/godwit-email/godwit/commit/2aa19dceb36995e4d04bb5f39553dd14373d748b))
* **ui:** card layout on the window material, resize grips ([#24](https://github.com/godwit-email/godwit/issues/24)) ([372865a](https://github.com/godwit-email/godwit/commit/372865a8ab9c62b26c7fb38df1748aae8871c9cf))
* **webui:** embedded web pages, with the Wit page as one ([#17](https://github.com/godwit-email/godwit/issues/17)) ([bf19ab9](https://github.com/godwit-email/godwit/commit/bf19ab99b6d95b9fe3e95f06a41464ef0abebc12))
* **webui:** web UI foundation and the compose window skeleton ([#14](https://github.com/godwit-email/godwit/issues/14)) ([129e052](https://github.com/godwit-email/godwit/commit/129e0520e4ce12c22100428797dfa82fcc2468a4))
* **wit:** read_attachment tool, so the agent can read PDF/Office/CSV attachments ([#23](https://github.com/godwit-email/godwit/issues/23)) ([8173d7f](https://github.com/godwit-email/godwit/commit/8173d7fe2070a9435e5d3f3aa913aafe79377cfe))


### Bug Fixes

* **cmd:** macOS top bar is 44pt, with the traffic lights centered in it ([#11](https://github.com/godwit-email/godwit/issues/11)) ([1852e34](https://github.com/godwit-email/godwit/commit/1852e3488b8f9563695287a50b07c89873ec3b43))
