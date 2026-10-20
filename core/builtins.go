package core

import (
	"context"
	"slices"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
)

// A built-in plugin ships inside Godwit as Go code: listed and switched with
// the installed plugins (ListPlugins, SetPluginEnabled), on unless the user
// turned it off, but with no files, sandbox, permissions or uninstall. It
// contributes through the plugin capability contracts (tags here), and owns any surface of its own through Service methods
// that consult builtinOn.
type builtinPlugin struct {
	name         string
	title, about plugins.Text
	// tags are what the plugin puts after a message's subject; nil for none.
	tags func(m plugins.MailMessage) []Tag
	// translates: a translation channel (translators.go) through
	// Options.SystemTranslator, listed only when the app set one.
	translates bool
}

// builtinAvailable reports whether this Godwit offers b: system translation needs the system's.
func (s *Service) builtinAvailable(b *builtinPlugin) bool {
	switch b.name {
	case SystemTranslationPlugin:
		return s.opts.SystemTranslator != nil
	case AppleIntelligencePlugin:
		return s.appleIntelligenceAvailable()
	default:
		return !b.translates || s.opts.SystemTranslator != nil
	}
}

// SignInCodesPlugin recognizes sign-in codes and links: the banner, the
// tray's recent list and the reader's card.
const SignInCodesPlugin = "sign-in-codes"

// SystemTranslationPlugin translates with the operating system's on-device
// translation (macOS 26's Translation framework).
const SystemTranslationPlugin = "system-translation"

// AppleIntelligencePlugin uses Apple Intelligence on macOS for generation,
// small tasks, and classification.
const AppleIntelligencePlugin = "apple-intelligence"

var builtinPlugins = []builtinPlugin{{
	name: SignInCodesPlugin,
	title: plugins.Text{Default: "Sign-in codes & links", ByLang: map[string]string{
		"zh-Hans": "验证码与登录链接",
		"zh-Hant": "驗證碼與登入連結",
		"ja":      "認証コードとログインリンク",
		"ko":      "인증 코드 및 로그인 링크",
		"de":      "Anmeldecodes & Links",
		"fr":      "Codes et liens de connexion",
		"es":      "Códigos y enlaces de inicio de sesión",
		"pt":      "Códigos e links de acesso",
		"it":      "Codici e link di accesso",
		"ru":      "Коды и ссылки для входа",
		"nl":      "Inlogcodes en -links",
		"tr":      "Giriş kodları ve bağlantıları",
		"vi":      "Mã và liên kết đăng nhập",
	}},
	about: plugins.Text{Default: "Finds sign-in codes and links in new mail, on this Mac, and offers them to copy or open",
		ByLang: map[string]string{
			"zh-Hans": "在本机识别新邮件里的验证码和登录链接，一键复制或打开",
			"zh-Hant": "在本機識別新郵件裡的驗證碼和登入連結，一鍵複製或打開",
			"ja":      "新着メールの認証コードやログインリンクをこの Mac 上で検出し、すぐにコピーや開くことができます",
			"ko":      "이 Mac에서 새 메일의 인증 코드와 로그인 링크를 찾아 바로 복사하거나 열 수 있게 합니다",
			"de":      "Erkennt Anmeldecodes und Links in neuen E-Mails auf diesem Mac zum direkten Kopieren oder Öffnen",
			"fr":      "Détecte les codes et liens de connexion dans les nouveaux e-mails sur ce Mac pour les copier ou les ouvrir en un clic",
			"es":      "Detecta códigos y enlaces de inicio de sesión en los correos nuevos en este Mac para copiarlos o abrirlos al instante",
			"pt":      "Identifica códigos e links de acesso em novos e-mails neste Mac para copiar ou abrir rapidamente",
			"it":      "Rileva codici e link di accesso nelle nuove email su questo Mac per copiarli o aprirli subito",
			"ru":      "Находит коды и ссылки для входа в новых письмах на этом Mac, чтобы сразу скопировать или открыть их",
			"nl":      "Herkent inlogcodes en -links in nieuwe e-mail op deze Mac om ze direct te kopiëren of te openen",
			"tr":      "Bu Mac'te yeni e-postalardaki giriş kodlarını ve bağlantılarını bulur, kolayca kopyalamanızı veya açmanızı sağlar",
			"vi":      "Nhận diện mã và liên kết đăng nhập trong thư mới ngay trên máy Mac này để sao chép hoặc mở nhanh",
		}},
	tags: signInCodeTags,
}, {
	name: SystemTranslationPlugin,
	title: plugins.Text{Default: "System translation", ByLang: map[string]string{
		"zh-Hans": "系统翻译",
		"zh-Hant": "系統翻譯",
		"ja":      "システム翻訳",
		"ko":      "시스템 번역",
		"de":      "Systemübersetzung",
		"fr":      "Traduction du système",
		"es":      "Traducción del sistema",
		"pt":      "Tradução do sistema",
		"it":      "Traduzione di sistema",
		"ru":      "Системный перевод",
		"nl":      "Systeemvertaling",
		"tr":      "Sistem çevirisi",
		"vi":      "Dịch của hệ thống",
	}},
	about: plugins.Text{Default: "Translates mail with macOS's own translation, on this Mac, free and without a model; download languages in System Settings › General › Language & Region › Translation Languages",
		ByLang: map[string]string{
			"zh-Hans": "用 macOS 自带的翻译在本机翻译邮件，免费，无需模型；语言请在“系统设置 › 通用 › 语言与地区 › 翻译语言”中下载",
			"zh-Hant": "用 macOS 內建的翻譯在本機翻譯郵件，免費，無需模型；語言請在「系統設定 › 一般 › 語言與地區 › 翻譯語言」中下載",
			"ja":      "macOS 標準の翻訳でこの Mac 上でメールを翻訳します。無料でモデル不要です。言語は「システム設定 › 一般 › 言語と地域 › 翻訳言語」でダウンロードしてください",
			"ko":      "macOS 기본 번역으로 이 Mac에서 메일을 번역합니다. 무료이며 모델이 필요 없습니다. 언어는 시스템 설정 › 일반 › 언어 및 지역 › 번역 언어에서 다운로드하세요",
			"de":      "Übersetzt E-Mails mit der Übersetzung von macOS auf diesem Mac, kostenlos und ohne Modell; Sprachen laden Sie unter Systemeinstellungen › Allgemein › Sprache & Region › Übersetzungssprachen",
			"fr":      "Traduit les e-mails avec la traduction de macOS sur ce Mac, gratuitement et sans modèle ; téléchargez les langues dans Réglages Système › Général › Langue et région › Langues de traduction",
			"es":      "Traduce el correo con la traducción de macOS en este Mac, gratis y sin modelo; descarga los idiomas en Ajustes del Sistema › General › Idioma y región › Idiomas de traducción",
			"pt":      "Traduz e-mails com a tradução do macOS neste Mac, de graça e sem modelo; baixe os idiomas em Ajustes do Sistema › Geral › Idioma e Região › Idiomas de Tradução",
			"it":      "Traduce le email con la traduzione di macOS su questo Mac, gratis e senza modello; scarica le lingue in Impostazioni di Sistema › Generali › Lingua e Zona › Lingue di traduzione",
			"ru":      "Переводит письма встроенным переводом macOS на этом Mac, бесплатно и без модели; языки загрузите в Системных настройках › Основные › Язык и регион › Языки перевода",
			"nl":      "Vertaalt e-mail met de vertaling van macOS op deze Mac, gratis en zonder model; download talen in Systeeminstellingen › Algemeen › Taal en regio › Vertaaltalen",
			"tr":      "E-postaları bu Mac'te macOS'un kendi çevirisiyle, ücretsiz ve model olmadan çevirir; dilleri Sistem Ayarları › Genel › Dil ve Bölge › Çeviri Dilleri'nden indirin",
			"vi":      "Dịch thư bằng tính năng dịch của macOS ngay trên máy Mac này, miễn phí và không cần mô hình; tải ngôn ngữ trong Cài đặt hệ thống › Cài đặt chung › Ngôn ngữ & Vùng › Ngôn ngữ dịch",
		}},
	translates: true,
}, {
	name: AppleIntelligencePlugin,
	title: plugins.Text{Default: "Apple Intelligence", ByLang: map[string]string{
		"zh-Hans": "Apple Intelligence",
		"zh-Hant": "Apple Intelligence",
		"ja":      "Apple Intelligence",
		"ko":      "Apple Intelligence",
		"de":      "Apple Intelligence",
		"fr":      "Apple Intelligence",
		"es":      "Apple Intelligence",
		"pt":      "Apple Intelligence",
		"it":      "Apple Intelligence",
		"ru":      "Apple Intelligence",
		"nl":      "Apple Intelligence",
		"tr":      "Apple Intelligence",
		"vi":      "Apple Intelligence",
	}},
	about: plugins.Text{Default: "Uses Apple Intelligence on this Mac for text generation, small tasks, and classification",
		ByLang: map[string]string{
			"zh-Hans": "在 Mac 上使用 Apple Intelligence 进行文本生成、简单任务和分类",
			"zh-Hant": "在 Mac 上使用 Apple Intelligence 進行文字生成、簡易任務和分類",
			"ja":      "この Mac 上で Apple Intelligence を使用して、テキスト生成、小さなタスク、分類を行います",
			"ko":      "이 Mac에서 Apple Intelligence를 사용하여 텍스트 생성, 간단한 작업 및 분류를 수행합니다",
			"de":      "Nutzt Apple Intelligence auf diesem Mac für Textgenerierung, kleine Aufgaben und Klassifizierung",
			"fr":      "Utilise Apple Intelligence sur ce Mac pour la génération de texte, les tâches simples et la classification",
			"es":      "Utiliza Apple Intelligence en este Mac para la generación de texto, tareas sencillas y clasificación",
			"pt":      "Usa o Apple Intelligence neste Mac para geração de texto, tarefas simples e classificação",
			"it":      "Utilizza Apple Intelligence su questo Mac per la generazione di testo, piccoli compiti e classificazione",
			"ru":      "Использует Apple Intelligence на этом Mac для генерации текста, небольших задач и классификации",
			"nl":      "Gebruikt Apple Intelligence op deze Mac voor tekstgeneratie, kleine taken en classificatie",
			"tr":      "Metin oluşturma, küçük görevler ve sınıflandırma için bu Mac'te Apple Intelligence'ı kullanır",
			"vi":      "Sử dụng Apple Intelligence trên máy Mac này để tạo văn bản, tác vụ nhỏ và phân loại",
		}},
}}

func builtin(name string) *builtinPlugin {
	for i := range builtinPlugins {
		if builtinPlugins[i].name == name {
			return &builtinPlugins[i]
		}
	}
	return nil
}

// builtinOn reports whether the built-in plugin is on: a missing setting is on.
func (s *Service) builtinOn(ctx context.Context, name string) (bool, error) {
	st, err := s.loadSettings(ctx)
	return err == nil && !slices.Contains(st.DisabledBuiltins, name), err
}

func (s *Service) builtinInfos(ctx context.Context) ([]PluginInfo, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	var out []PluginInfo
	for i := range builtinPlugins {
		b := &builtinPlugins[i]
		if !s.builtinAvailable(b) {
			continue
		}
		out = append(out, PluginInfo{Name: b.name, Title: b.title, Description: b.about, Builtin: true, Enabled: !slices.Contains(st.DisabledBuiltins, b.name)})
	}
	return out, nil
}

// setBuiltinEnabled records the switch in the settings, which announces
// settings.changed; the caller announces plugins.changed.
func (s *Service) setBuiltinEnabled(ctx context.Context, name string, on bool) error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	st.DisabledBuiltins = slices.DeleteFunc(st.DisabledBuiltins, func(n string) bool { return n == name })
	if !on {
		st.DisabledBuiltins = append(st.DisabledBuiltins, name)
	}
	_, err = s.storeSettings(ctx, st)
	return err
}

// builtinTags are the tags of the built-in plugins that are on. They read
// the stored body, links intact: the plugins' copy is sanitized.
func (s *Service) builtinTags(ctx context.Context, id int64) ([]Tag, error) {
	rows, err := s.db.Query(ctx, "SELECT subject, body_text, body_html FROM messages WHERE id = ?", id)
	if err != nil || len(rows.Values) == 0 {
		return nil, err
	}
	m := plugins.MailMessage{ID: id, Subject: store.Str(rows.Values[0][0]), Text: store.Str(rows.Values[0][1]), HTML: store.Str(rows.Values[0][2])}
	var out []Tag
	for _, b := range builtinPlugins {
		if b.tags == nil {
			continue
		}
		on, err := s.builtinOn(ctx, b.name)
		if err != nil {
			return nil, err
		}
		if !on {
			continue
		}
		for _, t := range b.tags(m) {
			t.Plugin, t.Builtin = b.name, true
			out = append(out, t)
		}
	}
	return out, nil
}

// signInCodeTags are a code to copy and the mail's sign-in, confirmation
// or password-reset link to open, its host shown.
func signInCodeTags(m plugins.MailMessage) []Tag {
	var out []Tag
	if code := mail.SignInCode(m.Subject, m.Text, m.HTML); code != "" {
		out = append(out, Tag{Kind: "copy", Text: code, Value: code})
	}
	if link, kind := mail.ActionLink(m.Subject, m.Text, m.HTML); link != "" {
		out = append(out, Tag{Kind: "open-url", Label: kind, Text: mail.LinkHost(link), Value: link})
	}
	return out
}
