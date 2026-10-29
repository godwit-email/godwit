package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
)

// LabelColors are the colors a label may take, in the picker's order.
var LabelColors = []string{"gray", "red", "orange", "amber", "yellow", "lime", "green", "teal", "sky", "blue", "violet", "pink"}

// LabelIcons are the icons a label may take, in the picker's order; the GUI
// draws each as "label-<name>".
var LabelIcons = []string{"shield-keyhole", "bill", "truck", "plane", "calendar-event", "notification-3", "newspaper", "shopping-bag-3",
	"folder-3", "money-dollar-circle", "book-2", "graduation-cap", "pencil", "quill-pen", "braces", "terminal-box",
	"music-2", "movie-2", "tools", "palette", "stethoscope", "first-aid-kit", "seedling", "briefcase-4",
	"bar-chart-box", "run", "sticky-note", "scales-3", "earth", "global", "hammer", "footprint",
	"flask", "brain", "heart-3", "plant", "team", "home-4", "key-2", "chat-3"}

// ErrLabelExists is a label name already taken, ignoring case and outer spaces.
var ErrLabelExists = errors.New("a label with this name exists")

// MaxLabelName is the most runes a label name has.
const MaxLabelName = 32

// MaxLabelDescription is the most runes a label description has: every
// classification sends every description to the model.
const MaxLabelDescription = 500

// Label is a mail label. Count is the messages it is on, Unread those unread.
type Label struct {
	ID                                   int64
	Slug, Name, Description, Color, Icon string
	Builtin                              bool
	Position                             int
	Count, Unread                        int
}

// LabelInput is what the user sets on a label.
type LabelInput struct{ Name, Description, Color, Icon string }

// presetLabel is a label seeded once, in the first language Godwit ran in.
type presetLabel struct {
	slug, color, icon string
	name, desc        plugins.Text
}

var presetLabels = []presetLabel{
	{
		slug: "otp", color: "red", icon: "shield-keyhole",
		name: plugins.Text{Default: "Codes", ByLang: map[string]string{
			"zh-Hans": "验证码", "zh-Hant": "驗證碼", "ja": "認証コード", "ko": "인증번호",
			"de": "Codes", "fr": "Codes", "es": "Códigos", "pt": "Códigos",
			"it": "Codici", "ru": "Коды", "nl": "Codes", "tr": "Kodlar", "vi": "Mã xác minh",
		}},
		desc: plugins.Text{Default: "One-time codes or sign-in links for logging in, signing up or paying", ByLang: map[string]string{
			"zh-Hans": "登录、注册、支付时收到的一次性验证码或登录链接",
			"zh-Hant": "登入、註冊、付款時收到的一次性驗證碼或登入連結",
			"ja":      "ログイン、登録、支払い時に届くワンタイムコードやログインリンク",
			"ko":      "로그인, 가입, 결제 시 받는 일회용 인증 코드 또는 로그인 링크",
			"de":      "Einmalcodes oder Anmeldelinks für Anmeldung, Registrierung oder Zahlung",
			"fr":      "Codes à usage unique ou liens de connexion pour se connecter, s’inscrire ou payer",
			"es":      "Códigos de un solo uso o enlaces para iniciar sesión, registrarse o pagar",
			"pt":      "Códigos de uso único ou links de acesso para entrar, cadastrar-se ou pagar",
			"it":      "Codici monouso o link di accesso per accedere, registrarsi o pagare",
			"ru":      "Одноразовые коды или ссылки для входа, регистрации и оплаты",
			"nl":      "Eenmalige codes of inloglinks voor inloggen, registreren of betalen",
			"tr":      "Giriş yapma, kaydolma veya ödeme için tek kullanımlık kodlar ve giriş bağlantıları",
			"vi":      "Mã dùng một lần hoặc liên kết đăng nhập khi đăng nhập, đăng ký hoặc thanh toán",
		}},
	},
	{
		slug: "bill", color: "amber", icon: "bill",
		name: plugins.Text{Default: "Bills", ByLang: map[string]string{
			"zh-Hans": "账单", "zh-Hant": "帳單", "ja": "請求・明細", "ko": "청구서",
			"de": "Rechnungen", "fr": "Factures", "es": "Facturas", "pt": "Faturas",
			"it": "Fatture", "ru": "Счета", "nl": "Facturen", "tr": "Faturalar", "vi": "Hóa đơn",
		}},
		desc: plugins.Text{Default: "Bills, receipts, invoices and payment notices", ByLang: map[string]string{
			"zh-Hans": "账单、收据、发票和扣款通知",
			"zh-Hant": "帳單、收據、發票和扣款通知",
			"ja":      "請求書、領収書、インボイス、引き落とし通知",
			"ko":      "청구서, 영수증, 세금계산서 및 결제 알림",
			"de":      "Rechnungen, Quittungen, Belege und Zahlungsbenachrichtigungen",
			"fr":      "Factures, reçus et avis de paiement ou de prélèvement",
			"es":      "Facturas, recibos y avisos de cobro o pago",
			"pt":      "Faturas, recibos, notas fiscais e avisos de pagamento",
			"it":      "Fatture, ricevute e avvisi di pagamento",
			"ru":      "Счета, квитанции, чеки и уведомления об оплате",
			"nl":      "Facturen, betalingsbewijzen, bonnen en afschrijvingen",
			"tr":      "Faturalar, makbuzlar ve ödeme bildirimleri",
			"vi":      "Hóa đơn, biên lai và thông báo thanh toán",
		}},
	},
	{
		slug: "order", color: "orange", icon: "truck",
		name: plugins.Text{Default: "Orders", ByLang: map[string]string{
			"zh-Hans": "订单与物流", "zh-Hant": "訂單與物流", "ja": "注文・配送", "ko": "주문 및 배송",
			"de": "Bestellungen", "fr": "Commandes", "es": "Pedidos", "pt": "Pedidos",
			"it": "Ordini", "ru": "Заказы", "nl": "Bestellingen", "tr": "Siparişler", "vi": "Đơn hàng",
		}},
		desc: plugins.Text{Default: "Order confirmations, shipping and delivery updates", ByLang: map[string]string{
			"zh-Hans": "网购订单确认、发货和快递进度",
			"zh-Hant": "網購訂單確認、出貨和快遞進度",
			"ja":      "注文確認、発送通知、配送状況のお知らせ",
			"ko":      "주문 확인, 발송 및 배송 진행 알림",
			"de":      "Bestellbestätigungen, Versand- und Lieferstatus",
			"fr":      "Confirmations de commande, expéditions et suivi de livraison",
			"es":      "Confirmaciones de pedido, envíos y actualizaciones de entrega",
			"pt":      "Confirmações de pedido, envio e atualizações de entrega",
			"it":      "Conferme d’ordine, spedizioni e aggiornamenti sulla consegna",
			"ru":      "Подтверждения заказов, отправка и статус доставки",
			"nl":      "Bestelbevestigingen, verzending en bezorgupdates",
			"tr":      "Sipariş onayları, kargo ve teslimat güncellemeleri",
			"vi":      "Xác nhận đơn hàng, vận chuyển và cập nhật giao hàng",
		}},
	},
	{
		slug: "travel", color: "blue", icon: "plane",
		name: plugins.Text{Default: "Travel", ByLang: map[string]string{
			"zh-Hans": "航班与出行", "zh-Hant": "航班與出行", "ja": "旅行・移動", "ko": "여행 및 교통",
			"de": "Reisen", "fr": "Voyages", "es": "Viajes", "pt": "Viagens",
			"it": "Viaggi", "ru": "Поездки", "nl": "Reizen", "tr": "Seyahat", "vi": "Di chuyển",
		}},
		desc: plugins.Text{Default: "Flights, train tickets, hotel bookings and check-in reminders", ByLang: map[string]string{
			"zh-Hans": "机票、火车票、酒店预订和值机提醒",
			"zh-Hant": "機票、火車票、飯店預訂和報到提醒",
			"ja":      "航空券、鉄道チケット、ホテル予約、チェックイン案内",
			"ko":      "항공권, 기차표, 호텔 예약 및 체크인 알림",
			"de":      "Flüge, Bahntickets, Hotelbuchungen und Check-in-Erinnerungen",
			"fr":      "Vols, billets de train, réservations d’hôtel et rappels d’enregistrement",
			"es":      "Vuelos, billetes de tren, reservas de hotel y recordatorios de check-in",
			"pt":      "Voos, passagens, reservas de hotel e lembretes de check-in",
			"it":      "Voli, biglietti del treno, prenotazioni alberghiere e promemoria di check-in",
			"ru":      "Авиабилеты, билеты на поезд, бронирование отелей и напоминания о регистрации",
			"nl":      "Vluchten, treinkaartjes, hotelboekingen en incheckherinneringen",
			"tr":      "Uçuşlar, tren biletleri, otel rezervasyonları ve check-in hatırlatmaları",
			"vi":      "Vé máy bay, vé tàu, đặt phòng khách sạn và nhắc làm thủ tục",
		}},
	},
	{
		slug: "calendar", color: "green", icon: "calendar-event",
		name: plugins.Text{Default: "Invites", ByLang: map[string]string{
			"zh-Hans": "日程邀请", "zh-Hant": "日程邀請", "ja": "予定・招待", "ko": "일정 초대",
			"de": "Termine", "fr": "Invitations", "es": "Invitaciones", "pt": "Convites",
			"it": "Inviti", "ru": "Встречи", "nl": "Uitnodigingen", "tr": "Davetler", "vi": "Lời mời lịch",
		}},
		desc: plugins.Text{Default: "Meeting invites, schedule changes and event sign-ups", ByLang: map[string]string{
			"zh-Hans": "会议邀请、日程变更和活动报名确认",
			"zh-Hant": "會議邀請、日程變更和活動報名確認",
			"ja":      "会議の招待、予定の変更、イベント参加登録の確認",
			"ko":      "회의 초대, 일정 변경 및 행사 신청 확인",
			"de":      "Besprechungseinladungen, Terminänderungen und Veranstaltungsanmeldungen",
			"fr":      "Invitations à des réunions, modifications d’horaire et inscriptions à des événements",
			"es":      "Invitaciones a reuniones, cambios de horario e inscripciones a eventos",
			"pt":      "Convites para reuniões, alterações de agenda e inscrições em eventos",
			"it":      "Inviti a riunioni, modifiche di programma e iscrizioni a eventi",
			"ru":      "Приглашения на встречи, изменения расписания и регистрация на мероприятия",
			"nl":      "Vergaderverzoeken, roosterwijzigingen en aanmeldingen voor evenementen",
			"tr":      "Toplantı davetleri, program değişiklikleri ve etkinlik kayıtları",
			"vi":      "Lời mời họp, thay đổi lịch trình và xác nhận đăng ký sự kiện",
		}},
	},
	{
		slug: "notice", color: "gray", icon: "notification-3",
		name: plugins.Text{Default: "Notices", ByLang: map[string]string{
			"zh-Hans": "通知", "zh-Hant": "通知", "ja": "通知", "ko": "알림",
			"de": "Hinweise", "fr": "Notifications", "es": "Avisos", "pt": "Avisos",
			"it": "Avvisi", "ru": "Уведомления", "nl": "Meldingen", "tr": "Bildirimler", "vi": "Thông báo",
		}},
		desc: plugins.Text{Default: "Automatic notices from systems or services that need no reply", ByLang: map[string]string{
			"zh-Hans": "系统或服务自动发出的提醒，不需要回复",
			"zh-Hant": "系統或服務自動發出的提醒，不需要回覆",
			"ja":      "システムやサービスからの自動通知（返信不要なもの）",
			"ko":      "회신이 필요 없는 시스템이나 서비스의 자동 알림",
			"de":      "Automatische System- oder Dienstbenachrichtigungen, die keine Antwort erfordern",
			"fr":      "Notifications automatiques de systèmes ou services ne nécessitant pas de réponse",
			"es":      "Avisos automáticos de sistemas o servicios que no requieren respuesta",
			"pt":      "Avisos automáticos de sistemas ou serviços que não precisam de resposta",
			"it":      "Avvisi automatici da sistemi o servizi che non richiedono risposta",
			"ru":      "Автоматические уведомления от систем и сервисов, не требующие ответа",
			"nl":      "Automatische meldingen van systemen of diensten waarop geen antwoord nodig is",
			"tr":      "Sistemlerden veya hizmetlerden gelen, yanıt gerektirmeyen otomatik bildirimler",
			"vi":      "Thông báo tự động từ hệ thống hoặc dịch vụ không cần trả lời",
		}},
	},
	{
		slug: "newsletter", color: "gray", icon: "newspaper",
		name: plugins.Text{Default: "Newsletters", ByLang: map[string]string{
			"zh-Hans": "订阅与资讯", "zh-Hant": "訂閱與資訊", "ja": "ニュースレター", "ko": "뉴스레터",
			"de": "Newsletter", "fr": "Newsletters", "es": "Boletines", "pt": "Newsletters",
			"it": "Newsletter", "ru": "Рассылки", "nl": "Nieuwsbrieven", "tr": "Bültenler", "vi": "Bản tin",
		}},
		desc: plugins.Text{Default: "Newsletters, product updates and promotions", ByLang: map[string]string{
			"zh-Hans": "新闻简报、产品更新和营销邮件",
			"zh-Hant": "電子報、產品更新和行銷郵件",
			"ja":      "ニュースレター、製品アップデート、プロモーションメール",
			"ko":      "뉴스레터, 제품 업데이트 및 프로모션 메일",
			"de":      "Newsletter, Produktneuheiten und Werbe-E-Mails",
			"fr":      "Newsletters, nouveautés produit et e-mails promotionnels",
			"es":      "Boletines informativos, novedades de productos y promociones",
			"pt":      "Newsletters, novidades de produtos e e-mails promocionais",
			"it":      "Newsletter, aggiornamenti di prodotto ed email promozionali",
			"ru":      "Дайджесты, обновления продуктов и рекламные рассылки",
			"nl":      "Nieuwsbrieven, productupdates en aanbiedingen",
			"tr":      "Haber bültenleri, ürün güncellemeleri ve tanıtım e-postaları",
			"vi":      "Bản tin, cập nhật sản phẩm và thư quảng cáo",
		}},
	},
}

// seedLabels writes the presets once, in the UI language; a preset the user
// deleted stays gone. With no language known yet (a headless first start) it
// waits for a start that knows one.
func (s *Service) seedLabels(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT 1 FROM meta WHERE key = 'labels_seeded'")
	if err != nil || len(rows.Values) > 0 {
		return err
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	lang := cmp.Or(st.UILanguage, s.opts.SystemLanguage)
	if lang == "" {
		return nil
	}
	// A label made before (headless, through the Local API) keeps its name:
	// the preset of that name is skipped, as CreateLabel would refuse it.
	have, err := s.db.Query(ctx, "SELECT name FROM labels")
	if err != nil {
		return err
	}
	var stmts []sqlite.Statement
	for i, p := range presetLabels {
		name, desc := p.name.For(lang), p.desc.For(lang)
		if slices.ContainsFunc(have.Values, func(r []any) bool { return strings.EqualFold(strings.TrimSpace(store.Str(r[0])), name) }) {
			continue
		}
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO labels(slug, name, description, color, icon, builtin, position)
			VALUES (?, ?, ?, ?, ?, 1, ?)`, Args: []any{p.slug, name, desc, p.color, p.icon, i}})
	}
	stmts = append(stmts, sqlite.Statement{SQL: "INSERT INTO meta(key, value) VALUES ('labels_seeded', '1')"})
	_, err = s.db.Transaction(ctx, stmts)
	return err
}

// ListLabels returns the labels in the user's order.
func (s *Service) ListLabels(ctx context.Context) ([]Label, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	return s.listLabels(ctx)
}

func (s *Service) listLabels(ctx context.Context) ([]Label, error) {
	rows, err := s.db.Query(ctx, `SELECT l.id, l.slug, l.name, l.description, l.color, l.icon, l.builtin, l.position,
		(SELECT count(*) FROM message_labels ml WHERE ml.label_id = l.id AND ml.active = 1),
		(SELECT count(*) FROM message_labels ml JOIN messages m ON m.id = ml.message_id
			WHERE ml.label_id = l.id AND ml.active = 1 AND instr(m.flags, '\Seen') = 0)
		FROM labels l ORDER BY l.position, l.id`)
	if err != nil {
		return nil, err
	}
	out := make([]Label, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, Label{ID: store.Int(r[0]), Slug: store.Str(r[1]), Name: store.Str(r[2]), Description: store.Str(r[3]),
			Color: store.Str(r[4]), Icon: store.Str(r[5]), Builtin: store.Int(r[6]) != 0, Position: int(store.Int(r[7])),
			Count: int(store.Int(r[8])), Unread: int(store.Int(r[9]))})
	}
	return out, nil
}

// clean trims in and checks it, and that no label but self (0 for none) has its name.
// The caller holds labelsMu.
func (s *Service) cleanLabel(ctx context.Context, in LabelInput, self int64) (LabelInput, error) {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	switch {
	case in.Name == "":
		return in, errors.New("a label needs a name")
	case utf8.RuneCountInString(in.Name) > MaxLabelName:
		return in, fmt.Errorf("a label name has at most %d characters", MaxLabelName)
	case utf8.RuneCountInString(in.Description) > MaxLabelDescription:
		return in, fmt.Errorf("a label description has at most %d characters", MaxLabelDescription)
	case !slices.Contains(LabelColors, in.Color):
		return in, fmt.Errorf("invalid label color %q", in.Color)
	case !slices.Contains(LabelIcons, in.Icon):
		return in, fmt.Errorf("invalid label icon %q", in.Icon)
	}
	// In Go, not SQL: SQLite's lower() leaves non-ASCII letters alone.
	rows, err := s.db.Query(ctx, "SELECT id, name FROM labels")
	if err != nil {
		return in, err
	}
	for _, r := range rows.Values {
		if store.Int(r[0]) != self && strings.EqualFold(strings.TrimSpace(store.Str(r[1])), in.Name) {
			return in, ErrLabelExists
		}
	}
	return in, nil
}

func (s *Service) label(ctx context.Context, id int64) (Label, error) {
	ls, err := s.listLabels(ctx)
	if err != nil {
		return Label{}, err
	}
	for _, l := range ls {
		if l.ID == id {
			return l, nil
		}
	}
	return Label{}, fmt.Errorf("label %d not found", id)
}

// CreateLabel adds a label at the end; its slug is "u" and its id.
func (s *Service) CreateLabel(ctx context.Context, in LabelInput) (Label, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Label{}, err
	}
	defer end()
	s.labelsMu.Lock()
	defer s.labelsMu.Unlock()
	if in, err = s.cleanLabel(ctx, in, 0); err != nil {
		return Label{}, err
	}
	res, err := s.db.Transaction(ctx, []sqlite.Statement{
		{SQL: `INSERT INTO labels(slug, name, description, color, icon, position)
			VALUES ('tmp-' || hex(randomblob(8)), ?, ?, ?, ?, (SELECT COALESCE(MAX(position) + 1, 0) FROM labels))`,
			Args: []any{in.Name, in.Description, in.Color, in.Icon}},
		{SQL: "UPDATE labels SET slug = 'u' || id WHERE id = last_insert_rowid()"},
	})
	if err != nil {
		return Label{}, err
	}
	s.bus.Publish(events.Event{Type: events.LabelsChanged})
	return s.label(ctx, res[0].LastInsertID)
}

// UpdateLabel changes a label's name, description, color and icon; its slug stays.
func (s *Service) UpdateLabel(ctx context.Context, id int64, in LabelInput) (Label, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Label{}, err
	}
	defer end()
	s.labelsMu.Lock()
	defer s.labelsMu.Unlock()
	if in, err = s.cleanLabel(ctx, in, id); err != nil {
		return Label{}, err
	}
	res, err := s.db.Execute(ctx, "UPDATE labels SET name = ?, description = ?, color = ?, icon = ? WHERE id = ?",
		in.Name, in.Description, in.Color, in.Icon, id)
	if err != nil {
		return Label{}, err
	}
	if res.Changes == 0 {
		return Label{}, fmt.Errorf("label %d not found", id)
	}
	s.bus.Publish(events.Event{Type: events.LabelsChanged})
	return s.label(ctx, id)
}

// DeleteLabel removes a label and takes it off every message.
func (s *Service) DeleteLabel(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.labelsMu.Lock()
	defer s.labelsMu.Unlock()
	if _, err := s.db.Execute(ctx, "DELETE FROM labels WHERE id = ?", id); err != nil {
		return err
	}
	s.bus.Publish(events.Event{Type: events.LabelsChanged})
	return nil
}

// ReorderLabels puts the labels in the order of ids, which names every label once.
func (s *Service) ReorderLabels(ctx context.Context, ids []int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.labelsMu.Lock()
	defer s.labelsMu.Unlock()
	ls, err := s.listLabels(ctx)
	if err != nil {
		return err
	}
	have := make([]int64, len(ls))
	for i, l := range ls {
		have[i] = l.ID
	}
	want := slices.Clone(ids)
	slices.Sort(have)
	slices.Sort(want)
	if !slices.Equal(have, want) {
		return errors.New("the new order must name every label once")
	}
	stmts := make([]sqlite.Statement, len(ids))
	for i, id := range ids {
		stmts[i] = sqlite.Statement{SQL: "UPDATE labels SET position = ? WHERE id = ?", Args: []any{i, id}}
	}
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		return err
	}
	s.bus.Publish(events.Event{Type: events.LabelsChanged})
	return nil
}

// labelCond matches a message m with the label (one arg) on.
const labelCond = "EXISTS (SELECT 1 FROM message_labels ml WHERE ml.message_id = m.id AND ml.label_id = ? AND ml.active = 1)"

// AppliedLabel is a label on a message or thread. Auto: the model set it, and
// no message of the thread has it set by hand.
type AppliedLabel struct {
	LabelID int64
	Auto    bool
}

// SetLabel puts the label on the messages (on) or takes it off, by hand: the
// classifier leaves these rows alone from then on. All or none are written.
func (s *Service) SetLabel(ctx context.Context, messageIDs []int64, labelID int64, on bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	if len(messageIDs) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, "SELECT count(*) FROM messages WHERE id IN ("+placeholders(len(messageIDs))+")", int64Args(messageIDs)...)
	if err != nil {
		return err
	}
	if int(store.Int(rows.Values[0][0])) != len(slices.Compact(slices.Sorted(slices.Values(messageIDs)))) {
		return errors.New("some messages no longer exist")
	}
	stmts := make([]sqlite.Statement, 0, len(messageIDs)+1)
	for _, id := range messageIDs {
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO message_labels(message_id, label_id, source, active) VALUES (?, ?, 'manual', ?)
			ON CONFLICT(message_id, label_id) DO UPDATE SET source = 'manual', active = excluded.active`, Args: []any{id, labelID, b2i(on)}})
	}
	// A copy that turns up later takes this choice, not one stashed before it:
	// a message with a stash has every hand-set choice in it.
	stmts = append(stmts, sqlite.Statement{SQL: `INSERT OR REPLACE INTO label_keep(account_id, fp, label_id, active)
		SELECT m.account_id, m.label_fp, ?, ? FROM messages m WHERE m.id IN (` + placeholders(len(messageIDs)) + `)
		AND EXISTS (SELECT 1 FROM label_keep k WHERE k.account_id = m.account_id AND k.fp = m.label_fp)`,
		Args: append([]any{labelID, b2i(on)}, int64Args(messageIDs)...)})
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		return err
	}
	s.bus.Publish(events.Event{Type: events.LabelsChanged})
	return nil
}

// MessageLabels returns the labels on each of the messages, in the labels' order.
func (s *Service) MessageLabels(ctx context.Context, ids []int64) (map[int64][]AppliedLabel, error) {
	return s.appliedLabels(ctx, "ml.message_id", ids)
}

// ThreadLabels returns the labels on any message of each thread, in the labels' order.
func (s *Service) ThreadLabels(ctx context.Context, threadIDs []int64) (map[int64][]AppliedLabel, error) {
	return s.appliedLabels(ctx, "m.thread_id", threadIDs)
}

func (s *Service) appliedLabels(ctx context.Context, key string, ids []int64) (map[int64][]AppliedLabel, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	out := map[int64][]AppliedLabel{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+key+`, ml.label_id, min(ml.source = 'auto') FROM message_labels ml
		JOIN messages m ON m.id = ml.message_id JOIN labels l ON l.id = ml.label_id
		WHERE ml.active = 1 AND `+key+` IN (`+placeholders(len(ids))+`)
		GROUP BY `+key+`, ml.label_id ORDER BY min(l.position), ml.label_id`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		k := store.Int(r[0])
		out[k] = append(out[k], AppliedLabel{LabelID: store.Int(r[1]), Auto: store.Int(r[2]) != 0})
	}
	return out, nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}
