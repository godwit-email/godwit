package mail

import (
	"regexp"
	"strings"
)

// A lexicon is how one language words sign-in mail. The recognizers
// (signincode.go, signinlink.go) own the patterns — a code after a hint, a
// code alone on its line, a URL after an action and a colon, a button's
// words — and take their words from every lexicon at once, so mail in any
// listed language is read without first guessing its language. Each field
// is a regular-expression alternative, matched case-insensitively; Latin
// words carry their own \b (RE2's \b knows only ASCII letters). Text and
// words alike are read with Traditional characters made Simplified
// (simplified), so zh lists Simplified only and ja may write its kanji as is.
// Adding a language is adding a lexicon and sample mails to its tests.
type lexicon struct {
	lang string

	// Codes.
	codeHints  string // right before a code, and only used for codes: "verification code", "验证码"
	ambiguous  string // right before a code, but other mail uses them too: "your code", "security code"
	copulas    string // between a hint and its code: "is", "为", "lautet"
	codeBefore string // between a code and its hint: "is your" ("482913 is your verification code")
	signIn     string // words that make a mail about signing in, near an ambiguous hint
	enter      string // telling to enter a code: "enter", "输入"
	codeWords  string // a line speaking of a code, above a code alone on its line

	// Replies and forwards: their quoted part, and a subject that repeats the old mail's.
	replyPrefix string // "re", "回复"
	replyHeader string // a line the quoted message starts after: "On … wrote:"
	replyFrom   string // "from", "发件人": with replySent on the next line, a header block
	replySent   string // "sent", "日期"

	// Links.
	signInSubject string // a subject about signing in
	signInLabel   string // a sign-in button's words, whole
	signInLead    string // a sign-in action before a colon and its URL
	delivery      string // a magic link's mail speaking of the link itself: "this link expires"
	alert         string // a security alert, whose links review activity
	confirm       string // confirming a new account's address: "confirm your email"
	reset         string // setting a new password: "reset your password"
	clickTo       string // before a button's words: "click here to"
	leadTail      string // between an action and its colon: " here", " below"
	context       string // more words that make a mail about confirming or resetting: "email verification"
	fallback      string // the line under an image-only button: "if the button does not work, use this link:"
	fallbackLabel string // that line's link, whole: "use this link"
}

// byLink: how a link is used, between an action and its colon.
const byLink = ` (?:by clicking|by tapping|using|with|via)(?: on)? (?:this|the)(?: (?:link|button))?(?: below)?`

var lexicons = []lexicon{
	{
		lang:          "en",
		codeHints:     `\b(?:verification|authentication|login|sign[ -]?in|one[ -]time|single[ -]use)\s+(?:verification\s+)?code\b|\bOTP\b|\bpasscode\b`,
		ambiguous:     `\b(?:your|confirmation|security|activation)\s+code\b`,
		copulas:       `\bis\b`,
		codeBefore:    `\bis (?:your|the)\b`,
		signIn:        `\b(?:verif(?:y|ying|ication)|sign[ -]?in|log[ -]?in|authenticat\w*|two[ -]factor|2fa|password|passcode)\b|\bconfirm (?:your )?(?:email|account|identity)\b|\baccount needs activation\b|\bactivate (?:your )?account\b`,
		enter:         `\b(?:enter|type|paste|input)\b`,
		codeWords:     `\b(?:code|passcode|OTP)\b`,
		replyPrefix:   `re|fwd?`,
		replyHeader:   `-{2,}\s*(?:original message|forwarded message)\s*-{2,}|on .+ wrote:`,
		replyFrom:     `from`,
		replySent:     `sent|date`,
		signInSubject: `\b(?:sign|log)[ -]?in\b|\b(?:magic|secure) link\b`,
		signInLabel:   `(?:(?:click|tap) (?:here )?to |continue to )?(?:(?:sign|log)[ -]?in|magic link)(?:\s+(?:to|with)\s+\S.*)?`,
		signInLead:    `(?:sign|log)[ -]?in(?: here| now| below| link| to [^:：]+| with [^:：]+|` + byLink + `)?|magic link`,
		delivery:      `\b(?:magic|secure|sign[ -]?in|log[ -]?in|login|one[ -]time|single[ -]use)\s+link\b|\bthis link\b|\blink\b[^\n]{0,60}\b(?:expires?|valid|used once)\b`,
		alert:         `\bnew (?:sign[ -]?in|login|device)\b|\b(?:sign[ -]?in|login) (?:attempt|alert|activity)\b|\bunusual\b`,
		confirm:       `(?:confirm|verify|activate)\s+(?:your\s+)?(?:e-?mail(?:\s+address)?|account)`,
		reset:         `(?:reset|change)\s+(?:your\s+)?password`,
		clickTo:       `(?:click|tap) (?:here )?to `,
		leadTail:      ` here| below|` + byLink,
		context:       `\bemail verification\b|\bpassword reset\b`,
		fallback:      `(?:button|link)\s+(?:does\s*n[o']t|is\s*n[o']t|won'?t|not)\s+work.*\b(?:use|copy|paste|open)\b.*\b(?:link|url|address)\b[^:：\n]*[:：]?\s*$|trouble clicking.*(?:copy|paste).*\b(?:url|link)\b`,
		fallbackLabel: `use this link`,
	},
	{
		lang:          "zh-Hans",
		codeHints:     `验证码|校验码|动态码|动态密码|一次性密码|一次性代码|安全代码|验证代码`,
		ambiguous:     `激活码`,
		copulas:       `为|是`,
		codeBefore:    `是(?:您|你)的`,
		signIn:        `登录|登入|验证|激活(?:您|你)?的?(?:账户|帐户|账号)`,
		enter:         `输入|填写|粘贴`,
		codeWords:     `代码|验证码|校验码|确认码|动态码|动态密码`,
		replyPrefix:   `回复|答复|转发`,
		replyHeader:   `在.+写道[:：]`,
		replyFrom:     `发件人`,
		replySent:     `发送时间|日期`,
		signInSubject: `登录|登入`,
		signInLabel:   `(?:点击|立即)?(?:登录|登入)(?:到|至)?\s*[A-Za-z0-9 ._-]*`,
		signInLead:    `(?:登录|登入)(?:链接)?`,
		delivery:      `(?:登录|登入|一次性|安全|此|该|下面的|下方的)链接|链接[^\n]{0,30}(?:有效|过期|失效)`,
		alert:         `新(?:的)?(?:登录|登入|设备)|登录(?:提醒|通知|活动)|异常登录`,
		confirm:       `(?:确认|验证|激活)(?:您|你)?的?(?:电子邮件|电子邮件地址|邮箱|邮箱地址|邮件地址|账户|帐户|账号)`,
		reset:         `(?:重置|重设|找回)(?:您的|你的)?密码`,
		clickTo:       `点击`,
		fallback:      `(?:无法|不能)点击.*(?:按钮|链接).*[:：]\s*$`,
		fallbackLabel: `使用此链接`,
	},
	{
		// Taiwan's and Hong Kong's wording, written as it is: words are read
		// Simplified like the text, so the characters zh-Hans shares match there
		// already; these are the words zh-Hans does not use.
		lang:          "zh-Hant",
		codeHints:     `驗證碼|認證碼|簡訊驗證碼|一次性密碼|動態密碼|登入碼`,
		ambiguous:     `安全碼|確認碼|啟用碼|開通碼`,
		copulas:       `為|是`,
		codeBefore:    `是(?:您|你)的`,
		signIn:        `登入|驗證|認證|(?:啟用|開通)(?:您|你)?的?(?:帳戶|帳號)`,
		enter:         `輸入|填寫|貼上`,
		codeWords:     `代碼|驗證碼|認證碼|確認碼`,
		replyPrefix:   `回覆|轉寄|轉發`,
		replyHeader:   `(?:於|在).+寫道[:：]`,
		replyFrom:     `寄件者|寄件人`,
		replySent:     `寄件日期|傳送時間|日期`,
		signInSubject: `登入|登錄`,
		signInLabel:   `(?:按此|點此|點擊|立即)?(?:登入|登錄)(?:到|至)?\s*[A-Za-z0-9 ._-]*`,
		signInLead:    `(?:登入|登錄)(?:連結)?`,
		delivery:      `(?:登入|登錄|一次性|安全|此|該|下方的?)連結|連結[^\n]{0,30}(?:有效|過期|失效)`,
		alert:         `新(?:的)?(?:登入|裝置)|登入(?:提醒|通知|活動)|(?:異常|不明)登入`,
		confirm:       `(?:確認|驗證|啟用|開通)(?:您|你)?的?(?:電子郵件(?:地址)?|電郵(?:地址)?|信箱|郵件地址|帳戶|帳號)`,
		reset:         `(?:重設|重新設定|重置|找回)(?:您的|你的)?密碼`,
		clickTo:       `按此|點此|點擊`,
		fallback:      `(?:無法|不能)(?:點擊|點選|按).*(?:按鈕|連結).*[:：]\s*$`,
		fallbackLabel: `使用此連結`,
	},
	{
		lang:          "ja",
		codeHints:     `認証コード|認証番号|確認番号|検証コード|ログインコード|ワンタイムパスワード|ワンタイムコード`,
		ambiguous:     `確認コード|セキュリティコード|アクティベーションコード`,
		copulas:       `は`,
		codeBefore:    `が(?:あなたの)?`,
		signIn:        `ログイン|サインイン|認証|本人確認|パスワード`,
		enter:         `入力|貼り付け`,
		codeWords:     `コード|認証番号|確認番号`,
		replyHeader:   `.+(?:さん)?は書きました[:：]`,
		replyFrom:     `差出人|送信者`,
		replySent:     `送信日時|日付`,
		signInSubject: `ログイン|サインイン`,
		signInLabel:   `(?:クリックして)?(?:ログイン|サインイン)(?:する)?`,
		signInLead:    `(?:ログイン|サインイン)(?:用)?(?:リンク)?`,
		delivery:      `(?:ログイン|サインイン|ワンタイム)(?:用)?リンク|この(?:リンク|URL)|リンク[^\n]{0,30}(?:有効|期限)`,
		alert:         `新しい(?:ログイン|サインイン|デバイス)|ログイン(?:通知|アラート)|不審な`,
		confirm:       `(?:メールアドレス|アカウント)(?:を)?(?:確認|認証|有効化)(?:する)?`,
		reset:         `パスワード(?:を)?(?:リセット|再設定|変更)(?:する)?`,
		clickTo:       `クリックして`,
		fallback:      `(?:ボタン|リンク)が(?:機能しない|動作しない|クリックできない)`,
	},
	{
		lang:          "ko",
		codeHints:     `인증\s*번호|인증\s*코드|일회용\s*비밀번호|로그인\s*코드`,
		ambiguous:     `확인\s*코드|확인\s*번호|보안\s*코드|활성화\s*코드`,
		copulas:       `는|은`,
		signIn:        `로그인|인증|본인\s*확인|비밀번호`,
		enter:         `입력|붙여넣`,
		codeWords:     `코드|인증\s*번호`,
		replyPrefix:   `답장|전달`,
		replyFrom:     `보낸\s*사람`,
		replySent:     `보낸\s*날짜|날짜`,
		signInSubject: `로그인`,
		signInLabel:   `(?:클릭하여\s*)?로그인(?:하기)?`,
		signInLead:    `로그인(?:\s*링크)?`,
		delivery:      `(?:로그인|일회용)\s*링크|이\s*링크|링크[^\n]{0,30}(?:유효|만료)`,
		alert:         `새로운\s*(?:로그인|기기)|로그인\s*(?:알림|시도)|비정상`,
		confirm:       `(?:이메일|계정)(?:\s*주소)?(?:을|를)?\s*(?:확인|인증|활성화)(?:하기)?`,
		reset:         `비밀번호(?:를)?\s*(?:재설정|초기화|변경)(?:하기)?`,
		clickTo:       `클릭하여\s*`,
		fallback:      `버튼이\s*작동하지\s*않`,
	},
	{
		lang:          "de",
		codeHints:     `\b(?:Bestätigungs|Verifizierungs|Anmelde|Einmal|Login|Authentifizierungs)code\b|\bEinmalpasswort\b`,
		ambiguous:     `\b(?:Sicherheits|Aktivierungs)code\b|\b(?:Ihr|dein)\s+Code\b`,
		copulas:       `\blautet\b|\bist\b`,
		codeBefore:    `\bist (?:Ihr|dein)\b`,
		signIn:        `\b(?:anmeld\w*|einlogg\w*|verifizier\w*|authentifizier\w*|passwort)\b`,
		enter:         `\beingeben\b|\bgeben Sie\b|\bgib\b|\beinfügen\b`,
		codeWords:     `\bCode\b|\bPIN\b`,
		replyPrefix:   `aw|wg`,
		replyHeader:   `-{2,}\s*(?:Ursprüngliche Nachricht|Weitergeleitete Nachricht)\s*-{2,}|Am .+ schrieb .+:`,
		replyFrom:     `von`,
		replySent:     `gesendet|datum`,
		signInSubject: `\banmelden\b|\banmeldung\b|\beinloggen\b|\bAnmeldelink\b`,
		signInLabel:   `(?:hier\s+)?(?:anmelden|einloggen)|bei\s+\S.*\s+anmelden`,
		signInLead:    `(?:anmelden|einloggen|Anmeldelink)(?:\s+hier)?`,
		delivery:      `\b(?:Anmelde|Login)-?link\b|\bdies(?:en|er) Link\b|\bLink\b[^\n]{0,60}(?:läuft|gültig)`,
		alert:         `\bneue (?:Anmeldung|Gerät)|\bAnmeldeversuch|\bungewöhnlich`,
		confirm:       `(?:E-Mail(?:-Adresse)?|Konto)\s+(?:bestätigen|verifizieren|aktivieren)|(?:bestätigen|verifizieren|aktivieren)\s+Sie\s+(?:Ihre\s+|Ihr\s+)?(?:E-Mail(?:-Adresse)?|Konto)`,
		reset:         `Passwort\s+(?:zurücksetzen|ändern)`,
		clickTo:       `hier klicken,? um `,
		fallback:      `(?:Schaltfläche|Button|Link)\s+funktioniert\s+nicht`,
	},
	{
		lang:          "fr",
		codeHints:     `\bcode de (?:vérification|connexion|validation)\b|\bcode à usage unique\b|\bmot de passe à usage unique\b`,
		ambiguous:     `\bcode de (?:confirmation|sécurité|activation)\b|\bvotre code\b`,
		copulas:       `\best\b`,
		codeBefore:    `\best votre\b`,
		signIn:        `\b(?:connexion|connecter|vérifi\w*|authentifi\w*)\b|\bmot de passe\b`,
		enter:         `\b(?:saisi\w*|entre[rz]|tape[rz]|colle[rz])\b`,
		codeWords:     `\bcode\b`,
		replyPrefix:   `tr`,
		replyHeader:   `-{2,}\s*(?:Message d'origine|Message transféré)\s*-{2,}|Le .+ a écrit\s*:`,
		replyFrom:     `de`,
		replySent:     `envoyé|date`,
		signInSubject: `\bconnexion\b|\bse connecter\b|\blien magique\b`,
		signInLabel:   `(?:cliquez ici pour )?(?:se connecter|connectez-vous|connexion)(?:\s+à\s+\S.*)?`,
		signInLead:    `(?:se connecter|connectez-vous|connexion)(?: ici)?|lien de connexion`,
		delivery:      `\blien (?:de connexion|magique)\b|\bce lien\b|\blien\b[^\n]{0,60}\b(?:expire|valable)`,
		alert:         `\bnouvelle connexion\b|\btentative de connexion\b|\binhabituel`,
		confirm:       `(?:confirme[rz]|vérifie[rz]|active[rz])\s+(?:votre\s+)?(?:adresse\s+)?(?:e-?mail|compte)`,
		reset:         `(?:réinitialise[rz]|change[rz])\s+(?:votre\s+)?mot de passe`,
		clickTo:       `cliquez ici pour `,
		fallback:      `(?:bouton|lien)\s+ne\s+fonctionne\s+pas`,
	},
	{
		lang:          "es",
		codeHints:     `\bcódigo de (?:verificación|acceso|inicio de sesión)\b|\bcódigo de un solo uso\b`,
		ambiguous:     `\bcódigo de (?:confirmación|seguridad|activación)\b|\b(?:tu|su) código\b`,
		copulas:       `\bes\b`,
		codeBefore:    `\bes (?:tu|su)\b`,
		signIn:        `\b(?:iniciar sesión|inicio de sesión|verifica\w*|autentica\w*|contraseña)\b`,
		enter:         `\b(?:introduce|introduzca|ingresa|ingrese|escribe|pega)\b`,
		codeWords:     `\bcódigo\b`,
		replyPrefix:   `rv`,
		replyHeader:   `-{2,}\s*(?:Mensaje original|Mensaje reenviado)\s*-{2,}|El .+ escribió\s*:`,
		replyFrom:     `de`,
		replySent:     `enviado|fecha`,
		signInSubject: `\binicia(?:r)? sesión\b|\binicio de sesión\b|\benlace mágico\b`,
		signInLabel:   `(?:haz clic aquí para )?(?:iniciar sesión|inicia sesión|acceder)(?:\s+en\s+\S.*)?`,
		signInLead:    `(?:iniciar sesión|inicia sesión)(?: aquí)?|enlace de inicio de sesión`,
		delivery:      `\benlace (?:de inicio de sesión|mágico)\b|\beste enlace\b|\benlace\b[^\n]{0,60}\b(?:caduca|expira|válido)`,
		alert:         `\bnuevo inicio de sesión\b|\bintento de inicio de sesión\b|\binusual`,
		confirm:       `(?:confirma(?:r)?|verifica(?:r)?|activa(?:r)?)\s+(?:tu\s+|su\s+)?(?:dirección de\s+)?(?:correo(?: electrónico)?|e-?mail|cuenta)`,
		reset:         `(?:restablece(?:r)?|cambia(?:r)?)\s+(?:tu\s+|su\s+)?contraseña`,
		clickTo:       `haz clic aquí para `,
		fallback:      `(?:botón|enlace)\s+no\s+funciona`,
	},
	{
		lang:          "pt",
		codeHints:     `\bcódigo de (?:verificação|acesso|login)\b|\bcódigo de uso único\b`,
		ambiguous:     `\bcódigo de (?:confirmação|segurança|ativação)\b|\b(?:o )?seu código\b`,
		copulas:       `é`,
		codeBefore:    `é (?:o )?seu`,
		signIn:        `\b(?:entrar|login|fazer login|iniciar sessão|verifica\w*|autentica\w*|senha)\b`,
		enter:         `\b(?:insira|digite|cole|introduza)\b`,
		codeWords:     `\bcódigo\b`,
		replyPrefix:   `res|enc`,
		replyHeader:   `Em .+ escreveu\s*:`,
		replyFrom:     `de`,
		replySent:     `enviado|data`,
		signInSubject: `\bentrar\b|\bfazer login\b|\biniciar sessão\b|\blink mágico\b`,
		signInLabel:   `(?:clique aqui para )?(?:entrar|fazer login|iniciar sessão)(?:\s+n[oa]\s+\S.*)?`,
		signInLead:    `(?:entrar|fazer login)(?: aqui)?|link de (?:acesso|login)`,
		delivery:      `\blink (?:de (?:acesso|login)|mágico)\b|\beste link\b|\blink\b[^\n]{0,60}\b(?:expira|válido)`,
		alert:         `\bnovo (?:login|acesso)\b|\btentativa de (?:login|acesso)\b|\bincomum`,
		confirm:       `(?:confirm[ae]r?|verifi(?:que|car)|ativ[ae]r?)\s+(?:o\s+)?(?:seu\s+)?(?:endereço de\s+)?(?:e-?mail|conta)`,
		reset:         `(?:redefin(?:ir|a)|alterar)\s+(?:a\s+)?(?:sua\s+)?senha`,
		clickTo:       `clique aqui para `,
		fallback:      `(?:botão|link)\s+não\s+funcion`,
	},
	{
		lang:          "it",
		codeHints:     `\bcodice di (?:verifica|accesso)\b|\bcodice monouso\b`,
		ambiguous:     `\bcodice di (?:conferma|sicurezza|attivazione)\b|\bil tuo codice\b`,
		copulas:       `è`,
		codeBefore:    `è il tuo`,
		signIn:        `\b(?:accedi|accesso|login|verifica\w*|autentica\w*|password)\b`,
		enter:         `\b(?:inserisci|digita|incolla|immetti)\b`,
		codeWords:     `\bcodice\b`,
		replyHeader:   `Il .+ ha scritto\s*:`,
		replyFrom:     `da`,
		replySent:     `inviato|data`,
		signInSubject: `\baccedi\b|\baccesso\b|\blink magico\b`,
		signInLabel:   `(?:clicca qui per )?(?:accedi|accedere)(?:\s+a\s+\S.*)?`,
		signInLead:    `(?:accedi|accedere)(?: qui)?|link di accesso`,
		delivery:      `\blink (?:di accesso|magico)\b|\bquesto link\b|\blink\b[^\n]{0,60}\b(?:scade|valido)`,
		alert:         `\bnuovo accesso\b|\btentativo di accesso\b|\binsolit`,
		confirm:       `(?:conferma(?:re)?|verifica(?:re)?|attiva(?:re)?)\s+(?:il tuo\s+|l')?(?:indirizzo\s+)?(?:e-?mail|account)`,
		reset:         `(?:reimposta(?:re)?|cambia(?:re)?)\s+(?:la\s+)?password`,
		clickTo:       `clicca qui per `,
		fallback:      `(?:pulsante|link)\s+non\s+funziona`,
	},
	{
		lang:          "ru",
		codeHints:     `код подтверждения|проверочный код|код для входа|одноразов\S* (?:код|пароль)|код верификации`,
		ambiguous:     `код безопасности|код активации|ваш код`,
		copulas:       `—`,
		codeBefore:    `—\s*ваш|это ваш`,
		signIn:        `вход|войти|подтвер\S*|верифика\S*|парол\S*|авториза\S*`,
		enter:         `введите|вставьте|укажите`,
		codeWords:     `код`,
		replyHeader:   `-{2,}\s*(?:Исходное сообщение|Пересылаемое сообщение)\s*-{2,}|.+ пишет\s*:`,
		replyFrom:     `от`,
		replySent:     `отправлено|дата`,
		signInSubject: `вход|войти|магическая ссылка`,
		signInLabel:   `(?:нажмите,?\s+чтобы\s+)?(?:войти|вход)(?:\s+в\s+\S.*)?`,
		signInLead:    `(?:войти|вход)(?:\s+здесь)?|ссылка для входа`,
		delivery:      `ссылк\S*\s+для\s+входа|эт[ау]\s+ссылк\S*|ссылк\S*[^\n]{0,60}(?:действительн|истеч)`,
		alert:         `нов\S*\s+вход|попытк\S*\s+входа|необычн`,
		confirm:       `(?:подтвердите|подтвердить|активируйте)\s+(?:свой\s+|ваш\s+)?(?:адрес\s+)?(?:электронной почты|e-?mail|почту|аккаунт|учетную запись)`,
		reset:         `(?:сбросить|сбросьте|изменить)\s+парол\S*`,
		clickTo:       `нажмите,?\s+чтобы\s+`,
		fallback:      `кнопк\S*\s+не\s+работает`,
	},
	{
		lang:          "nl",
		codeHints:     `\b(?:verificatie|inlog|eenmalige\s*)code\b`,
		ambiguous:     `\b(?:bevestigings|beveiligings|activatie)code\b|\b(?:je|uw) code\b`,
		codeBefore:    `\bis (?:je|uw)\b`,
		signIn:        `\b(?:inloggen|aanmelden|verifi(?:catie|eer|eren)|wachtwoord)\b`,
		enter:         `\b(?:voer|vul|plak)\b`,
		replyPrefix:   `antw|dr`,
		replyHeader:   `Op .+ schreef .+:`,
		replyFrom:     `van`,
		replySent:     `verzonden|datum`,
		signInSubject: `\binloggen\b|\baanmelden\b|\binloglink\b`,
		signInLabel:   `(?:klik hier om )?(?:in te loggen|inloggen|aanmelden)`,
		signInLead:    `(?:inloggen|aanmelden)(?: hier)?|inloglink`,
		delivery:      `\b(?:inlog|aanmeld)link\b|\bdeze link\b|\blink\b[^\n]{0,60}\b(?:verloopt|geldig)`,
		alert:         `\bnieuwe (?:aanmelding|login|inlog)|\binlogpoging|\bongebruikelijk`,
		confirm:       `(?:bevestig|verifieer|activeer)\s+(?:je\s+|uw\s+)?(?:e-?mail(?:adres)?|account)`,
		reset:         `wachtwoord\s+(?:opnieuw\s+instellen|resetten|wijzigen)`,
		clickTo:       `klik hier om `,
		fallback:      `(?:knop|link)\s+werkt\s+niet`,
	},
	{
		lang:          "tr",
		codeHints:     `\bdoğrulama kodu(?:nuz|n)?|\bgiriş kodu(?:nuz|n)?|\btek kullanımlık (?:kod|şifre)`,
		ambiguous:     `\b(?:onay|güvenlik|aktivasyon) kodu(?:nuz|n)?|\bkodunuz\b`,
		signIn:        `\bgiriş|\boturum aç|\bdoğrula|\bşifre`,
		enter:         `\bgir(?:in|iniz)\b|\byapıştır`,
		codeWords:     `\bkod`,
		replyPrefix:   `ynt|ilt`,
		replyFrom:     `kimden`,
		replySent:     `gönderildi|tarih`,
		signInSubject: `\bgiriş yap|\boturum aç|\bgiriş bağlantısı`,
		signInLabel:   `giriş yap(?:ın)?|oturum aç(?:ın)?`,
		signInLead:    `giriş yap(?:ın)?|oturum aç(?:ın)?|giriş bağlantısı`,
		delivery:      `\bgiriş bağlantısı|\bbu bağlantı|\bbağlantı[^\n]{0,60}(?:geçerli|sona)`,
		alert:         `\byeni (?:giriş|oturum|cihaz)|\bgiriş denemesi|\bolağandışı`,
		confirm:       `(?:e-?posta(?:\s+adresinizi|nızı)?|hesabınızı)\s+(?:doğrula|onayla|etkinleştir)\S*`,
		reset:         `şifre(?:nizi)?\s+(?:sıfırla|değiştir)\S*`,
		fallback:      `düğme\s+çalışmıyor`,
	},
	{
		lang:          "vi",
		codeHints:     `mã xác minh|mã xác thực|mã đăng nhập|mật khẩu một lần`,
		ambiguous:     `mã xác nhận|mã bảo mật|mã kích hoạt|mã của bạn`,
		copulas:       `(?:của bạn\s+)?là`,
		codeBefore:    `là mã`,
		signIn:        `đăng nhập|xác minh|xác thực|mật khẩu`,
		enter:         `nhập|dán`,
		codeWords:     `mã`,
		replyFrom:     `từ`,
		replySent:     `ngày gửi`,
		signInSubject: `đăng nhập`,
		signInLabel:   `(?:nhấp vào đây để\s+)?đăng nhập(?:\s+vào\s+\S.*)?`,
		signInLead:    `đăng nhập(?:\s+tại đây)?|liên kết đăng nhập`,
		delivery:      `liên kết đăng nhập|liên kết này|liên kết[^\n]{0,60}(?:hết hạn|hiệu lực)`,
		alert:         `đăng nhập mới|thiết bị mới|bất thường`,
		confirm:       `(?:xác nhận|xác minh|kích hoạt)\s+(?:địa chỉ\s+)?(?:email|tài khoản)(?:\s+của bạn)?`,
		reset:         `(?:đặt lại|thay đổi)\s+mật khẩu`,
		clickTo:       `nhấp vào đây để\s+`,
		fallback:      `nút\s+không\s+hoạt động`,
	},
}

// words is one field of every lexicon as a single alternative group.
func words(field func(lexicon) string) string {
	var parts []string
	for _, l := range lexicons {
		if s := field(l); s != "" {
			parts = append(parts, s)
		}
	}
	// The text is read with Traditional characters made Simplified, which
	// Japanese kanji share (認証 reads 认証): words go the same way.
	return simplified("(?:" + strings.Join(parts, "|") + ")")
}

// ci compiles a case-insensitive pattern.
func ci(pattern string) *regexp.Regexp { return regexp.MustCompile(`(?i)` + pattern) }
