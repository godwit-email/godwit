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

const maxLabelName = 32 // runes

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

// seedLabels writes the presets once; a preset the user deleted stays gone.
func (s *Service) seedLabels(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT 1 FROM settings WHERE key = 'labels_seeded'")
	if err != nil || len(rows.Values) > 0 {
		return err
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	zh := strings.HasPrefix(strings.ToLower(cmp.Or(st.UILanguage, s.opts.SystemLanguage)), "zh")
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
	case utf8.RuneCountInString(in.Name) > maxLabelName:
		return in, fmt.Errorf("a label name has at most %d characters", maxLabelName)
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
