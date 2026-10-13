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
	"github.com/godwit-email/godwit/core/store"
)

// LabelColors are the colors a label may take, in the picker's order.
var LabelColors = []string{"gray", "red", "orange", "amber", "green", "blue", "violet", "pink"}

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
type presetLabel struct{ slug, color, icon, zh, en, zhDesc, enDesc string }

var presetLabels = []presetLabel{
	{"otp", "red", "shield-keyhole", "验证码", "Codes", "登录、注册、支付时收到的一次性验证码或登录链接", "One-time codes or sign-in links for logging in, signing up or paying"},
	{"bill", "amber", "bill", "账单", "Bills", "账单、收据、发票和扣款通知", "Bills, receipts, invoices and payment notices"},
	{"order", "orange", "truck", "订单与物流", "Orders", "网购订单确认、发货和快递进度", "Order confirmations, shipping and delivery updates"},
	{"travel", "blue", "plane", "航班与出行", "Travel", "机票、火车票、酒店预订和值机提醒", "Flights, train tickets, hotel bookings and check-in reminders"},
	{"calendar", "green", "calendar-event", "日程邀请", "Invites", "会议邀请、日程变更和活动报名确认", "Meeting invites, schedule changes and event sign-ups"},
	{"notice", "gray", "notification-3", "通知", "Notices", "系统或服务自动发出的提醒，不需要回复", "Automatic notices from systems or services that need no reply"},
	{"newsletter", "gray", "newspaper", "订阅与资讯", "Newsletters", "新闻简报、产品更新和营销邮件", "Newsletters, product updates and promotions"},
}

// seedLabels writes the presets once, in the UI language; a preset the user
// deleted stays gone. With no language known yet (a headless first start) it
// waits for a start that knows one.
func (s *Service) seedLabels(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT 1 FROM settings WHERE key = 'labels_seeded'")
	if err != nil || len(rows.Values) > 0 {
		return err
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	lang := strings.ToLower(cmp.Or(st.UILanguage, s.opts.SystemLanguage))
	if lang == "" {
		return nil
	}
	zh := strings.HasPrefix(lang, "zh")
	var stmts []sqlite.Statement
	for i, p := range presetLabels {
		name, desc := p.en, p.enDesc
		if zh {
			name, desc = p.zh, p.zhDesc
		}
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO labels(slug, name, description, color, icon, builtin, position)
			VALUES (?, ?, ?, ?, ?, 1, ?)`, Args: []any{p.slug, name, desc, p.color, p.icon, i}})
	}
	stmts = append(stmts, sqlite.Statement{SQL: "INSERT INTO settings(key, value) VALUES ('labels_seeded', '1')"})
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
		SELECT m.account_id, ` + messageFP + `, ?, ? FROM messages m WHERE m.id IN (` + placeholders(len(messageIDs)) + `)
		AND EXISTS (SELECT 1 FROM label_keep k WHERE k.account_id = m.account_id AND k.fp = ` + messageFP + `)`,
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

// messageFP is a message m's fingerprint in label_keep, as the schema's
// triggers compute it: its Message-ID, else sender, date, subject, size and
// the start of its text.
const messageFP = "CASE WHEN COALESCE(m.message_id, '') != '' THEN m.message_id ELSE 'fp:' || COALESCE(m.from_addr, '') || char(31) || COALESCE(m.date, '') || char(31) || COALESCE(m.subject, '') || char(31) || COALESCE(m.size, '') || char(31) || COALESCE(substr(m.body_text, 1, 512), '') END"
