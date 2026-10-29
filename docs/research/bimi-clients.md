# BIMI in mail clients: spec duties, receiver policies, open-source implementations

Researched 2026-10-29. [O] = primary source (spec, official doc, source code at a cited path/commit), [S] = secondary. Source repos were read from tarballs of HEAD on 2026-10-29; commit SHAs below are what `GET /repos/{r}/commits/HEAD` returned that day. Nothing downloaded was executed.

Short links used below:
- BIMI = https://www.ietf.org/archive/id/draft-brand-indicators-for-message-identification-14.txt (rev 14, May 2026, expires 2026-11-02)
- GUIDE = https://www.ietf.org/archive/id/draft-brotman-ietf-bimi-guidance-15.txt (rev 15, May 2026)
- VMC = https://www.ietf.org/archive/id/draft-fetch-validation-vmc-wchuang-11.txt (rev 11, May 2026)
- MUA = https://www.ietf.org/archive/id/draft-brotman-bimi-mua-00.txt (rev 00, June 2025, expired 2025-12-22)
- SVGPS = https://www.ietf.org/archive/id/draft-svg-tiny-ps-abrotman-12.txt (rev 12, May 2026)
- RFC8601 = https://www.rfc-editor.org/rfc/rfc8601 (Authentication-Results)

`draft-blank-ietf-bimi-header` does not exist on datatracker (API 404). The datatracker list of `*bimi*` docs is: the main draft, -guidance, -bimi-mua, `draft-adams-bimi-reporting`, the expired `draft-blank-ietf-bimi-02` (2021, the old name of the main draft), and some 2018-2019 drafts [O https://datatracker.ietf.org/api/v1/doc/document/?name__contains=bimi].

## 1. What the spec says: MTA vs MUA

| Topic | Rule | Source |
|---|---|---|
| Who validates DMARC | "a receiver MUST verify" the message before BIMI: exactly one From address; DMARC result for the Author Domain = pass (a trusted ARC chain or local policy MAY count as pass); no BIMI if the Author or Org Domain DMARC is `p=none` or `sp=none`; if `p=quarantine`, `pct` must be 100. | [O BIMI §7.1] |
| Who does discovery | The "Protocol Client", which in practice is the receiver. It queries `<selector>._bimi.<author-domain>` TXT, then `<selector>._bimi.<org-domain>`. The selector comes from `BIMI-Selector`, else `default`. Handles `lps=`. 0 or >1 records means stop. "MUST NOT be attempted if the message authentication fails". | [O BIMI §7.2] |
| BIMI-Selector trust | SHOULD be covered by the DMARC-aligned DKIM signature, "the header SHOULD be ignored" otherwise. A trusted ARC chain MAY substitute. | [O BIMI §5.5] |
| Headers the MTA adds | `BIMI-Location` (`l=`/`a=` URIs), `BIMI-Indicator` (base64 of the validated SVG, gunzipped), `BIMI-Logo-Preference`. "MUST NOT be added if Discovery or Validation steps failed". | [O BIMI §5.2-5.4, §7.9-7.11] |
| Header signing | BIMI-Location/-Indicator/-Logo-Preference "MUST NOT be DKIM signed"; they "are untrusted by definition, and is only for use between an MTA and its MUAs". | [O BIMI §5.5] |
| Stripping | A BIMI-aware receiving MTA MUST remove those headers when they come from outside its site. A non-BIMI-aware site "SHOULD remove those headers". §7.8: existing ones MUST be removed or renamed "regardless of success of the BIMI lookup", because letting them reach a MUA "is a security risk". | [O BIMI §5.6, §7.8] |
| Authentication-Results | The MTA SHOULD add `bimi=pass|none|fail|temperror|declined|skipped` with `header.d`, `header.selector`, `policy.authority` (pass/fail/none for evidence), and optional `policy.authority-uri`, `policy.indicator-uri`, `policy.indicator-hash`, `policy.logo-preference`. | [O BIMI §7.7] |
| indicator-hash / ARC | `policy.indicator-hash` = SHA-256 of the raw SVG, truncated to the last >=8 chars, so the hash "can be signed by, and verified by an ARC aware MTA/MUA pair". If present, BIMI-Indicator MUST be present too. GUIDE §5.4: when the MUA uses ARC, it MUST evaluate ARC, use only passing sets "added by known good servers", stop at the first ARC fail, and "MUST disregard any BIMI-Indicator headers which do not have a matching hash". BIMI-Location "is not protected from forgery in the ARC set, and MUST NOT be used". GUIDE marks this as not yet in the main draft. | [O BIMI §7.7; GUIDE §5.4] |
| MUA duties | Only §4.1: MUAs SHOULD make a "best-effort attempt" to follow the domain owner's policy, MAY show other indicators or none. Appendix C.6: the MUA "performs locally defined checks to make sure that it can trust the BIMI-Indicator header". | [O BIMI §4.1, App. C.6] |
| Can a MUA do its own DNS lookup? | The main draft does not forbid it: "Protocol Client" is role-neutral and §2.3 puts display requirements out of scope. GUIDE §3.3: "the responsibility for validating inbound messages lies with the Receiver/MBP". GUIDE §5.3: the MUA "must check to see if a message passed BIMI" and headers "should not be relied upon by an MUA without additional checks", for example a trusted AR `bimi=pass`. GUIDE §5.4 lets a MUA, when the hash fails, re-fetch from `policy.authority-uri`/`policy.indicator-uri` or "evaluat[e] BIMI directly using the domain and selector from the bimi Authentication-Results entry". So self-lookup is described only as a step after a trusted AR result, never instead of one. | [O BIMI §2.3; GUIDE §3.3, §5.3, §5.4] |
| Independent MUA problem | MUA draft: a MUA "almost certainly cannot perform SPF validation" and can verify DKIM only if the MTA did not alter the message. It proposes a `BIMI-Receiver-Signature` (DKIM-style signature over BIMI-Location, BIMI-Selector and a new BIMI-Receiver-Information) with keys at `<sel>._local._bimi.<mbp-domain>` and revocation via empty records. Threats it names: header injection to an unaware MBP, and IMAP APPEND. Section 4, "MUA Validation", is a stub. Expired, not implemented anywhere I found. | [O MUA §1-6] |
| Authentication-Results trust | MUAs "SHOULD NOT interpret this header field unless specifically configured to do so", and not unless the authserv-id "is used within the ADMD as configured by the user or administrator". MTAs MUST delete inbound AR headers that claim their own authserv-id. | [O RFC8601 §4.1, §5] |
| Image format | `l=` must be HTTPS. Only SVG/SVGZ are allowed, otherwise permerror [O BIMI §4.3, §4.3.2]. SVG Tiny PS: `version="1.2"`, `baseProfile="tiny-ps"`, exactly one non-empty `<title>`. No `image`, `switch`, multimedia, interactivity, linking, scripting or animation. SHOULD NOT exceed 32 KB [O SVGPS §2.1, §2.3]. GUIDE §6.2 says to guard against XML bombs. Without evidence, the TLS cert of the `l=` host must chain to a trusted root [O BIMI §7.5]. |
| Evidence (VMC/CMC) | VMC covers registered trademarks or government marks; CMC covers prior-use marks or modified trademarks [O BIMI §3.5]. Validation: the PKIX chain to the "receiver's trusted BIMI root set"; validity dates; CRL revocation check (the cert must have a CRL DP); CT SCT validation; EKU `1.3.6.1.5.5.7.3.31` on the leaf and the issuing CA; logotype extension (RFC 3709) with the SVG; SAN dNSName matching the assertion domain, or `<selector>._bimi.<domain>`. Comparing the `l=` SVG with the embedded one is optional (MAY) [O VMC §4.3-4.5, §5.1-5.3]. |
| Reputation | Publishing a record "is not sufficient". Receivers may use brand lists or reputation [O BIMI §2.2, §8.2]. |
| Privacy | MUAs "are encouraged to extract Indicators from the BIMI-Indicator header rather than retrieving them directly". Per-recipient selectors can be used for tracking [O GUIDE §6.3]. |

## 2. Big receivers (official pages only)

| Provider | Cert required | Msg DMARC pass | Domain DMARC enforcement | Badge | Source |
|---|---|---|---|---|---|
| Gmail | Yes: VMC or CMC | Yes: "BIMI requires that messages be authenticated by DMARC" | p=quarantine/reject, pct=100 | Checkmark only for VMC senders. A CMC shows the logo without a check. | [O https://knowledge.workspace.google.com/admin/security/set-up-bimi (formerly support.google.com/a/answer/10911320)] |
| Apple Mail / iCloud | Requires a "BIMI Evidence Document (for example, a VMC trusted by the mail provider)". CMC is not mentioned. | Per the BIMI draft | Per the BIMI draft | Not stated | [O https://developer.apple.com/support/bimi/ ; https://support.apple.com/en-us/108340] |
| Yahoo/AOL | No. A VMC, if present, "will use it to inform" eligibility. | Implied | p=quarantine/reject | Not stated. Bulk mail only, needs reputation/engagement. | [O https://senders.yahooinc.com/bimi/] |
| Fastmail (receiving) | Not stated for receiving. Sending side says "Some receivers (such as Gmail) won't show the logo without a VMC". | Yes, the message "passes its DMARC authentication checks" | "strong or enforcing" (undefined for receiving) | Not mentioned. Logo fetched at MX, nothing fetched on view. | [O https://www.fastmail.help/hc/en-us/articles/7002542139663-Using-BIMI-in-Fastmail] |
| Zone (EE) webmail | Yes: VMC or CMC | Yes, plus a valid DKIM signature | quarantine/reject, pct=100 | Badge for VMC | [O https://www.zone.ee/help//en/kb/bimi-en/] |
| Proton | Unverified. The sender-images help page says nothing about BIMI; it describes favicons fetched by Proton servers. | ? | ? | ? | [O https://proton.me/support/sender-images]. Code: §3. |
| La Poste | Unverified. bimigroup links to aide.laposte.net and the relevant article returned 404. | ? | ? | ? | - |
| Microsoft | Listed under "Does not support BIMI" | - | - | - | [O https://bimigroup.org/bimi-infographic/] |

Apple matters most for Godwit because Apple Mail is a MUA that also reads third-party IMAP accounts. The developer page requires the *mail provider* to have "been added to the bimigroup.org list of providers supporting BIMI, and been verified by Apple", and to have "added the required headers vouching for these checks" [O developer.apple.com/support/bimi]. That is a provider allowlist: Apple trusts headers only from vetted providers. It probably means no logos for an unlisted self-hosted IMAP server, but that is my reading of the page, not something Apple says. Apple Mail's code is closed, so I could not check what it does with headers from unlisted providers.

Fastmail does stamp the headers. In a real Fastmail-received CNN message in DKIM Verifier's test corpus, the MX added `BIMI-Location: v=BIMI1;`, a `BIMI-Indicator:` with base64 SVG, and AR `bimi=pass header.d=cnn.com header.selector=default policy.authority=pass`, plus 3 ARC header fields [O lieser/dkim_verifier@727c54b test/data/original/"Fastmail from CNN - Welcome to CNN Breaking News.eml"].

## 3. Open-source clients, confirmed in source

| Client | Where the logo comes from | Message auth required | Domain DMARC policy | VMC | Trust in AR headers | Default |
|---|---|---|---|---|---|---|
| FairEmail (Android) | Own DNS + HTTPS | Only if `favicons_dmarc` is on (the same gate covers all avatar sources). The badge needs message auth. | Checked only when validating a VMC | Partial validation; unverified logo shown unless `bimi_vmc` | Any AR/ARC-AR/X-AnonAddy-AR, no authserv-id check | Off; not in Play build |
| DKIM Verifier (Thunderbird add-on) | `BIMI-Indicator` header only, no DNS | Its DKIM result must be SUCCESS and From must be in the SDID | MTA's job | MTA's AR `policy.authority=pass` | authserv-id allowlist per account; default = newest AR only | ARH reading off by default |
| SnappyMail (avatars plugin) | Server-side DNS `<sel>._bimi.<from-domain>` + fetch | AR `dkim=pass` matched to From | No | No | Any AR header, no authserv-id check | Off |
| Proton web | Proton API `core/v4/images/logo?Address&BimiSelector` | Server decides | Server | Server | n/a (server-side) | Server flag `DisplaySenderImage` |
| Roundcube (unmerged PRs) | DNS `default._bimi` + fetch | No | No | No | n/a | Not shipped |
| Auto Profile Picture (Thunderbird add-on) | Cloudflare DoH `default._bimi.<domain>` + fetch | No | No | No | n/a | BIMI provider enabled first |
| BIMI Result Viewer (Thunderbird add-on) | Popup only, not an avatar | AR `bimi=pass` + `policy.authority=pass` + `header.d` == From domain | MTA | MTA | Any AR header | Popup on demand |
| KMail / KDE messagelib | No display. The AR parser accepts `bimi` method keywords. | - | - | - | - | - |

### FairEmail (M66B/FairEmail @ 28b290b)

Base: https://github.com/M66B/FairEmail/blob/28b290b3533ad329a73b838704257a72cab89ca6/

- Lookup [O `app/src/extra/java/eu/faircode/email/Bimi.java` L84-104, L386-399]: `<selector>._bimi.<domain>`, selector default `"default"`. If nothing is found, one retry at `UriHelper.getParentDomain`. It takes `records[0]` with no >1-record check and no `lps=`. An empty `l=` is skipped, not treated as a declination.
- Selector [O `app/src/main/java/eu/faircode/email/MessageHelper.java` L2237-2262]: taken from the `BIMI-Selector` header with no check that DKIM covers it (BIMI §5.5 says it SHOULD be ignored otherwise).
- Tags are sorted, so `a` is processed before `l` (L107-110). A valid-looking VMC's logotype SVG wins, and `l=` is then skipped (L121-123, L254-286). The VMC logo and the `l=` logo are never compared.
- VMC checks (L159-360): EKU `1.3.6.1.5.5.7.3.31` on the leaf; SAN compared at the *registrable/root domain* level (`getRootDomain`, L228-239), looser than VMC §5.2; PKIX chain to 2 bundled roots (`app/src/main/assets/vmc/DigiCert Verified Mark Root CA.pem`, `entrust_vmc_specific.pem`) plus the whole `AndroidCAStore` (L289-310), not a BIMI-only root set; `setRevocationEnabled(false)` (L319); no CT/SCT check; no CMC handling.
- After the chain validates, it reads `_dmarc.<domain>` TXT (parent fallback) and requires `p=quarantine|reject` and, if present, `pct=100` (L331-357). `sp=` is not checked. This is DNS policy, not this message's result. Only then is `verified=true`.
- If unverified, the logo is still returned unless pref `bimi_vmc` is on (L375-381).
- Gating [O `app/src/main/java/eu/faircode/email/ContactInfo.java` L281-289]: `bimi = pref "bimi" (default false) && (!favicons_dmarc || dmarc) && !PLAY_STORE_RELEASE`. The same `(!favicons_dmarc || dmarc)` gate applies to gravatars, libravatars, favicons and ddg_icons; `favicons_dmarc` defaults to false. The label is "Require DMARC verification for external images" (`res/values/strings.xml` L652). Skipped in the Junk folder (L350-352).
- Badge [O `AdapterMessage.java` L2114-2131]: the check icon shows only if the avatar came from BIMI, `verified`, and the message passed auth (`message.auth`, or `dkim && spf && dmarc`).
- Message DMARC [O `MessageHelper.java` L2264-2328; `Core.java` L3615]: comes from every `Authentication-Results`, `X-AnonAddy-Authentication-Results` and `ARC-Authentication-Results` header. The first header with a `dmarc=` value wins, with **no authserv-id check** (confirmed). Optional `native_dkim` (off by default, not in Play build) instead verifies DKIM locally and computes alignment (`Core.java` L3666-3674).
- Play Store build: "does not support Gravatars/Libravatars/BIMI due to Play Store policies" [O `FAQ.md` L5659].

### DKIM Verifier (lieser/dkim_verifier @ 727c54b)

Base: https://github.com/lieser/dkim_verifier/blob/727c54bfd76b3b5443647492f2fea80ebba5e632/

- [O `modules/bimi.mjs.js` L34-73]: no DNS. It returns the `BIMI-Indicator` value only if a *trusted* AR has `bimi=pass` with `policy.authority=pass`, and exactly one `BIMI-Indicator` header exists. The code comment states its assumptions: the MTA strips foreign BIMI headers, and a non-BIMI MTA produces no trusted bimi AR. `policy.indicator-hash` is not checked ("TODO: If in the future we support ARC").
- Trust [O `modules/arhVerifier.mjs.js` L39-105]: uses only AR headers whose authserv-id is in `account.arh.allowedAuthserv` (exact match or `@domain` suffix). If that list is empty (the default, `modules/preferences.mjs.js` L634-636), it trusts only the first, i.e. newest, AR header.
- AR reading itself is opt-in: `arh.read` defaults to false (`preferences.mjs.js` L219-220).
- Display [O `modules/authVerifier.mjs.js` L603-618]: shown as the favicon only when the first DKIM result is SUCCESS and From is within the signing SDID. Otherwise it falls back to bundled static favicons (`modules/dkim/favicon.mjs.js`, `data/favicon.json`), with no network fetch.
- Added in 5.4.0 (2023-11-16) [O `CHANGELOG.md` L156]. The author's note is in bugzilla 1670078 c19 [O].

### SnappyMail (the-djmaze/snappymail @ b5315e4)

This contradicts the earlier "no BIMI code" note: the avatars plugin has a BIMI path. Base: https://github.com/the-djmaze/snappymail/blob/b5315e4044e34376beb016167f08e60b424239f8/

- [O `plugins/avatars/index.php` L41-50, L79-81, L261-266, L146-149]: pref `bimi` defaults to false ("DKIM header must be valid"). It fetches `BIMI-Selector` over IMAP and uses BIMI only when `From.dkimStatus == 'pass'`. The URL goes through the server-side `getUrl` and is cached.
- [O `snappymail/v/0.0.0/app/libraries/snappymail/dns.php` L17-54]: one `dns_get_record("{selector}._bimi.{domain}")`, regex for `l=`. No org-domain fallback, DMARC check or VMC.
- DKIM status [O `.../MailSo/Mime/HeaderCollection.php` L183-227; `.../MailSo/Mail/Message.php` L293-305]: all AR headers are concatenated and regex-scanned, with no authserv-id check. Fallback is `X-DKIM-Authentication-Results`. The match is `strpos(fromEmail, header.d)`, a substring test, not alignment.

### Proton web client (ProtonMail/WebClients @ 320def3)

- [O `packages/shared/lib/api/images.ts`; `packages/components/containers/contacts/helpers/senderImage.ts` L28-48; `applications/mail/src/app/components/list/Item.tsx` L258-263; `packages/shared/lib/interfaces/Address.ts` L61-62]: the client sends `Address`, `BimiSelector` and `Mode` to `core/v4/images/logo`, and only shows the image when the server-provided `DisplaySenderImage` is set. All BIMI evaluation is server-side and not visible.

### Roundcube (not shipped)

- Issue #8143 open since 2021-07-23; maintainer alecpl: "sounds like a good idea for a plugin" [O https://github.com/roundcube/roundcubemail/issues/8143].
- PR #8860 (open, unmerged, head 4c1207c) [O https://github.com/roundcube/roundcubemail/blob/4c1207c889623b91f4a790e506c13184aedf38d6/plugins/bimi/bimi_engine.php L64-78, L107-140]: `default._bimi.<domain>` only. It walks up *every* parent label until one hits, fetches `l=` server-side, requires `Content-Type: image/svg+xml`, and runs `rcmail_attachment_handler::svg_filter`. Cached. No DMARC, VMC or selector.
- PR #8759 (open) [O https://github.com/roundcube/roundcubemail/pull/8759 `program/actions/mail/show.php`]: DNS lookup on the sender domain, then sets `onerror` so the *browser* loads the `l=` URL directly. That leaks the reader's IP to the sender.

### Thunderbird and add-ons

- Core: no `bimi` hits in comm-central (Searchfox JSON query, only unrelated base64 in package-lock) [O https://searchfox.org/comm-central/search?q=bimi]. Bug 1670078 is NEW, opened 2020-10-08 [O https://bugzilla.mozilla.org/show_bug.cgi?id=1670078]. Key comments: c20 (2024) quotes BIMI §7.8 and warns that a MUA "can be attacked unless its supporting MTA also supports" the stripping rule. c24 (Hanno Böck, 2025): "The current BIMI spec cannot be securely implemented by Thunderbird", because a client for arbitrary servers cannot know whether the server strips headers, and no IMAP extension exists. c25 suggests ARC-seal trust per configured sealing domain. c17 raises tracking-pixel concerns.
- Auto Profile Picture 2.6.0 (MPL-2.0, ATN id 988770; GitHub repo noam-sc/thunderbird-auto-profile-picture now 404). Read from the published XPI [O https://addons.thunderbird.net/thunderbird/downloads/file/1052909/auto_profile_picture-2.6.0-tb.xpi `providers/Bimi.js`, `settings/defaultSettings.js` L60-66]: DoH to `cloudflare-dns.com` for `default._bimi.<domain>`. It rejects non-HTTPS, IP-literal, single-label, `.local` and non-default-port URLs. No DMARC or VMC. Enabled and first in the provider order by default. SVG is rasterized to PNG before painting (`src/ProfilePictureFetcher.js` L590-593). The listing itself says logos "do not certify the authenticity of the sender".
- BIMI Result Viewer 1.0.7 (MIT, ATN id 989774) [O XPI `bimi.js` L145-175]: reads all AR headers with no authserv-id check. PASS = `bimi=pass` + `policy.authority=pass` + `header.d` == From domain + a `BIMI-Indicator` present. The logo is loaded from `policy.indicator-uri` (it also tries to read a URL out of `BIMI-Indicator`, which per spec holds base64 SVG, not a URL). The listing admits forged ARs on non-stripping servers can show fraudulent logos.

### Searched, no BIMI display code

Checked by full-text grep of HEAD tarballs, 2026-10-29 [O]. Hits in locales, lockfiles and test fixtures are ignored.
- thunderbird/thunderbird-android @1d3437a
- nextcloud/mail @adc4971 (only an encrypted test fixture)
- Foundry376/Mailspring @4d2ca0f
- chatmail/core @1a4a988 (only a test)
- tutao/tutanota @86d30ab (lockfile hash only)
- cypht-org/cypht @ba52702
- Alinto/sogo @8c50b84
- horde/imp @60f8346
- RainLoop/rainloop-webmail @9bf8fa5 (archived 2022; a base64 false positive)
- neomutt/neomutt @88ae848
- aerc (git.sr.ht/~rjarry/aerc master)
- KDE pim/kmail master
- GNOME evolution master, geary main
- KDE pim/messagelib master: only `messagecore/src/dkim-verify/dkimauthenticationstatusinfo.cpp` L109-112 accepts the `bimi` method's result keywords when parsing AR. No logo display.

## 4. Not verified

- Claws Mail: snapshot download from git.claws-mail.org failed; not checked. Classic mutt: not checked.
- Apple Mail behaviour for providers not on its allowlist (closed source); Apple's CMC stance.
- Proton's server-side BIMI rules (no official page found); La Poste's rules (help article 404); Yahoo's badge.
- Whether CMCs carry the same EKU `1.3.6.1.5.5.7.3.31`, i.e. whether FairEmail's check accepts CMCs. Not checked against the Mark Certificate requirements document.
- Third-party claims that Fastmail displays without a VMC, and that Apple rejects CMCs, are [S] only (e.g. https://www.captaindns.com/en/blog/bimi-vmc-cmc-compatibilite-dns).
- Exim's refusal to strip BIMI headers is cited in bugzilla c20 (https://bugs.exim.org/show_bug.cgi?id=3069#c4); I did not read the Exim bug itself [S].

## 5. Implications for Godwit

Godwit already shows a favicon for the sender's From domain. With no message authentication, that has the same spoofing exposure as an unauthenticated BIMI logo: a forged `From: bank.example` shows the bank's real icon. BIMI marks look more official than favicons, and a verification badge would make the claim explicit, so badges are where the extra risk lies.

| Option | What it proves | Who does it | Cost |
|---|---|---|---|
| A. DNS only: `<sel>._bimi.<from-domain>` (+org fallback) → fetch `l=` SVG | Only that the From domain's DNS points at this logo. Same as a favicon. | Auto Profile Picture (on by default); Roundcube PRs (unmerged); FairEmail with `bimi_vmc` and `favicons_dmarc` off | 1-2 TXT + 1 HTTPS per domain, cacheable. Needs an SVG Tiny PS sanitizer/rasterizer (XML bomb, size cap 32 KB [O SVGPS, GUIDE §6.2]). The fetch leaks the reader to the logo host unless done once and cached. An unsigned `BIMI-Selector` allows per-recipient tracking selectors [O GUIDE §6.3]. Ignores the spec's "MUST NOT be attempted if the message authentication fails" [O BIMI §7.2]. |
| B. A + domain DMARC enforcement (`_dmarc` p=quarantine/reject, pct=100, sp≠none) | The domain *asks* receivers to reject spoofs. Says nothing about this message: a spoof delivered by a lax server still gets the logo. | FairEmail (only inside its VMC path, sp unchecked); the server-side rule at Gmail/Yahoo/Fastmail/Zone | +1 TXT per domain. Cuts out most hobby domains. |
| C. B + VMC/CMC validation | The logo is a mark certified for that domain (trademark/prior use). Still not that this message came from it. | FairEmail (partial: no CRL, no CT, Android CA store as anchors, root-domain SAN match) | X.509 path to a BIMI root set (bundled roots to maintain), CRL fetch, CT SCT checks, RFC 3709 logotype parsing, SAN rules [O VMC §5]. Go stdlib `crypto/x509` covers chain and EKU, but not logotype or SCT policy (unverified). Few senders qualify. |
| D. Per-message trust: `dmarc=pass` (or `bimi=pass` + `BIMI-Indicator`) from an Authentication-Results header whose authserv-id the user configured for that account | This message passed DMARC at *your* server, provided that server strips forged ARs [O RFC8601 §5] and forged BIMI-* headers [O BIMI §5.6/§7.8]. | DKIM Verifier (opt-in, per-account authserv allowlist, default newest-only). Apple Mail (provider allowlist, closed source). FairEmail, SnappyMail and BIMI Result Viewer read AR **without** authserv-id checks. | Per-account setting; RFC 8601 §4.1 says it should not be "on by default". Godwit can't tell whether an arbitrary IMAP server strips headers (bugzilla c24). Using `BIMI-Indicator` needs no DNS/HTTP fetch, which is better for privacy [O GUIDE §6.3]. ARC + `policy.indicator-hash` and the MUA draft's `BIMI-Receiver-Signature` would remove the config burden, but neither is deployed or finished (GUIDE §5.4 note; MUA expired, §4 stub). |

Facts relevant to the choice:
- No surveyed open-source client combines D with an authserv-id allowlist *and* its own DNS lookup.
- Every client that does its own lookup (A-C) runs it off by default except Auto Profile Picture.
- FairEmail shows its "verified" badge only when C and per-message auth both pass, even though its per-message auth (D) is untrusted.
