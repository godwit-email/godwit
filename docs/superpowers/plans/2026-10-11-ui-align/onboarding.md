# Onboarding / AddAccountSheet / Toasts: design vs code

Layout skeleton (aside 440, 48/44 pad, gap 28, form column 520, pad 56/32, gap 22, sheet 560/760/r16/top 64/backdrop .28, toast pad 12/16, gap 10, r16, max 420, top 44+8) already matches. Copy (zh) matches except where listed.

## Global (affects every screen below)
- design: base font 14 (Sm 13, Xs 12), Heading 20, Display 30, Brand 20, sheet title 16 | code: theme.go:160 FontSize 13 -> Base 13, Sm 12, Xs 11, Heading 19, Display 29, Brand 17, Lg(sheet title) 15 | change: only if the app-wide base is meant to be 14 (other screens will say the same); otherwise leave.
- design: default button 36 high (primary/secondary actions), inputs 40 high | code: kit/button.go:41 default 32 (Lg=36), kit/input.go:38 Input 32 | change: onboarding `primary()` (onboarding.go:~395) and secondary full-width buttons use the 36 size (SizeLg); onboarding Inputs (email, secret, name) 40 high -> add a Height(th.Space(10)) override on those three, leave manual-server inputs at 32.

## Onboarding window: aside
- design: brand mark 48px, gap 8 to "Godwit" 20/600 | code: onboarding.go Content: theme.BrandLogo=40, Gap Space(2.5)=10, Text.Brand | change: BrandLogo 48, row Gap Space(2); brand text 20 (Brand = f+7 or accept 17).
- design: promise icons #404040 (neutral-700) | code: Foreground.Mix(Background,0.25) | change: ok if mix lands ~neutral-700; verify only.
- design: license text 12px #A3A3A3 (neutral-400) | code: Text.Xs, t.Muted (500) | change: lighter color (Muted is #737373); no token for 400, skip unless Ring (neutral-400) is acceptable.

## Step: pick (choose provider)
- design: detected badge: padding 1px 8px, bg #F5F5F5, text #404040 12px | code: kit/display.go:14 padding Space(0.25)/Space(1.5)=1/6, text Foreground | change: horizontal padding Space(2) for this badge (or all); text color Foreground.Mix(...) ~ neutral-700.
- design: email input is a 40-high box with the badge INSIDE it (right side, gap 8, pad 12, radius 6, shadow xs) | code: onboarding.go pickView puts the badge as a sibling Row item beside the input | change: render badge inside the input box (overlay/trailing slot) or accept; at minimum input 40 high.
- design: "或选择服务商" divider text 12px #A3A3A3 | code: Text.Xs, Muted | change: none needed beyond global font note.
- design: provider order Gmail, Outlook, 163, 126, QQ, 腾讯企业邮, 飞书, 钉钉, iCloud, 雅虎, AOL, 其他 | code: core/auth/providers.go:37-78 same order | match.
- design: yahoo fallback mark 'Y' | code: zh.go:106 provider.yahoo.mark "雅" | change: only shown when no brand logo; leave unless logo missing (icons.Brand("yahoo")).
- design: provider tile hover/pressed unspecified | code: kit.Pressable Bordered | match.

## Step: password
- design: instruction card border #F0F0F0 (lighter than E5E5E5), bg #FAFAFA | code: onboarding.go passwordView: Background t.Sidebar, Border t.Border (neutral-200) | change: Border t.Outline-ish/neutral-100 (use t.Hover as border color).
- design: steps are an <ol> with 20px indent, color #404040, gap 4 | code: "1. text" plain lines, gap Space(1) | match visually; hanging indent for wrapped lines is absent (minor).
- design: help link is a plain text link "打开 QQ 邮箱设置 ↗" 13px | code: Outline sm Button | change: kit.Link variant button, Text.Sm.
- design: secret label gap 6, help 12px muted | code: ui.Field (mygo) gap unknown; kit.Field uses Space(1.5)=6 | change: verify ui.Field gap = 6 (code uses ui.Field, not kit.Field).
- design: manual-server panel: 3-col grid: IMAP host (span 2) + "端口 · SSL" ; SMTP host + "端口 · SSL"; inputs 32 high, border #E5E5E5, labels 12px muted; NO security select, NO username field; opened by `<details>` "手动设置服务器" 13px muted | code: manualView: host / port(90) / security Select per protocol + username field, summary is a Ghost sm button with chevron | change: layout differs; design is simpler. Decide: keep security+username (functional) but relabel "IMAP 端口"->"端口", labels 12px muted; or treat as feature gap below.
- design: back link "← 换一个邮箱" muted text, no padding | code: kit.Button Link variant | check Link variant colour is Muted and has zero padding.

## Step: oauth
- design: pending dot 10px #A3A3A3 (neutral-400) | code: PendingDot 10, t.Muted | change: lighter (t.Ring is neutral-400).
- design: two buttons side by side: outline "重新打开浏览器" (+ demo primary, ignore) | code: one Outline sm button, centered | match; design buttons are h32 (sm) ok.
- design: no error state in mockup | code: Alert Danger + retry | keep (code-only).

## Step: checking / error
- design: ok icon bg #F0FDF4 (SuccessBg) fg #15803D (SuccessText=Green700) | code: Success.Alpha(.12) bg, Success (Green600) fg | change: bg t.SuccessBg, fg t.SuccessText.
- design: fail icon bg #FEF2F2 fg #B91C1C (Red700) | code: DangerBg ok, fg t.Danger (Red600) | change: fg t.DangerText.
- design: IMAP ok row detail "6 个文件夹" | code: checkRow ok branch gives detail only for settings | feature gap (needs folder count in CheckReport).
- design: row separator #F5F5F5 | code: t.Hover (neutral-100 = F5F5F5) | match.
- design: failed actions: primary "重新输入授权码" + outline "手动设置服务器", both 36 high pad 14 | code: default 32 buttons | change: 36 size.

## Step: done
- design: badge 36 circle bg #F0FDF4 fg #15803D, check glyph | code: DoneBadge 36, Success.Alpha(.12)/Success | change: SuccessBg / SuccessText (as above).
- design: subtitle "email · 6 个文件夹" | code: email only (doneView) | feature gap (folder count).
- design: sync options: "每个文件夹最近 2000 封（推荐）", "最近 3 个月", "全部" | code: onboarding.go syncLimits {2000,10000,100000}, zh.go:175-177 | change: copy+values differ; time-based ranges are a feature gap (SyncLimit is a message count). Minimum: relabel 100000 as "全部"?  (not equal semantically; flag to owner).
- design: name input pre-filled/placeholder "[你的名字]" | code: placeholder "你的名字" | match.
- design: sheet primary label "完成", window "开始使用" | code same | match.

## AddAccountSheet (mode=sheet)
- design: header title 16/600 + 28x28 radius-6 "×" close, header margin-bottom -6 (title->body gap 16) | code: kit/overlay.go:33-39 pad 20/24, title Text.Lg(15)/600, IconButton Ghost sm, body top pad 16 | match except font note; verify IconButton sm = 28.
- design: panel shadow 0 24 64 rgba(0,0,0,.18), border #E5E5E5 | code: ShadowLg (10/15/-3 .1) | change: heavier shadow only if wanted app-wide; dialogs use ShadowLg by token design — leave.
- design: sheet inner content has no left aside; steps identical | code: same body() | match.

## Toasts (Toasts, ToastKinds)
- design: shadow 0 4 16 rgba(10,10,10,.08) | code: kit/toast.go:41 ShadowLg (10/15/-3,4/6/-4 @.1) | change: use a lighter custom Elevation{{4,16,0,.08}} for toasts (or ShadowMd).
- design: text 14/500; code Text.Base(13)/500 | global font note.
- design: close button 20x20 r4 icon 14, colour = toast text colour (success/warn/error) or #737373 (plain) | code: TextColor(look.muted) = SuccessText etc, plain Muted | match.
- design: icons circle-check / triangle-alert / circle-x, 16px, plain none | code toastLook same | match.
- design: colours Green50/200/700, Amber50/Amber300/Amber800, Red50/Red200/Red700, plain white/E5E5E5/0A0A0A | code toastLook uses SuccessBg/Border/Text, WarningBg/Border/Text, DangerBg/Border/Text | match (palette Amber300, Amber800 verified).
- design: placement top 52 centered, newest on top, gap 8 | code: padding TopBarHeight+Space(2)=52, newest first | match.
- design: max 3 at once, 4 s auto-dismiss, pause on hover | code: no limit or duration set in ToastHost or toastAs (shell.go:123 appends to a.toasts unbounded) | change: verify mygo ui.Toast default duration (4s?) / max; if not, set Duration 4s and drop oldest beyond 3 in toastAs.
- design: kinds: plain, success, warning, error | code: kit.Toast* + toastAs | match. Toast Description/Action supported in code but absent in design (extra, harmless).
- design copy: "邮件已发送","设置已保存","链接已复制","先接受或拒绝助手的修改","2 封未能处理","work@... 没有归档文件夹，已跳过" | code: grep for "mail.sent"/link-copied/ask pending-edits not checked beyond settings.saved, mail.failed, mail.no_archive | change: spot-check strings "邮件已发送", "链接已复制", "先接受或拒绝助手的修改" exist and use toastAs with right kind (sent=success, copied=plain, pending edits=warning).

## notify.go
- No design counterpart (OS notifications, not in these mockups). Nothing to align.

## Feature gaps
- Folder count ("6 个文件夹") in IMAP check detail and done-step subtitle (core.CheckReport needs a count).
- Sync range by time ("最近 3 个月", "全部") vs code's message-count limits.
- Manual server panel without security/username fields (design simplification; code keeps them).
- Email input with in-box provider badge (needs trailing-slot support in kit.Input).
- Toast cap (3) and 4 s auto-dismiss with hover pause, if mygo doesn't provide them.
