# Changelog

## [1.23.1](https://github.com/godwit-email/godwit/compare/v1.23.0...v1.23.1) (2026-11-01)


### Bug Fixes

* **conn:** retry with STARTTLS when an SSL/TLS port greets in plaintext ([#407](https://github.com/godwit-email/godwit/issues/407)) ([698952e](https://github.com/godwit-email/godwit/commit/698952ee3ff5060cdaedb195977d6fa09d401787)), closes [#406](https://github.com/godwit-email/godwit/issues/406)

## [1.23.0](https://github.com/godwit-email/godwit/compare/v1.22.0...v1.23.0) (2026-11-01)


### Features

* **reader:** show the system's file type icon on attachment tiles ([#405](https://github.com/godwit-email/godwit/issues/405)) ([4d774a6](https://github.com/godwit-email/godwit/commit/4d774a65ebe46ceaacc08c65ad0e72c23e657f69))


### Bug Fixes

* **assist:** one-click and selection rewrites follow the sending account ([#400](https://github.com/godwit-email/godwit/issues/400)) ([b27392e](https://github.com/godwit-email/godwit/commit/b27392e12618f2875f887f484d5c8127d0c0e612))
* **reader:** bare links in AI output end at CJK punctuation ([#403](https://github.com/godwit-email/godwit/issues/403)) ([1fede1a](https://github.com/godwit-email/godwit/commit/1fede1a03f232b9907fdbe02d11ccfa029a8457e))

## [1.22.0](https://github.com/godwit-email/godwit/compare/v1.21.0...v1.22.0) (2026-11-01)


### Features

* **ai:** answer judge questions with the small model when no judge is set ([#380](https://github.com/godwit-email/godwit/issues/380)) ([8fbdf20](https://github.com/godwit-email/godwit/commit/8fbdf20c0a410de65a428cf3f813aecc52f33259))
* **ai:** give quick replies the whole thread ([#382](https://github.com/godwit-email/godwit/issues/382)) ([6536505](https://github.com/godwit-email/godwit/commit/65365051ce39a631dbc87014d10c213b4b8897e6))
* **auth:** sign in to Gmail with OAuth (own client, client_secret import, Thunderbird's public client) ([#395](https://github.com/godwit-email/godwit/issues/395)) ([72d3462](https://github.com/godwit-email/godwit/commit/72d3462d18d25b10a77e490300a16eef98ade156))
* **contacts:** contacts page beside Mail and Wit, with unsubscribe, undoable archive all and block ([#383](https://github.com/godwit-email/godwit/issues/383)) ([f2a02ab](https://github.com/godwit-email/godwit/commit/f2a02ab47ed0e3caca280c4c92bdfc55d25f14ca))
* **windows:** check for updates in the Microsoft Store version ([#378](https://github.com/godwit-email/godwit/issues/378)) ([67eeeb9](https://github.com/godwit-email/godwit/commit/67eeeb98a9966f4e0ba590fa0bd3ab8913684c02))


### Bug Fixes

* **ai:** name the destination on Wit's unsubscribe and open-link approvals ([#384](https://github.com/godwit-email/godwit/issues/384)) ([05f537c](https://github.com/godwit-email/godwit/commit/05f537c1533a8a08815279262125199951783f92))
* **imap:** work around Tencent Exmail's BODYSTRUCTURE and HEADER.FIELDS quirks ([#394](https://github.com/godwit-email/godwit/issues/394)) ([66e7967](https://github.com/godwit-email/godwit/commit/66e7967602e792011d0f7a89a6d83146ef762062)), closes [#387](https://github.com/godwit-email/godwit/issues/387) [#388](https://github.com/godwit-email/godwit/issues/388)
* **sync:** import Gmail messages whose BODYSTRUCTURE go-imap rejects ([#396](https://github.com/godwit-email/godwit/issues/396)) ([d53c836](https://github.com/godwit-email/godwit/commit/d53c836793d03cef43a1e41d9caa7a98e8bfb10f))

## [1.21.0](https://github.com/godwit-email/godwit/compare/v1.20.2...v1.21.0) (2026-10-30)


### Features

* **ai,reader:** responsive single-line quick replies with universal intent extraction ([#373](https://github.com/godwit-email/godwit/issues/373)) ([dc8bd91](https://github.com/godwit-email/godwit/commit/dc8bd91ad871b37c998839f273e09adb24aa9a3c))
* **mail:** add folder bulk actions for empty trash, empty spam, and mark all as read ([#377](https://github.com/godwit-email/godwit/issues/377)) ([f7cb1a0](https://github.com/godwit-email/godwit/commit/f7cb1a0d7f684acf6e7ac62a99c0da645ea1a76d))
* **mail:** add per-account labels collapsible group to sidebar ([#360](https://github.com/godwit-email/godwit/issues/360)) ([d4d01f9](https://github.com/godwit-email/godwit/commit/d4d01f9814a4f8145d51a7d47579abae1c9fd60f))
* **mask:** recognition model for masking (ADR 0028, [#363](https://github.com/godwit-email/godwit/issues/363)) ([#371](https://github.com/godwit-email/godwit/issues/371)) ([667b040](https://github.com/godwit-email/godwit/commit/667b0400b3b6df7a3492f0af54c7b2f665be2f82))
* **palette:** add filter menu icons, fill styles, and hide floating preview ([#362](https://github.com/godwit-email/godwit/issues/362)) ([939b12b](https://github.com/godwit-email/godwit/commit/939b12b2d540b5d157b75236e4c88d863ae50905))
* **reader:** display account sidebar name instead of generic 'me' in message recipients ([#375](https://github.com/godwit-email/godwit/issues/375)) ([3b094c0](https://github.com/godwit-email/godwit/commit/3b094c0ac99fea09e793d6b36c35e913241199fc))
* **security:** add display-name spoofing protection, verification badges, and AI fraud classification ([#359](https://github.com/godwit-email/godwit/issues/359)) ([4b699a7](https://github.com/godwit-email/godwit/commit/4b699a76ddde92c02bd06cd4fb47afe73595df96))


### Bug Fixes

* **wit:** give the prompt input a soft shadow that deepens on focus ([#376](https://github.com/godwit-email/godwit/issues/376)) ([f79d7eb](https://github.com/godwit-email/godwit/commit/f79d7ebe186939aa907bf11d991cae19ecdd73a1))

## [1.20.2](https://github.com/godwit-email/godwit/compare/v1.20.1...v1.20.2) (2026-10-30)


### Bug Fixes

* **kit:** glass popovers stand a web view's screenshot in for it ([#361](https://github.com/godwit-email/godwit/issues/361)) ([d69396f](https://github.com/godwit-email/godwit/commit/d69396f00346c4bec0414e687ce942d2f2b401f7))

## [1.20.1](https://github.com/godwit-email/godwit/compare/v1.20.0...v1.20.1) (2026-10-30)


### Bug Fixes

* close audit findings in sending, sync, storage, attachments, Wit and plugins ([#352](https://github.com/godwit-email/godwit/issues/352)) ([58a7d62](https://github.com/godwit-email/godwit/commit/58a7d622494a71312c391b53fdbf7426f9a69baf))
* **mailsync:** read envelope fields from headers, not ENVELOPE ([#358](https://github.com/godwit-email/godwit/issues/358)) ([0c03b24](https://github.com/godwit-email/godwit/commit/0c03b24cccea5748c7e36254630b384fdaf6dd95))

## [1.20.0](https://github.com/godwit-email/godwit/compare/v1.19.0...v1.20.0) (2026-10-29)


### Features

* **avatar:** show BIMI logos published by DMARC-enforcing sender domains ([#351](https://github.com/godwit-email/godwit/issues/351)) ([feea547](https://github.com/godwit-email/godwit/commit/feea5476ec23aff8a138694d4abc284dbb140cb4))
* **wit:** load sender avatars on cited mail cards ([#348](https://github.com/godwit-email/godwit/issues/348)) ([136cc01](https://github.com/godwit-email/godwit/commit/136cc01e378cdb59f3ad5f85ee2e708574d94e5f))


### Bug Fixes

* follow mygo 0.7's installer names in download links, the cask and pruning ([#349](https://github.com/godwit-email/godwit/issues/349)) ([d40f800](https://github.com/godwit-email/godwit/commit/d40f8009cb2b8565f9fed489a22ebb7df4cc0478))

## [1.19.0](https://github.com/godwit-email/godwit/compare/v1.18.0...v1.19.0) (2026-10-29)


### Features

* **app:** reader redesign — one sheet, folded gists, thread digest ([#342](https://github.com/godwit-email/godwit/issues/342)) ([5c0684c](https://github.com/godwit-email/godwit/commit/5c0684c6588688a365cf63b4dc4726ee36cdcc8d))
* count tracking pixels apart from remote images ([#333](https://github.com/godwit-email/godwit/issues/333)) ([40848a0](https://github.com/godwit-email/godwit/commit/40848a05d437d7ec399f6fd85167bc17970fba8b))
* **kit,web:** frosted liquid glass for popovers, overlays, and controls ([#331](https://github.com/godwit-email/godwit/issues/331)) ([4eee858](https://github.com/godwit-email/godwit/commit/4eee8580763047d78f0b3e12059742c8a4f9c911))
* **mail:** add reply, reply all, and forward actions to row context menu ([#343](https://github.com/godwit-email/godwit/issues/343)) ([3dc4a88](https://github.com/godwit-email/godwit/commit/3dc4a88608e5707c66038fbe5db29c79d2f232f5))
* **settings:** give every setting one owner; 智能 page with one-click Claude and Codex; masking log by request ([#347](https://github.com/godwit-email/godwit/issues/347)) ([442791b](https://github.com/godwit-email/godwit/commit/442791bc0acdbd78c6eb9b2c0a8882588c620248))
* **wit:** add frosted glass info tooltip and ICU pluralization to masking note ([#346](https://github.com/godwit-email/godwit/issues/346)) ([90ee371](https://github.com/godwit-email/godwit/commit/90ee371070a94d539fc17dbbb2e87d0301deaa8f))
* **wit:** add humanize skill to remove AI writing patterns from drafts ([#344](https://github.com/godwit-email/godwit/issues/344)) ([fc8ebd5](https://github.com/godwit-email/godwit/commit/fc8ebd53282c16fd8e1517c6180398b724832fbc))


### Bug Fixes

* **assist:** convert plain text drafts into HTML paragraphs and line breaks ([#345](https://github.com/godwit-email/godwit/issues/345)) ([9b1eebc](https://github.com/godwit-email/godwit/commit/9b1eebce61ac46dd4ccd6a323365cbe2a22c81ca))

## [1.18.0](https://github.com/godwit-email/godwit/compare/v1.17.0...v1.18.0) (2026-10-28)


### Features

* ⌘K search palette for mail, conversations, contacts and attachments ([#326](https://github.com/godwit-email/godwit/issues/326)) ([a023381](https://github.com/godwit-email/godwit/commit/a023381b1946a955f2023564714e32a4d99bbe60))
* **app:** unframe the reader toolbar; save and print move to a more menu ([#332](https://github.com/godwit-email/godwit/issues/332)) ([1b2d0cf](https://github.com/godwit-email/godwit/commit/1b2d0cf4f4072c893e11541514a2bc0dc2a542af))
* **kit:** harmonize sidebar item hover and selected states ([#330](https://github.com/godwit-email/godwit/issues/330)) ([8583948](https://github.com/godwit-email/godwit/commit/8583948c9dd0cb604726216e83691606eb84219b))
* **kit:** soften list row hover contrast scoped to kit.List ([#329](https://github.com/godwit-email/godwit/issues/329)) ([7306196](https://github.com/godwit-email/godwit/commit/7306196db92e447d9a56141cbd0d5c7c72f10b6e))
* mask text sent to cloud services (ADR 0025) ([#309](https://github.com/godwit-email/godwit/issues/309)) ([e798cea](https://github.com/godwit-email/godwit/commit/e798cea81132c7c7ad0b2831e71972501b1f06f2))

## [1.17.0](https://github.com/godwit-email/godwit/compare/v1.16.0...v1.17.0) (2026-10-27)


### Features

* **i18n:** localize date and time formatting according to Unicode CLDR standards ([#306](https://github.com/godwit-email/godwit/issues/306)) ([3adf210](https://github.com/godwit-email/godwit/commit/3adf2103a37e81521c450bd61b9beef1a6f1a9c3))
* **mail:** direct star toggle on mail rows and per-account starred category ([#291](https://github.com/godwit-email/godwit/issues/291)) ([065c017](https://github.com/godwit-email/godwit/commit/065c017ce719880aa15f4b8f6d4e8040e29f65ff))
* **mail:** enable context menu transitions across inbox, archive, junk, and trash ([#304](https://github.com/godwit-email/godwit/issues/304)) ([84eb57d](https://github.com/godwit-email/godwit/commit/84eb57ddc004d85a04cd48b108a3c3d2c451e7ad))
* **reader,settings:** sender actions popover and blocked senders management ([#292](https://github.com/godwit-email/godwit/issues/292)) ([05417f9](https://github.com/godwit-email/godwit/commit/05417f98583549653679b73198f2a01f7c20bf58))
* **reader:** adapt email colors to their backgrounds in dark mode ([#301](https://github.com/godwit-email/godwit/issues/301)) ([9b5ac8a](https://github.com/godwit-email/godwit/commit/9b5ac8a41780c3b7369fb11d07239e1146e53cb5))
* **reader:** add save mail (CMD+S) and print mail (CMD+P) actions ([#305](https://github.com/godwit-email/godwit/issues/305)) ([000f819](https://github.com/godwit-email/godwit/commit/000f8196d145f242636ccc9b9041fed74bf6658c))
* **shell,sync:** polish sync status popover layout, account icons, and alignment ([#285](https://github.com/godwit-email/godwit/issues/285)) ([964ea28](https://github.com/godwit-email/godwit/commit/964ea289f2a6ab2bda6901054a0553e3c75cedfe))
* **wit:** add mail triage, flag & reply actions with interactive batch approval card ([#302](https://github.com/godwit-email/godwit/issues/302)) ([ff0e5f7](https://github.com/godwit-email/godwit/commit/ff0e5f78af3608bbeb5e980edb0d5d873816b9fa))
* **wit:** cache a conversation's prompt and show its cache hits ([#283](https://github.com/godwit-email/godwit/issues/283)) ([526d69f](https://github.com/godwit-email/godwit/commit/526d69f30e6f557bc09998df647fbf3d36cd81ab))


### Bug Fixes

* **app:** deflake TestPageMailtoFromDefaultAccount ([#308](https://github.com/godwit-email/godwit/issues/308)) ([9e0e9c1](https://github.com/godwit-email/godwit/commit/9e0e9c1ab15b2d9fb1f005954c4c643dda7fd542))
* **mail:** leave Junk and Trash out of an account's unread count ([#303](https://github.com/godwit-email/godwit/issues/303)) ([816bb6f](https://github.com/godwit-email/godwit/commit/816bb6f18e0920145cd202c66dde363006c2295a))
* **reader:** match native webview background for transparent body ([#294](https://github.com/godwit-email/godwit/issues/294)) ([96ca375](https://github.com/godwit-email/godwit/commit/96ca37539ba6e3ca751ca15e10ba53bc1d7c0bdf))
* **wit:** add explicit close button to quote search popover ([#286](https://github.com/godwit-email/godwit/issues/286)) ([4931286](https://github.com/godwit-email/godwit/commit/49312865ab38e1759bdd87aab97513084f864701))
* **wit:** give the time note in the UI's current time zone ([#307](https://github.com/godwit-email/godwit/issues/307)) ([cbb4193](https://github.com/godwit-email/godwit/commit/cbb4193aa50a3d6cfc3b4435da385b5c9f8bd9bd)), closes [#146](https://github.com/godwit-email/godwit/issues/146)

## [1.16.0](https://github.com/godwit-email/godwit/compare/v1.15.0...v1.16.0) (2026-10-26)


### Features

* **account,wit:** per-account AI instructions for drafting ([#269](https://github.com/godwit-email/godwit/issues/269)) ([d2f87b7](https://github.com/godwit-email/godwit/commit/d2f87b7422ef83cddcc17bc702d11d310edd43af))
* **compose,wit:** draft review cards, settled-edit status and no-op edits (slim redo of [#268](https://github.com/godwit-email/godwit/issues/268)) ([#276](https://github.com/godwit-email/godwit/issues/276)) ([3871608](https://github.com/godwit-email/godwit/commit/387160826e98050f8bbd2169d12386b783d26fad))
* **compose:** toolbar undo and redo over the editor's own history ([#279](https://github.com/godwit-email/godwit/issues/279)) ([498e496](https://github.com/godwit-email/godwit/commit/498e4963ba92655f1acf07e341a3d0c561cafdc0))
* **models:** on-demand service tests, compose model persistence, and Apple Intelligence service row ([#272](https://github.com/godwit-email/godwit/issues/272)) ([1eeb205](https://github.com/godwit-email/godwit/commit/1eeb20517a3a6e00f40fd276c3f7c2ffd10afbc5))
* **reader,settings:** collapse long thread history into expandable card with configurable threshold ([#273](https://github.com/godwit-email/godwit/issues/273)) ([904eb3f](https://github.com/godwit-email/godwit/commit/904eb3f7d1147c21d3c9eaaff94a7a3a7b4a7e5f))
* **settings,labels:** add label display style setting (icon and text, icon only, text only) ([#275](https://github.com/godwit-email/godwit/issues/275)) ([ab50787](https://github.com/godwit-email/godwit/commit/ab5078740da17fc79d8f27139ff780cedeb59f28))
* **settings:** Wit instructions, bordered cards, sidebar names, signature editor ([#282](https://github.com/godwit-email/godwit/issues/282)) ([e04ade8](https://github.com/godwit-email/godwit/commit/e04ade8e127bc710789384f81f89d84c6d2cff17))
* **wit:** show an answer's steps in the order they came ([#252](https://github.com/godwit-email/godwit/issues/252)) ([d5eccc8](https://github.com/godwit-email/godwit/commit/d5eccc86cac267a6712f21fbe568486da1d61986))


### Bug Fixes

* **mail,reader:** preserve multi-line inline styles and prevent dark canvas inversion in dark mode ([#274](https://github.com/godwit-email/godwit/issues/274)) ([97de648](https://github.com/godwit-email/godwit/commit/97de6482c9e0d94ce10b38009323999589ea8e29))
* **mail:** keep a table's cellspacing, cellpadding and border ([#281](https://github.com/godwit-email/godwit/issues/281)) ([54f17f4](https://github.com/godwit-email/godwit/commit/54f17f4dcc03f7489b0807d60aba7851d60db122))
* **reader,images:** load remote images behind fake-ip TUN proxies and the macOS system proxy ([#280](https://github.com/godwit-email/godwit/issues/280)) ([7ffc718](https://github.com/godwit-email/godwit/commit/7ffc718d76bd9ce1d7f2fda9f966d88329f3d4b3))
* **shell,theme:** eliminate white flash on startup in light and dark mode ([#278](https://github.com/godwit-email/godwit/issues/278)) ([af946b2](https://github.com/godwit-email/godwit/commit/af946b20841132882561ae9af847cd7a659c96b8))
* **sync:** eliminate redundant sync rounds and stabilize multi-account sync status ([#271](https://github.com/godwit-email/godwit/issues/271)) ([7ea4c56](https://github.com/godwit-email/godwit/commit/7ea4c56e046d0796f547380e0d62954e18215da6))

## [1.15.0](https://github.com/godwit-email/godwit/compare/v1.14.0...v1.15.0) (2026-10-25)


### Features

* **ai:** delete Genkit; pi is the only agent runtime ([#236](https://github.com/godwit-email/godwit/issues/236)) ([#250](https://github.com/godwit-email/godwit/issues/250)) ([e96e322](https://github.com/godwit-email/godwit/commit/e96e322127bb51815d470a940fe1d9ee97f65114))
* **ai:** interrupt and resume approvals and questions on pi ([#233](https://github.com/godwit-email/godwit/issues/233)) ([#247](https://github.com/godwit-email/godwit/issues/247)) ([9ce3ada](https://github.com/godwit-email/godwit/commit/9ce3ada2646d47d551f7bf34d4c3aabb880525f8))
* **ai:** run new Wit conversations on pi-agent-core with read-only tools ([#232](https://github.com/godwit-email/godwit/issues/232)) ([#244](https://github.com/godwit-email/godwit/issues/244)) ([77dc804](https://github.com/godwit-email/godwit/commit/77dc804254aac289ff2e7241799e2483dc324775))
* **ai:** run one-off generation on pi for every model service kind ([#230](https://github.com/godwit-email/godwit/issues/230)) ([#242](https://github.com/godwit-email/godwit/issues/242)) ([7490bb4](https://github.com/godwit-email/godwit/commit/7490bb4e81464bf6376ad94cca42fadebd2358e6))
* **ai:** run plugin models and judges on pi behind the Labs switch ([#231](https://github.com/godwit-email/godwit/issues/231)) ([#245](https://github.com/godwit-email/godwit/issues/245)) ([cda9097](https://github.com/godwit-email/godwit/commit/cda9097d167ce5d1b879e1be7ef6bc1c5eca02b4))
* **ai:** run the compose window's Assist on pi behind the Labs switch ([#235](https://github.com/godwit-email/godwit/issues/235)) ([#249](https://github.com/godwit-email/godwit/issues/249)) ([dd81a59](https://github.com/godwit-email/godwit/commit/dd81a597ebac037635af86cc732f8fe707bc49aa))
* **ai:** run the mail summary on pi + moejs behind a Labs switch ([#229](https://github.com/godwit-email/godwit/issues/229)) ([#240](https://github.com/godwit-email/godwit/issues/240)) ([347cbcb](https://github.com/godwit-email/godwit/commit/347cbcb0fad62ee9fecae58d978f16d593cce89e))
* **ai:** save every conversation as pi messages and convert old ones at startup ([#234](https://github.com/godwit-email/godwit/issues/234)) ([#246](https://github.com/godwit-email/godwit/issues/246)) ([a61f7b3](https://github.com/godwit-email/godwit/commit/a61f7b3ea060bee00d7b60889ffc4d4bedba7f0f))
* **plugins:** run every script plugin on moejs and delete goja ([#248](https://github.com/godwit-email/godwit/issues/248)) ([ea2cc67](https://github.com/godwit-email/godwit/commit/ea2cc67acbd617ec10b0db6462f9d42ac50c05ab))
* **plugins:** run translate script plugins on moejs behind the Labs switch ([#237](https://github.com/godwit-email/godwit/issues/237)) ([#243](https://github.com/godwit-email/godwit/issues/243)) ([ec39319](https://github.com/godwit-email/godwit/commit/ec39319cb85ee6490d32b775e436fb16e9e2458f))

## [1.14.0](https://github.com/godwit-email/godwit/compare/v1.13.0...v1.14.0) (2026-10-24)


### Features

* **ai:** drop AI cache outputs unused for 30 days ([#222](https://github.com/godwit-email/godwit/issues/222)) ([6b6c13d](https://github.com/godwit-email/godwit/commit/6b6c13d44f4b4c403a8da37e12bf719565923678))
* **outbox:** undo send, retry by SMTP reply, hand back mail queued too long ([#227](https://github.com/godwit-email/godwit/issues/227)) ([0a5e26d](https://github.com/godwit-email/godwit/commit/0a5e26de5c875675d6614ced422fc190d431d6e1))
* **reader:** one-click unsubscribe by RFC 8058 headers only ([#225](https://github.com/godwit-email/godwit/issues/225)) ([ef5cce9](https://github.com/godwit-email/godwit/commit/ef5cce972d405bd36ca0a8924b8d1f8784a9056d))


### Bug Fixes

* **wit:** answer at the tool-call limit; summaries of mail are data ([#223](https://github.com/godwit-email/godwit/issues/223)) ([cafda00](https://github.com/godwit-email/godwit/commit/cafda00d1cf84f37683bdaac7f62fafa5a4efa43))

## [1.13.0](https://github.com/godwit-email/godwit/compare/v1.12.1...v1.13.0) (2026-10-24)


### Features

* **mcp:** MCP Apps show tools and sending through godwit mcp ([#200](https://github.com/godwit-email/godwit/issues/200)) ([7b901c1](https://github.com/godwit-email/godwit/commit/7b901c10e11a44fb5003df2070eb6a2892ba1489))
* **settings:** redesign the About page around the 3D bird ([#195](https://github.com/godwit-email/godwit/issues/195)) ([843840c](https://github.com/godwit-email/godwit/commit/843840ce1a8d7f221e526adb83fc3139b34634fb))
* **wit:** ask_user_question lets the model ask the user in an Ask run ([#203](https://github.com/godwit-email/godwit/issues/203)) ([599a4a6](https://github.com/godwit-email/godwit/commit/599a4a658235d5fc6d874f41ce1e4dfdc64e2caa))


### Bug Fixes

* **ai:** give models only the mail text the reader shows ([#209](https://github.com/godwit-email/godwit/issues/209)) ([0366ce4](https://github.com/godwit-email/godwit/commit/0366ce44275f0c99cd7fccfbc85769c19eabef9e))
* **ai:** let a nil *Error format without a nil dereference ([#205](https://github.com/godwit-email/godwit/issues/205)) ([5ef1756](https://github.com/godwit-email/godwit/commit/5ef175618b4cb9e62e3870ade18b26e72669e279))

## [1.12.1](https://github.com/godwit-email/godwit/compare/v1.12.0...v1.12.1) (2026-10-24)


### Bug Fixes

* **wit:** a resumed run sends the summary its first request sent ([#188](https://github.com/godwit-email/godwit/issues/188)) ([7065974](https://github.com/godwit-email/godwit/commit/7065974c471634542ab82bdf3b3797796faa98aa)), closes [#176](https://github.com/godwit-email/godwit/issues/176)
* **wit:** cancel a draft's runs before deleting or sending it ([#193](https://github.com/godwit-email/godwit/issues/193)) ([389d4c7](https://github.com/godwit-email/godwit/commit/389d4c78bf91cf0bf50485e9700974dd9ac85db9)), closes [#178](https://github.com/godwit-email/godwit/issues/178)
* **wit:** close the open calls a failed run leaves before the next question ([#191](https://github.com/godwit-email/godwit/issues/191)) ([3ee6a39](https://github.com/godwit-email/godwit/commit/3ee6a3972cda891084552fa0816966a1de69dda8))
* **wit:** compact a conversation one stretch at a time within the model's budget ([#186](https://github.com/godwit-email/godwit/issues/186)) ([1dfdec8](https://github.com/godwit-email/godwit/commit/1dfdec8352aec416d497b425a72b60fac1a61f0c)), closes [#173](https://github.com/godwit-email/godwit/issues/173)
* **wit:** drop the conversation a ⌘K rewrite made when its run fails to start ([#189](https://github.com/godwit-email/godwit/issues/189)) ([1f5690b](https://github.com/godwit-email/godwit/commit/1f5690bd4885478343eec4b68743ac02f8bc4ac8)), closes [#179](https://github.com/godwit-email/godwit/issues/179)
* **wit:** keep large integers in tool calls and results exact across runs ([#185](https://github.com/godwit-email/godwit/issues/185)) ([8153c91](https://github.com/godwit-email/godwit/commit/8153c91c1d63f6af4588be02d099c2a3b11c8835)), closes [#172](https://github.com/godwit-email/godwit/issues/172)
* **wit:** keep the last two turns whole when a kept summary reaches into them ([#183](https://github.com/godwit-email/godwit/issues/183)) ([61ca57e](https://github.com/godwit-email/godwit/commit/61ca57e1ab4e1a5f59132e10599199165aa19f4b)), closes [#170](https://github.com/godwit-email/godwit/issues/170)
* **wit:** read a compaction's history after its summary epoch ([#190](https://github.com/godwit-email/godwit/issues/190)) ([c0137e0](https://github.com/godwit-email/godwit/commit/c0137e043968dc8e4e3b2f376223c6eee6d56160)), closes [#175](https://github.com/godwit-email/godwit/issues/175)
* **wit:** use the summary when asking again right after its end ([#184](https://github.com/godwit-email/godwit/issues/184)) ([3d4b5ba](https://github.com/godwit-email/godwit/commit/3d4b5ba648489072f0a42de6a4be1b675f81fcaa)), closes [#171](https://github.com/godwit-email/godwit/issues/171)

## [1.12.0](https://github.com/godwit-email/godwit/compare/v1.11.0...v1.12.0) (2026-10-23)


### Features

* redraw the godwit logo and fly an origami godwit through the site hero ([#181](https://github.com/godwit-email/godwit/issues/181)) ([641516b](https://github.com/godwit-email/godwit/commit/641516bce9d9a1cd65855a6801e0dc3d6f0a2d28))
* **settings:** draw sections as borderless filled blocks ([#169](https://github.com/godwit-email/godwit/issues/169)) ([8bf8612](https://github.com/godwit-email/godwit/commit/8bf86122461635510057ebaa544ef156cccd826d))
* **wit:** say how long an answer worked on its steps line ([#166](https://github.com/godwit-email/godwit/issues/166)) ([8dccf25](https://github.com/godwit-email/godwit/commit/8dccf25071f25cc1f2569ed5d5e4b5d11ec235fc))


### Bug Fixes

* **reader:** stop table columns in HTML mail shrinking to one letter ([#168](https://github.com/godwit-email/godwit/issues/168)) ([4b7a466](https://github.com/godwit-email/godwit/commit/4b7a46615b0b95818443355ea534e319f50f15f7))

## [1.11.0](https://github.com/godwit-email/godwit/compare/v1.10.0...v1.11.0) (2026-10-23)


### Features

* **settings:** reorder accounts by dragging them in the settings sidebar ([#150](https://github.com/godwit-email/godwit/issues/150)) ([c39aaed](https://github.com/godwit-email/godwit/commit/c39aaed7122d6925aebe8e56c625713eca152db4))
* **wit:** redesign the Wit page (design canvas + [#154](https://github.com/godwit-email/godwit/issues/154)–[#163](https://github.com/godwit-email/godwit/issues/163)) ([#164](https://github.com/godwit-email/godwit/issues/164)) ([5fdf72a](https://github.com/godwit-email/godwit/commit/5fdf72a1a4672c6536468286153f4b57fce5ad48))
* **wit:** size history to the model's context window and compact it with a summary ([#152](https://github.com/godwit-email/godwit/issues/152)) ([e8ed318](https://github.com/godwit-email/godwit/commit/e8ed31886269333cf2d2aaaaaef24938a05e35b5))


### Bug Fixes

* **compose:** keep a change's accept/reject buttons reachable on hover ([#148](https://github.com/godwit-email/godwit/issues/148)) ([abc0a12](https://github.com/godwit-email/godwit/commit/abc0a122d3d6b5966e9a17dcbe22fbfedbc48e6e))
* **sync:** replace invalid UTF-8 in message bodies so a folder's sync can't fail ([#165](https://github.com/godwit-email/godwit/issues/165)) ([e1eb9d5](https://github.com/godwit-email/godwit/commit/e1eb9d5555a0ef1c5d96d1f7d13e6da37f0d8c0a))

## [1.10.0](https://github.com/godwit-email/godwit/compare/v1.9.0...v1.10.0) (2026-10-22)


### Features

* **wit:** tell the model the user's local time ([#145](https://github.com/godwit-email/godwit/issues/145)) ([26fd2cb](https://github.com/godwit-email/godwit/commit/26fd2cbac20f4df5d739d4db0753bb3a54e01a2e))

## [1.9.0](https://github.com/godwit-email/godwit/compare/v1.8.1...v1.9.0) (2026-10-21)


### Features

* **account:** add customizable account icons and transparent default icon ([#127](https://github.com/godwit-email/godwit/issues/127)) ([95e3445](https://github.com/godwit-email/godwit/commit/95e34454d79e942f29d119900313e2b8031cd7ec))
* **ai:** integrate Apple Intelligence as builtin plugin for small tasks and classification ([#136](https://github.com/godwit-email/godwit/issues/136)) ([b4642f2](https://github.com/godwit-email/godwit/commit/b4642f2d90dc3f5edafb7df3daf1f37621da06fa))
* **ai:** show long mail links to models as godwit://link references ([#135](https://github.com/godwit-email/godwit/issues/135)) ([b1b7ba0](https://github.com/godwit-email/godwit/commit/b1b7ba0549b0b0ce74c3f971be3a316b84c9d52e))
* **auth:** add Lark Mail as Feishu's overseas provider ([#138](https://github.com/godwit-email/godwit/issues/138)) ([633686a](https://github.com/godwit-email/godwit/commit/633686abb58b80567184d5da9bc4f164156a8212))
* **compose:** add underline, strikethrough, font controls, and autocorrect preservation ([#130](https://github.com/godwit-email/godwit/issues/130)) ([4a3f820](https://github.com/godwit-email/godwit/commit/4a3f8204ce2256c02244e611373b16a5bc204a4b))
* **signature:** shared signatures and an HTML mode for the editor ([#140](https://github.com/godwit-email/godwit/issues/140)) ([8725b95](https://github.com/godwit-email/godwit/commit/8725b9572cc338bc6eaa1f0a20d7803835712673))
* **signin:** recognize codes and buttons by how a code mail looks ([#141](https://github.com/godwit-email/godwit/issues/141)) ([2cae26c](https://github.com/godwit-email/godwit/commit/2cae26cf6d567749938e4fad88391a68b031443f))
* **wit:** add format skill to structure drafts without altering wording ([#129](https://github.com/godwit-email/godwit/issues/129)) ([5ff2e59](https://github.com/godwit-email/godwit/commit/5ff2e597d7e8d0b0a42cf600aba10571c33e7ad2))
* **wit:** support email subject in draft assist, translation, and review diffs ([#131](https://github.com/godwit-email/godwit/issues/131)) ([e58d30f](https://github.com/godwit-email/godwit/commit/e58d30f5d073c3b206332f3f0a2ee5cb068c6b0c))


### Bug Fixes

* **translate:** query installed macOS translation languages dynamically and pin active app language ([#132](https://github.com/godwit-email/godwit/issues/132)) ([c4b76a9](https://github.com/godwit-email/godwit/commit/c4b76a9ee0c5fd25610bafbcb755f3cc3e1c1fd4))
* **ui:** align and clean up wit compose action tray ([#126](https://github.com/godwit-email/godwit/issues/126)) ([a3d2d9f](https://github.com/godwit-email/godwit/commit/a3d2d9f6955fa3b7c4b6c73f9a2759d5440d5cc2))

## [1.8.1](https://github.com/godwit-email/godwit/compare/v1.8.0...v1.8.1) (2026-10-20)


### Bug Fixes

* **ai:** default omitted predicate questions to no in gen judge ([#124](https://github.com/godwit-email/godwit/issues/124)) ([6e0b48a](https://github.com/godwit-email/godwit/commit/6e0b48a2ffd4f12f738d02348ab19e25ab6e21f1))

## [1.8.0](https://github.com/godwit-email/godwit/compare/v1.7.1...v1.8.0) (2026-10-19)


### Features

* translate mail with macOS system translation ([#122](https://github.com/godwit-email/godwit/issues/122)) ([0bebd7c](https://github.com/godwit-email/godwit/commit/0bebd7cdeccbf9151ba962128cbfe1d4cc4f3730))

## [1.7.1](https://github.com/godwit-email/godwit/compare/v1.7.0...v1.7.1) (2026-10-19)


### Bug Fixes

* **ui:** color swatches in one row across the field ([#120](https://github.com/godwit-email/godwit/issues/120)) ([f85244b](https://github.com/godwit-email/godwit/commit/f85244bfcfc92a5ce61f76e95c2c317dcfbe1ffc))

## [1.7.0](https://github.com/godwit-email/godwit/compare/v1.6.0...v1.7.0) (2026-10-19)


### Features

* **i18n:** add German (de) translation ([#113](https://github.com/godwit-email/godwit/issues/113)) ([f2dd9b6](https://github.com/godwit-email/godwit/commit/f2dd9b68ee86969511874ae109d640ae9d819a96))
* **i18n:** complete mainstream LTR language support across client and core ([#115](https://github.com/godwit-email/godwit/issues/115)) ([231fa4d](https://github.com/godwit-email/godwit/commit/231fa4d84dad893ad362bd6fc38edf6d0f7296d6))
* local StartLux judge: registry model assets and the native plugin ([#90](https://github.com/godwit-email/godwit/issues/90)) ([53df493](https://github.com/godwit-email/godwit/commit/53df4931044ce12b3131453bc9ff6bd78d4f9517))
* plugin models through Godwit's SDKs (replaces [#108](https://github.com/godwit-email/godwit/issues/108)) ([#109](https://github.com/godwit-email/godwit/issues/109)) ([95eb018](https://github.com/godwit-email/godwit/commit/95eb0180654353e4e0a026a33258d6f9f0c6fb54))
* twelve label colors from Tailwind's palette ([#119](https://github.com/godwit-email/godwit/issues/119)) ([e1e4b19](https://github.com/godwit-email/godwit/commit/e1e4b19f25ee3481e86ad1fc4522b7c2ad190837))
* **wit:** model picker, reasoning, regenerate/edit, context meter ([#112](https://github.com/godwit-email/godwit/issues/112)) ([6cba34d](https://github.com/godwit-email/godwit/commit/6cba34d8fb11c6c87219a776d7effe5846ebc40f))
* **wit:** reasoning effort per conversation; Anthropic and Responses show their reasoning ([#118](https://github.com/godwit-email/godwit/issues/118)) ([24f16c2](https://github.com/godwit-email/godwit/commit/24f16c2ef73b683e25e45df1e7b85329f3d1f620))


### Bug Fixes

* **imap:** tolerate missing space after empty literal in responses ([#114](https://github.com/godwit-email/godwit/issues/114)) ([a264c15](https://github.com/godwit-email/godwit/commit/a264c15a36070a4d76f6346bbba72cb0e879a9fe))

## [1.6.0](https://github.com/godwit-email/godwit/compare/v1.5.0...v1.6.0) (2026-10-18)


### Features

* publish the plugin types as @godwit/sdk ([#110](https://github.com/godwit-email/godwit/issues/110)) ([26f0cb3](https://github.com/godwit-email/godwit/commit/26f0cb35d9386dd33de6713c2827c18210b6ad41))


### Bug Fixes

* fold only folders that map onto a sidebar row ([#106](https://github.com/godwit-email/godwit/issues/106)) ([064de01](https://github.com/godwit-email/godwit/commit/064de010e0b33619f1914187b7caff92b62bc162))
* **ui:** hairline cards and dividers; Wit page tints the material ([#105](https://github.com/godwit-email/godwit/issues/105)) ([6f44bdc](https://github.com/godwit-email/godwit/commit/6f44bdc3a89fe4d639daca488b828e0bc5eda175))

## [1.5.0](https://github.com/godwit-email/godwit/compare/v1.4.0...v1.5.0) (2026-10-17)


### Features

* account tree sidebar with nested folders ([#102](https://github.com/godwit-email/godwit/issues/102)) ([821fad1](https://github.com/godwit-email/godwit/commit/821fad1323de2496d23a1598d2c08290814b0d3a))
* per-account sync status in the top bar ([#92](https://github.com/godwit-email/godwit/issues/92)) ([6041968](https://github.com/godwit-email/godwit/commit/6041968a9dbc4a229baff04c42a1ef469ce3ea64))
* recognize sign-in codes and links in 14 languages through per-language lexicons ([#99](https://github.com/godwit-email/godwit/issues/99)) ([0c0dd54](https://github.com/godwit-email/godwit/commit/0c0dd5450d217582481ca869bba5de4045661412))
* sign-in codes and links: a built-in plugin with a banner, tray list and subject tags ([#96](https://github.com/godwit-email/godwit/issues/96)) ([64d45ec](https://github.com/godwit-email/godwit/commit/64d45ec3d513f14f14e922740b472da4cbf73f70))


### Bug Fixes

* a rescan keeps model labels until the judge answers again ([#100](https://github.com/godwit-email/godwit/issues/100)) ([06805e9](https://github.com/godwit-email/godwit/commit/06805e99b61164385940e6d46773785db77214ad))
* relaunch after an update instead of only quitting ([#93](https://github.com/godwit-email/godwit/issues/93)) ([62b7e70](https://github.com/godwit-email/godwit/commit/62b7e7052eeb8c41c8470b54b07308f7271a3c57))
* the banner's Copy and Open buttons are icons alone ([#103](https://github.com/godwit-email/godwit/issues/103)) ([7d5901d](https://github.com/godwit-email/godwit/commit/7d5901d5715e78785a32ad1a64a546cb95f1d7ba))
* **ui:** lighter window material and the panels' shadow, as the design ([#101](https://github.com/godwit-email/godwit/issues/101)) ([9ad23f9](https://github.com/godwit-email/godwit/commit/9ad23f91abcc2355db380d88a0b96f3a6a2b567a))

## [1.4.0](https://github.com/godwit-email/godwit/compare/v1.3.1...v1.4.0) (2026-10-16)


### Features

* signed macOS sender-avatar notifications ([#89](https://github.com/godwit-email/godwit/issues/89)) ([63c7ff1](https://github.com/godwit-email/godwit/commit/63c7ff1922b660ab3235cda01f2827e780e9f233))


### Bug Fixes

* **ci:** allow builds to read draft releases ([#87](https://github.com/godwit-email/godwit/issues/87)) ([5fa894f](https://github.com/godwit-email/godwit/commit/5fa894fe1349260532f4ba5d0eb562b744dd377d))

## [1.3.1](https://github.com/godwit-email/godwit/compare/v1.3.0...v1.3.1) (2026-10-16)


### Bug Fixes

* default decision threshold to 80% with a 50–95% range ([#84](https://github.com/godwit-email/godwit/issues/84)) ([99f1dbd](https://github.com/godwit-email/godwit/commit/99f1dbd9d0a656486537c44bb00b4d5815ab0a67))

## [1.3.0](https://github.com/godwit-email/godwit/compare/v1.2.0...v1.3.0) (2026-10-15)


### Features

* make the decision threshold configurable ([#82](https://github.com/godwit-email/godwit/issues/82)) ([7e7fd22](https://github.com/godwit-email/godwit/commit/7e7fd2250264cba58ccd4182bc9b0dfad09e4464))
* **ui:** refine account inputs and sidebar transitions ([#78](https://github.com/godwit-email/godwit/issues/78)) ([aae89b6](https://github.com/godwit-email/godwit/commit/aae89b68a67377f52fe9135b41e395eba1013dab))


### Bug Fixes

* **notify:** send a separate notification for each new message ([#79](https://github.com/godwit-email/godwit/issues/79)) ([fb54353](https://github.com/godwit-email/godwit/commit/fb54353c2f52e00fdaeb642a1033598f4570c639))
* **plugins:** read the plugin registry forward-compatibly ([#81](https://github.com/godwit-email/godwit/issues/81)) ([360cc40](https://github.com/godwit-email/godwit/commit/360cc40b2ce28fcb7b9f1b38c6971a82ac969f91))

## [1.2.0](https://github.com/godwit-email/godwit/compare/v1.1.0...v1.2.0) (2026-10-15)


### Features

* **plugins:** native plugins, a signed plugin directory, and translation through plugins ([#68](https://github.com/godwit-email/godwit/issues/68)) ([40bf3d7](https://github.com/godwit-email/godwit/commit/40bf3d776aa7f2cf3f97e91cdce777294be11e29))
* upgrade MyGo to 0.5.1 and improve mail rendering and i18n ([#74](https://github.com/godwit-email/godwit/issues/74)) ([1a0266f](https://github.com/godwit-email/godwit/commit/1a0266fde12ad9bb30fe2c680cb6db62c3cd9b68))


### Bug Fixes

* **ci:** select the standalone MSIX publisher with the package path ([#72](https://github.com/godwit-email/godwit/issues/72)) ([89b78a0](https://github.com/godwit-email/godwit/commit/89b78a04f54eedbb9cd691b26713a582deb6c99d))
* **ci:** use the supported Store publish input directory ([#70](https://github.com/godwit-email/godwit/issues/70)) ([a6dfb21](https://github.com/godwit-email/godwit/commit/a6dfb2112014d2759c8ca9919f0b9b3bfff2eb04))
* **dev:** isolate development data from production ([#75](https://github.com/godwit-email/godwit/issues/75)) ([8eaa551](https://github.com/godwit-email/godwit/commit/8eaa551bcfaa8e7ccfb7d2f200cadb8d43d40b91))
* **plugins:** show install progress and clarify plugin settings ([#77](https://github.com/godwit-email/godwit/issues/77)) ([60051bd](https://github.com/godwit-email/godwit/commit/60051bda9e846ef0815a083a4d515a54faf066bd))

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
