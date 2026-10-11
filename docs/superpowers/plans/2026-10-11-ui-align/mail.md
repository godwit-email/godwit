# Mail window + tokens: design vs code
Design = scratchpad/design-v2/project (Main.dc.html L = line). Code = /Users/baran/Documents/workspace/godwit.
Format: design | code | change. Matching items omitted.

## 1. Tokens (ds/godwit/tokens.json vs internal/theme/theme.go + internal/palette/palette.go)
All 33 colour tokens, both themes, resolve to the same values (neutral/green/red/amber/blue Tailwind v4 via OKLCH; window = Mid(100,200)/(900,800) = #ededed/#1e1e1e). Radius scale, spacing (Space(n)=4n), shadow xs..xl + float, sidebar 248, rail 56, reader 720, attachment 260 all match.

- colour `highlight` #fff085 (both themes), search match <mark>, radius-xs, text stays foreground | code: no token (theme.go Tokens struct) and no highlighting anywhere | change: add `Highlight` (Yellow200 #fff085) to Tokens + palette; use it in search results (see 4).
- type scale line-height per step (xs 1.4, sm 1.45, base 1.5, body 1.6, lg 1.5, title 1.4, xl 1.35, 2xl 1.3, display 1.25) | code TextScale (theme.go:56) holds sizes only | change: none needed unless mygo exposes line height; note only.
- type weights (lg 500; title/xl/2xl/display 600; mono 500) | code: weights set per call site (e.g. app/shell.go:176 FontWeight(600)) | change: none, but mono uses `theme.Mono = "monospace"` (theme.go:~228) vs design "Geist Mono" 12px/500; Kbd uses Xs 11 mono.
- code-only sizes: TextScale.Brand (f+4=17) and Heading (f+6=19) not in design scale (design has 11/12/13/14/15/16/18/20/29) | change: confirm usage; fold into 16/18 or add to design.
- code-only colours: FloatInset, MarkTile (design documents float inset in shadow-float usage text only) | no change.
- layout const ListWidth = 360 (theme.go:~63) | design list column = 340 (Main L113 flex 0 0 340px) | change: ListWidth 340.
- TopBarHeight = 44 (theme.go) | design Windows strip = 40 (Main stripH, L472) | change: 40 when platform is windows (see 2).
- GLOBAL (decide): design root font-size 14px on every screen (Main windowStyle L470; sidebar items, list from/subject, toolbar buttons, names all inherit 14) | code default text = FontSize 13 = Text.Base (theme.go:mygoTheme th.FontSize=Text.Base) | change: either design is wrong vs tokens.json (base 13, body 14 for mail bodies only) or default UI size should be 14. Every "14 vs 13" below is this one question. Recommend: ask designer; tokens.json says 13.

## 2. Title bar / rail / panel (app/shell.go)
- Windows: strip 40 high, window+panel radius 8, bg #EEEEEE, border #D4D4D4, caption buttons 46x40 at right, padding-left 8, right 150, Segoe UI Variable | code: barH=max(44,TitleBar().Height) (shell.go:~124), panel Radius.Xl always (shell.go:~255), sidebar corner Xl-hairline | change: platform-aware barH (40) and panel/sidebar radius Lg(8) on Windows; needs a platform flag in App (a.plat?) - styling only.
- disabled back/forward colour #A3A3A3 (non-token) | code: Ghost disabled = 50% opacity of Muted (button.go) | change: none (design literal, closest token is ring/#a1a1a1).
- sync dot colour #16A34A in Main L45 | code StatusDot OK = Success #00a63e | change: none (design literal differs from its own token; token wins).
- title text weight 600 size 14 | code ui.Text(title).FontWeight(600) default 13 (shell.go:~143) | change: depends on GLOBAL 14 vs 13.
- Everything else (44 bar, 28 ghost buttons r6, separator 1x24 + 16 gap, sub 12 muted, rail 56/40 r8 gap 6 pad 4/0/12, icons 20 filled when selected, rail-selected tint, panel inset 0/8/8/0, r12, shadow-xs, outline) matches.

## 3. Sidebar (app/mail_sidebar.go, kit/surface.go, kit/collapsible.go)
- unified list: 全部收件箱, 已标星, 待回复 (note " · 智能视图" #A3A3A3 12px, count 3), 订阅与账单 (" · 智能视图", count 7) | code: only inbox + starred (mail_sidebar.go:~72-73) | FEATURE GAP: smart views.
- trash folder label: account folder list shows "垃圾箱" (Main L400, FOLDER_ICON also lists 废纸篓/已删除) | code zh `mail.folder.trash` = "废纸篓" (internal/i18n/zh.go:202), junk = "垃圾邮件" (design FOLDER_ICON agrees) | change: decide wording; low priority.
- folder icons are Remix outline/fill (inbox, draft, send-plane, archive, spam-2, delete-bin) | code Lucide-style names via icons pkg (file-pen, send, spam, trash-2) | change: project-wide icon set, not per-screen; skip unless icon set is being swapped.
- sidebar text 14 vs code 13: see GLOBAL.
- Else (248 wide, pad 12, gap 2, header 18/600 pad 4/6/10 + 28 search btn, Compose 32 high gap 10, 8/10 spacers, account header 30 high 12px muted chevron 12 rotate 90 shows count only when folded, folder indent 26, selected F5F5F5 + filled icon, Add account 30 high 12px muted icon 12) matches.

## 4. Mail list (app/mail_list.go, app/drafts.go, kit/tabs.go, kit/list.go)
- column width 340 | code 360 (theme.ListWidth) | change: 340 (tokens section).
- list heading is a date-group label "今天" 13/500 muted (Main L137, listTitle) | code shows the view title (e.g. 全部收件箱) in same style (mail_list.go:~lH) | FEATURE GAP: rows grouped by date (今天/昨天/本周...). Until then keep title.
- 全部/未读 toggle: container pad 2, r8, bg F5F5F5, buttons h26 pad 0 10 r6 12px weight 400, active white + shadow 0 1px 2px .06 | code kit.Tabs: pad 3 (Space .75), tab h26, r6, FontWeight 500, size 13 (kit/tabs.go:~13-17), shadow-sm | change: tab font Sm(12), weight 400, container pad 2 (Space 0.5); kit.Tabs is shared - check Settings usage.
- row tag chip (验证码/航班/账单; drafts: 编辑中): align-self start, mt 2, pad 0 6, r4, border 1px #E5E5E5, #404040, 12px | code row (mail_list.go:~row) has none | FEATURE GAP for plugin tags (needs card/plugin API returning a tag per message); 编辑中 is part of drafts gap.
- row extras in code not in design: paperclip icon, thread count | keep or drop - ask.
- rows: design rows touch (no gap); code List Gap(hairline) | negligible.
- time text "10/02" zero-padded in Main list, but "9/28" and "10/03" in results (design itself inconsistent) | code "M/D" | no change.
- SEARCH panel (Main L114-134):
  - input: bordered box h32, pad 0 4 0 10, r8, border #A1A1A1 (=ring) + 3px ring 50%, leading search icon 16 muted, trailing clear (x 14) 24x24 button, placeholder "搜索邮件、联系人和附件" | code kit.Input in column pad 12/16/8, placeholder "搜索邮件" (zh.go:209), no icon, no clear button (mail_list.go:~listHeader) | change: copy "搜索邮件、联系人和附件"; Bare input inside a row with icon + clear IconButton; container pad 12/12/8 gap 8.
  - filter chips row: 全部账户 / 发件人 / 时间, each h24 r6 border #E5E5E5 12px with chevron-down 12, menus (aria-haspopup) | code none | FEATURE GAP: search filters (account/sender/time).
  - hint 12 muted: "找到 N 封 · 全部账户 · 只搜索已同步到本机的邮件" / empty "没有结果 · 只搜索已同步到本机的邮件" | code static "只搜索已同步的邮件" (zh.go:210) | change: copy + result count + scope (count/scope parts need no new backend: len(p.results), account name).
  - "让 Wit 帮你找：「query」" row: mx 8 mb 4, pad 8/12, r8, bg #FAFAFA(sidebar), 12px, sparkles 14 muted, links to Ask | code none | FEATURE GAP: hand search query to Wit.
  - result rows: pad 8/12 (no unread gutter), from 400 weight + time 12, subject, preview 12 muted, folder line "收件箱 · you@example.com" 12 muted; sent shown as "我 → Lena Fischer"; matches in subject+preview wrapped in <mark> pad 0 2, r2, #FFF085, text foreground | code reuses p.row (unread dot gutter, bold unread, no folder line, no highlight) | change: dedicated result row + highlight (needs Highlight token; mygo text spans or split Text runs); folder line needs folder/account name lookup (p.folders, a.accounts: available); "我 →" sent format needs To names (MessageSummary has To).
  - empty state: Empty component bare, pad 32/12; 40x40 r8 F5F5F5 tile with search icon 20; title 16/600 `没有找到「Q」`; desc 14 muted lh1.6 max 400 "只搜索了已同步到本机的邮件（每个文件夹最近 2000 封）。换个关键词，或者让 Wit 帮你找。"; actions: primary "让 Wit 帮你找" + outline "扩大同步范围" (h32 r8 gap 8) | code plain muted text "没有找到邮件" (mail_list.go:~538) | change: add kit.Empty (icon tile, title, desc, actions) - also needed for empty list/drafts/outbox. "扩大同步范围" = FEATURE GAP (sync range setting action); Wit button = same gap as above.
  - with empty search the reader pane shows a 96px bird watermark (#E5E5E5, Main L164, noSelection) | code reader renders nothing when no selection (reader.go:~view early return) | change: bird illustration asset in empty reader pane (also shown when nothing selected? design only for search-empty; recommend both).
- DRAFTS list (MainDrafts, folder=drafts):
  - heading "草稿 · you@example.com" | code "草稿" (Title(), mail_list.go:~Title) | change: append account email for drafts view.
  - rows identical to inbox rows: pad 10/10/10/20, from=recipient short name ("（无收件人）" when none), time, subject ("（无主题）"), preview line 13 muted, selected F5F5F5, "编辑中" chip | code drafts.go rows: pad 10 all sides, 2 lines (recipient+time, subject), no preview, no selection (click opens compose window, drafts.go:~38), no chip | change: reuse p.row layout; add preview (core.Draft has body? check), selection state, chip. Click-opens-compose vs select-then-detail is a behaviour change -> see Feature gaps.

## 5. Reader chrome (app/reader.go, app/reader_ai.go)
- toolbar: pad 8/16, bottom border #E5E5E5, gap 8, wrap | code pad Space(2)/Space(4), hairline border-bottom, gap 4 (reader.go:~572) | change: gap Space(2) between groups.
- action groups (Main L170): two segmented groups, each h32, pad 0 2, border 1px #E5E5E5, r8, white; inside 28x28 r6 icon-only buttons (16 icon), 1x16 separators (margin 0 2) between; group "回复": reply, reply-all, forward (forward = mirrored reply); group "整理": archive, delete, move-to-junk; tooltips (dark primary, r6, pad 4/8, 12px, shadow-md, 6px below) | code: flat row of outline text buttons "回复", "转发", "归档", "删除" + ghost icon reply-all (reader.go:572-577) | change: build a `kit.ButtonGroup` (bordered container + separators) of IconButtons with tooltips; icon-only per design.
- 移到垃圾邮件 button | code: no junk op (mail_list.go write() has read/unread/star/unstar/archive/delete only) | FEATURE GAP: Move to junk.
- code-only toolbar buttons: star toggle, mark unread, "…" menu (view in browser, export .eml) | design toolbar has none of them | change: ask - keep (hidden features otherwise lost) or move into a "…" menu; design silent.
- 总结 / 翻译 / 更多 skill ▾: h30, pad 0 10, r8, border #E5E5E5, white, gap 6, icons 14 (file-text / languages) before label on 总结/翻译, ml auto gap 4; ON state = border #171717 + bg #F5F5F5 (text unchanged) | code: Outline Sm (h28); ON = filled Default (black bg, white text) (reader_ai.go:~364-376), no icons | change: ON style -> Outline with Primary border + Hover bg; h30; add icons at 14 (ButtonOpts.Icon renders 16 - add size); More skill button same height.
- article: pad 20/28, max 720, gap 14, subject 20/600 lh 1.35 | code matches (reader.go:551-553).
- sender row: avatar 32 circle bg F5F5F5 fg #171717 600 13px | code kit.Avatar initial FontSize Sm 12 (display.go:~95) | change: avatar initial 13 (Base) - minor.
- sub line "发给 我 · 今天 09:41" (relative: 今天/昨天/周一 hh:mm, "10 月 2 日") | code `reader.to` + Format("2006-01-02 15:04") (reader.go:~660) | change: relative/localized date formatter (reuse listTime logic with time).
- summary panel: border #E5E5E5 r12 bg #FAFAFA pad 12/14 gap 8; header = sparkles 14 + "总结" 600 + muted 12 "summarize · [MODEL]"; bullets as <ul> indent 18 gap 4 lh 1.65 | code r.panel (reader_ai.go:488): Xl/Sidebar/hairline/pad 12,14 (match) but title is single bold string "总结 · summarize · model" (zh.go:474), no sparkle, bullets drawn as "•" rows gap 8 | change: split title into bold "总结" + Sm muted "summarize · model" + sparkles icon 14; bullet indent 18.
- translated notice: row gap 8, 12px muted, languages icon 14, text "已翻译为中文 · translate · [MODEL]", then underlined 12px link-style "显示原文" in foreground | code Sm muted text + Ghost Sm button (28 high) (reader_ai.go:~432) | change: icon + kit Link-style button, 12px, underline, foreground colour (kit Link variant is muted until hover).
- plugin card (OTP/flight/receipt): border #E5E5E5 r12 pad 12/14, row gap 16, shadow 0 1px 2px .04; left col: "label · 来自插件 plugin" 12 muted, VALUE in Geist Mono 22px letter-spacing 3, hint 12 muted; right: primary button h32 r8 weight 500 | code (reader_ai.go:~526): column, Sm muted title, label: value rows 13, buttons in a row below (first Default, rest Outline Sm), no shadow | change: horizontal layout with action button right; when a card has one prominent field render mono 22 letter-spaced; add shadow-xs-ish. Mapping of "value" vs "hint" fields onto core.CardField{Label,Value} is unspecified (hint = second field?) - may need a schema hint (e.g. field Role) - FEATURE GAP (small).
- remote-image notice: row pad 8/12 r8 gap 12 13px, bg #FFFBEB border #FCD34D text #92400E; text "已阻止 N 张远程图片，防止发件人追踪你是否打开了邮件"; two underlined text actions "加载图片" and "始终信任此发件人" | code kit.Alert Warning (uses WarningBg/Border/Text tokens - correct, design literals are old Tailwind v3 amber) with one Ghost Sm button "加载图片" (reader.go:686-694) | change: actions -> underlined text links in warning-text colour; add "始终信任此发件人" = FEATURE GAP (per-sender remote-image allowlist; check core for existing trusted-senders setting).
- attachment: 260 w, pad 8/10 (match), badge 28x34 r4 F5F5F5 label 10px muted, name 14, size 12 | code ext label Xs=11 (reader.go:723); extra download IconButton (not in design) | change: badge text 10 (minor); keep download button or ask.
- quick replies: mt 8, pt 16, border-top 1px #F5F5F5 (hover token, fainter than border), gap 8; header: sparkles 14 + "即时回复 · 选一封，进入撰写页再改" 12 muted gap 6; each item pad 10/12 r8 border #E5E5E5 gap 10: kind chip (确认/改签/同意/同意但调整/稍后答复/收到; bg F5F5F5 r4 12px #404040 pad 0 6) + preview ellipsis + (first only) "Tab" in Geist Mono 12 #A3A3A3 plain text | code (reader_ai.go:574-583): border-top uses Border (#E5E5E5), no sparkle, no kind chip, "Tab" drawn as bordered kit.Kbd | change: border-top colour Hover token (or accept Border), sparkles icon, Tab as plain mono Sm muted text instead of Kbd box. Kind chip = FEATURE GAP (core.QuickReply{Preview, HTML} has no label; prompt must also return a short label).
- body line-height 1.75 / 14px / paragraph gap 10 (webview; out of scope, noted for web/src/reader CSS).

## 6. Drafts detail pane (MainDrafts) - FEATURE GAP (whole pane)
Design: selecting a draft shows (a) toolbar group "草稿": 继续编辑 (opens/switches to compose window), 发送 (disabled while it is being edited in a compose window, tooltip "正在撰写窗口里编辑，先在那里发送"), 删除草稿; right-aligned status 12 muted "草稿已保存 · 09:52" or "正在撰写窗口里编辑 · 草稿已保存 09:52"; (b) when editing, notice r8 bg F5F5F5 12px "这封草稿正在撰写窗口里编辑，下面是最近一次保存的内容。点「继续编辑」切回那个窗口。"; (c) subject 20/600; grid 收件人/主题/发件人 (48px muted label column, gap 4/12, 14px); divider; body (lh 1.75, gap 10); attachment 260 pad 8/12, badge 28x32 11px, size 412 KB.
Code: drafts.go only lists; click opens compose (OpenCompose); reader pane stays empty; delete is only in the row context menu (zh "删除草稿" exists).

## Feature gaps (need new behaviour, not just styling)
1. Smart views 待回复 / 订阅与账单 in sidebar.
2. Date-grouped list headings (今天...).
3. Search: filter chips (account/sender/time), result highlight, folder line + sent "我 → X", "让 Wit 帮你找" hand-off, "扩大同步范围" action, clear button.
4. Plugin tag chip on list rows.
5. Move-to-junk action.
6. "始终信任此发件人" for remote images.
7. Quick-reply kind label (model/prompt change).
8. Card hint/prominent-value semantics in plugin card schema.
9. Drafts: detail pane with toolbar/status/editing notice, select-then-open behaviour, "编辑中" tracking (needs compose-window-open state per draft), disabled send while editing.
10. Empty reader watermark (bird asset) and shared kit.Empty component (icon tile + title + desc + actions) used by search-empty / empty list / drafts / outbox.
11. Windows-specific chrome (strip 40, radius 8) needs a platform flag.
