package mail_test

import (
	"testing"

	"github.com/godwit-email/godwit/core/mail"
)

// Each lexicon's sample mails: a code, a code before its hint, a code other
// mail would use too, a sign-in link, a confirmation link, a reset link and
// a security alert, in that language's usual wording.
func TestLexicons(t *testing.T) {
	type code struct{ subject, text, want string }
	type link struct{ subject, html, want, kind string }
	for _, tc := range []struct {
		lang  string
		codes []code
		links []link
	}{
		{"zh-Hant", []code{
			{"Acme 驗證碼", "您的驗證碼為 482913，10 分鐘內有效。", "482913"},
			{"Acme 帳戶", "您的認證碼：482913，請勿告知他人。", "482913"},
			{"482913 是您的驗證碼", "請於 10 分鐘內輸入。", "482913"},
			{"信用卡資料", "安全碼：123", ""},
		}, []link{
			{"Acme 登入連結", `<p>此連結 10 分鐘內有效。</p><a href="https://acme.example/l/1">按此登入</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"歡迎加入 Acme", `<a href="https://acme.example/c/1">確認您的電郵地址</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"重新設定密碼", `<a href="https://acme.example/r/1">重新設定密碼</a>`, "https://acme.example/r/1", mail.LinkReset},
			{"新的登入通知", `<p>此連結可查看活動。</p><a href="https://acme.example/a">登入</a>`, "", ""},
		}},
		{"ja", []code{
			{"【Acme】認証コードのお知らせ", "あなたの認証コードは 482913 です。10分間有効です。", "482913"},
			{"ご注文の確認", "クーポンのセキュリティコード：AB12", ""},
		}, []link{
			{"Acme ログインリンク", `<p>このリンクは10分間有効です。</p><a href="https://acme.example/l/1">ログイン</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"メールアドレスの確認", `<a href="https://acme.example/c/1">メールアドレスを確認する</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"パスワード再設定のご案内", `<a href="https://acme.example/r/1">パスワードを再設定する</a>`, "https://acme.example/r/1", mail.LinkReset},
			{"新しいログインがありました", `<p>このリンクから確認できます。</p><a href="https://acme.example/a">ログイン</a>`, "", ""},
		}},
		{"ko", []code{
			{"[Acme] 인증번호 안내", "인증번호는 482913입니다.", "482913"},
			{"Acme 로그인", "인증번호 [482913]을 입력해 주세요.", "482913"},
			{"주문 안내", "보안 코드: AB12", ""},
		}, []link{
			{"Acme 로그인 링크", `<p>이 링크는 10분 동안 유효합니다.</p><a href="https://acme.example/l/1">로그인</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"Acme 가입을 환영합니다", `<a href="https://acme.example/c/1">이메일 인증하기</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"비밀번호 재설정", `<a href="https://acme.example/r/1">비밀번호 재설정</a>`, "https://acme.example/r/1", mail.LinkReset},
		}},
		{"de", []code{
			{"Ihr Bestätigungscode", "Ihr Bestätigungscode lautet: 482913", "482913"},
			{"Acme", "482913 ist Ihr Bestätigungscode.", "482913"},
			{"Sommerrabatt", "Ihr Code: SOMMER20", ""},
		}, []link{
			{"Anmelden bei Acme", `<p>Dieser Link ist 10 Minuten gültig.</p><a href="https://acme.example/l/1">Anmelden</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"Willkommen bei Acme", `<a href="https://acme.example/c/1">E-Mail-Adresse bestätigen</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"Passwort vergessen?", `<a href="https://acme.example/r/1">Passwort zurücksetzen</a>`, "https://acme.example/r/1", mail.LinkReset},
			{"Neue Anmeldung bei Ihrem Konto", `<p>Dieser Link zeigt die Aktivität.</p><a href="https://acme.example/a">Anmelden</a>`, "", ""},
		}},
		{"fr", []code{
			{"Votre code de vérification", "Votre code de vérification est : 482913", "482913"},
			{"Acme", "482913 est votre code de vérification.", "482913"},
			{"Soldes", "Votre code : SOLDES20", ""},
		}, []link{
			{"Votre lien de connexion", `<p>Ce lien expire dans 10 minutes.</p><a href="https://acme.example/l/1">Se connecter</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"Bienvenue", `<a href="https://acme.example/c/1">Confirmez votre adresse e-mail</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"Mot de passe oublié", `<a href="https://acme.example/r/1">Réinitialiser votre mot de passe</a>`, "https://acme.example/r/1", mail.LinkReset},
		}},
		{"es", []code{
			{"Tu código de verificación", "Tu código de verificación es 482913", "482913"},
			{"Rebajas", "Tu código: VERANO20", ""},
		}, []link{
			{"Inicia sesión en Acme", `<p>Este enlace caduca en 10 minutos.</p><a href="https://acme.example/l/1">Iniciar sesión</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"Bienvenido", `<a href="https://acme.example/c/1">Confirma tu correo electrónico</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"¿Olvidaste tu contraseña?", `<a href="https://acme.example/r/1">Restablecer contraseña</a>`, "https://acme.example/r/1", mail.LinkReset},
		}},
		{"pt", []code{
			{"Seu código de verificação", "Seu código de verificação é 482913", "482913"},
		}, []link{
			{"Entrar no Acme", `<p>Este link expira em 10 minutos.</p><a href="https://acme.example/l/1">Entrar</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"Bem-vindo", `<a href="https://acme.example/c/1">Confirme seu e-mail</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"Esqueceu a senha?", `<a href="https://acme.example/r/1">Redefinir senha</a>`, "https://acme.example/r/1", mail.LinkReset},
		}},
		{"it", []code{
			{"Il tuo codice di verifica", "Il tuo codice di verifica è 482913", "482913"},
		}, []link{
			{"Accedi ad Acme", `<p>Questo link scade tra 10 minuti.</p><a href="https://acme.example/l/1">Accedi</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"Benvenuto", `<a href="https://acme.example/c/1">Conferma il tuo indirizzo email</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"Password dimenticata", `<a href="https://acme.example/r/1">Reimposta password</a>`, "https://acme.example/r/1", mail.LinkReset},
		}},
		{"ru", []code{
			{"Код подтверждения", "Ваш код подтверждения: 482913", "482913"},
			{"Скидки", "Ваш код: LETO20", ""},
		}, []link{
			{"Вход в Acme", `<p>Эта ссылка действительна 10 минут.</p><a href="https://acme.example/l/1">Войти</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"Добро пожаловать", `<a href="https://acme.example/c/1">Подтвердите адрес электронной почты</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"Восстановление пароля", `<a href="https://acme.example/r/1">Сбросить пароль</a>`, "https://acme.example/r/1", mail.LinkReset},
		}},
		{"nl", []code{
			{"Je verificatiecode", "Je verificatiecode is 482913", "482913"},
		}, []link{
			{"Inloggen bij Acme", `<p>Deze link verloopt over 10 minuten.</p><a href="https://acme.example/l/1">Inloggen</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"Welkom", `<a href="https://acme.example/c/1">Bevestig je e-mailadres</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"Wachtwoord vergeten", `<a href="https://acme.example/r/1">Wachtwoord opnieuw instellen</a>`, "https://acme.example/r/1", mail.LinkReset},
		}},
		{"tr", []code{
			{"Doğrulama kodu", "Doğrulama kodunuz: 482913", "482913"},
		}, []link{
			{"ACME'YE GİRİŞ YAP", `<p>Bu bağlantı 10 dakika geçerlidir.</p><a href="https://acme.example/l/2">GİRİŞ YAP</a>`, "https://acme.example/l/2", mail.LinkSignIn},
			{"Acme'ye giriş yap", `<p>Bu bağlantı 10 dakika geçerlidir.</p><a href="https://acme.example/l/1">Giriş yap</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"Hoş geldiniz", `<a href="https://acme.example/c/1">E-posta adresinizi doğrulayın</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"Şifre sıfırlama", `<a href="https://acme.example/r/1">Şifrenizi sıfırlayın</a>`, "https://acme.example/r/1", mail.LinkReset},
		}},
		{"vi", []code{
			{"Mã xác minh Acme", "Mã xác minh của bạn là 482913", "482913"},
			{"Acme", "482913 là mã xác minh của bạn", "482913"},
		}, []link{
			{"Đăng nhập vào Acme", `<p>Liên kết này có hiệu lực trong 10 phút.</p><a href="https://acme.example/l/1">Đăng nhập</a>`, "https://acme.example/l/1", mail.LinkSignIn},
			{"Chào mừng", `<a href="https://acme.example/c/1">Xác nhận email của bạn</a>`, "https://acme.example/c/1", mail.LinkConfirm},
			{"Đặt lại mật khẩu", `<a href="https://acme.example/r/1">Đặt lại mật khẩu</a>`, "https://acme.example/r/1", mail.LinkReset},
		}},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			for _, c := range tc.codes {
				if got := mail.SignInCode(c.subject, c.text, ""); got != c.want {
					t.Errorf("code in %q / %q: got %q, want %q", c.subject, c.text, got, c.want)
				}
			}
			for _, l := range tc.links {
				if got, kind := mail.ActionLink(l.subject, "", l.html); got != l.want || kind != l.kind {
					t.Errorf("link in %q: got %q %q, want %q %q", l.subject, got, kind, l.want, l.kind)
				}
			}
		})
	}
}
