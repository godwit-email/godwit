package core

import (
	"context"
	"slices"
	"strings"

	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// 回复状态 (ADR 0031), as a thread's status in a view.
const (
	ReplyStatusUnreplied = "unreplied" // the newest mail is direct, from someone else, and has no reply
	ReplyStatusWaiting   = "waiting"   // the newest mail is the user's, or a reply queued after it
)

// replyStatusDays is how far back 回复状态 reads: a new view for a long-time
// customer must not count years of old conversations as needing a reply.
const replyStatusDays = 30

// replyStatus is a conversation's state now. NewReply marks mail newer than
// the conversation's last archive; Since is when the user's last mail or reply
// was sent, for ReplyStatusWaiting.
type replyStatus struct {
	Kind     string
	NewReply bool
	Since    int64
}

// logicalMail is one message of a conversation, however many folders hold it.
type logicalMail struct {
	mid                              string
	id, date                         int64
	from, replyTo, listID, listUnsub string
	auto, precedence                 string
	to, cc                           []string
	roles                            []string
	answered                         bool
}

func (m *logicalMail) in(role string) bool { return slices.Contains(m.roles, role) }

// counted is whether the message counts in a conversation: it has a copy
// outside trash, junk and drafts.
func (m *logicalMail) counted() bool {
	return slices.ContainsFunc(m.roles, func(r string) bool { return r != "trash" && r != "junk" && r != "drafts" })
}

// folderRoles is each folder's role as folderRoleSQL gives it, All Mail
// standing in for archive with no regard to the copies a message has.
func (s *Service) folderRoles(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.Query(ctx, "SELECT id, account_id, role, role_override FROM folders")
	if err != nil {
		return nil, err
	}
	hasArchive := map[int64]bool{}
	for _, r := range rows.Values {
		if over, role := store.Str(r[3]), store.Str(r[2]); over == "archive" || over == "" && role == "archive" {
			hasArchive[store.Int(r[1])] = true
		}
	}
	out := make(map[int64]string, len(rows.Values))
	for _, r := range rows.Values {
		role := store.Str(r[2])
		switch over := store.Str(r[3]); {
		case over != "":
			role = over
		case role == "all" && !hasArchive[store.Int(r[1])]:
			role = "archive"
		}
		out[store.Int(r[0])] = role
	}
	return out, nil
}

// sentMailRead is how much of the newest sent mail mailPeople reads.
const sentMailRead = 5000

// mailPeople is who the user is and who they write to, as their mail shows.
type mailPeople struct {
	own   map[string]bool // by ownKey: the accounts' addresses, and the aliases their sent mail came from
	known map[string]bool // by gomail.AddrKey: who their sent mail went to
}

// ownKey is an address as its mailbox knows it: without a +tag, and without
// the dots Gmail ignores.
func ownKey(addr string) string {
	local, domain, ok := strings.Cut(gomail.AddrKey(addr), "@")
	if !ok {
		return local
	}
	local, _, _ = strings.Cut(local, "+")
	if domain == "gmail.com" || domain == "googlemail.com" {
		local, domain = strings.ReplaceAll(local, ".", ""), "gmail.com"
	}
	return local + "@" + domain
}

func (s *Service) mailPeople(ctx context.Context, roles map[int64]string) (mailPeople, error) {
	p := mailPeople{own: map[string]bool{}, known: map[string]bool{}}
	acc, err := s.db.Query(ctx, "SELECT email FROM accounts")
	if err != nil {
		return p, err
	}
	for _, r := range acc.Values {
		p.own[ownKey(store.Str(r[0]))] = true
	}
	var sent []int64
	for id, r := range roles {
		if r == "sent" {
			sent = append(sent, id)
		}
	}
	rows, err := s.db.Query(ctx, "SELECT from_addr, to_addrs, cc_addrs FROM messages WHERE folder_id IN (SELECT value FROM json_each(?)) ORDER BY date DESC LIMIT ?", intList(sent), sentMailRead)
	if err != nil {
		return p, err
	}
	for _, r := range rows.Values {
		p.own[ownKey(store.Str(r[0]))] = true
		for _, a := range append(jsonList(store.Str(r[1])), jsonList(store.Str(r[2]))...) {
			p.known[gomail.AddrKey(a)] = true
		}
	}
	return p, nil
}

// mine is whether m is the user's.
func (p mailPeople) mine(m *logicalMail) bool { return m.in("sent") || p.own[ownKey(m.from)] }

// replyStatuses computes the 回复状态 of every conversation that has any: they
// are read, never stored, so that the passage of time needs no job. Only those
// active in the last replyStatusDays days can have one.
func (s *Service) replyStatuses(ctx context.Context, now int64) (map[int64]replyStatus, error) {
	cutoff := now - replyStatusDays*86400
	roles, err := s.folderRoles(ctx)
	if err != nil {
		return nil, err
	}
	people, err := s.mailPeople(ctx, roles)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT m.thread_id, m.id, COALESCE(m.message_id, ''), COALESCE(m.date, 0), m.from_addr, m.to_addrs, m.cc_addrs, m.reply_to,
		m.list_id, m.list_unsubscribe, m.auto_submitted, m.precedence, m.flags, m.folder_id FROM messages m JOIN threads t ON t.id = m.thread_id
		WHERE t.last_date >= ? AND `+notPurgedCond+` ORDER BY m.id`, cutoff)
	if err != nil {
		return nil, err
	}
	threads := map[int64]map[string]*logicalMail{}
	for _, r := range rows.Values {
		tid, id, mid := store.Int(r[0]), store.Int(r[1]), store.Str(r[2])
		key := mid
		if key == "" {
			key = "\x00" + store.Str(r[1]) // no Message-ID: always its own message
		}
		if threads[tid] == nil {
			threads[tid] = map[string]*logicalMail{}
		}
		m := threads[tid][key]
		if m == nil {
			m = &logicalMail{mid: mid, id: id, date: store.Int(r[3]), from: store.Str(r[4]), to: jsonList(store.Str(r[5])), cc: jsonList(store.Str(r[6])),
				replyTo: store.Str(r[7]), listID: store.Str(r[8]), listUnsub: store.Str(r[9]), auto: store.Str(r[10]), precedence: store.Str(r[11])}
			threads[tid][key] = m
		}
		m.roles = append(m.roles, roles[store.Int(r[13])])
		m.answered = m.answered || strings.Contains(store.Str(r[12]), `\Answered`)
	}
	marks, err := s.archiveMarks(ctx)
	if err != nil {
		return nil, err
	}
	replied, err := s.db.Query(ctx, `SELECT r.thread_id, max(r.at) FROM reply_records r LEFT JOIN outbox o ON o.id = r.outbox_id
		WHERE COALESCE(o.state, '') != 'failed' GROUP BY r.thread_id`)
	if err != nil {
		return nil, err
	}
	repliedAt := make(map[int64]int64, len(replied.Values))
	for _, r := range replied.Values {
		repliedAt[store.Int(r[0])] = store.Int(r[1])
	}
	out := map[int64]replyStatus{}
	strangers := map[int64]int64{} // unreplied mail from who the user never wrote to: its row, to its conversation
	for tid, msgs := range threads {
		var newest *logicalMail
		engaged := false // the user took part in it, or has written to its sender
		for _, m := range msgs {
			if m.counted() && (newest == nil || m.date > newest.date || m.date == newest.date && m.id > newest.id) {
				newest = m
			}
			engaged = engaged || people.mine(m)
		}
		if newest == nil || newest.date < cutoff {
			continue
		}
		engaged = engaged || people.known[gomail.AddrKey(newest.from)] || people.known[gomail.AddrKey(newest.replyTo)]
		if st, ok := threadReplyStatus(newest, people, engaged, marks[tid], repliedAt[tid]); ok {
			out[tid] = st
			if st.Kind == ReplyStatusUnreplied && !engaged {
				strangers[newest.id] = tid
			}
		}
	}
	return out, s.dropMassMail(ctx, out, strangers)
}

// massMailWords in the text of a stranger's mail say it went to many: the law
// asks for them. Only mail synced since list headers are kept has those.
var massMailWords = []string{"unsubscribe", "退订", "退訂", "取消订阅", "取消訂閱", "配信停止", "구독 취소", "opt out", "opt-out", "manage preferences", "email preferences", "notification settings"}

// dropMassMail takes out the conversations whose mail in strangers is mass mail.
func (s *Service) dropMassMail(ctx context.Context, out map[int64]replyStatus, strangers map[int64]int64) error {
	if len(strangers) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(strangers))
	for id := range strangers {
		ids = append(ids, id)
	}
	args := []any{intList(ids)}
	has := make([]string, len(massMailWords))
	for i, w := range massMailWords {
		has[i] = "instr(lower(COALESCE(body_text, '')), ?) > 0"
		args = append(args, w)
	}
	rows, err := s.db.Query(ctx, "SELECT id FROM messages WHERE id IN (SELECT value FROM json_each(?)) AND ("+strings.Join(has, " OR ")+")", args...)
	if err != nil {
		return err
	}
	for _, r := range rows.Values {
		delete(out, strangers[store.Int(r[0])])
	}
	return nil
}

// threadReplyStatus is the status of a conversation whose newest counted mail
// is m, with its archive mark, and repliedAt the latest reply queued (0: none).
func threadReplyStatus(m *logicalMail, p mailPeople, engaged bool, mark archiveMark, repliedAt int64) (replyStatus, bool) {
	marked := mark.has && (m.mid != "" && mark.mid == m.mid || m.date <= mark.date)
	if marked {
		return replyStatus{}, false
	}
	if p.mine(m) {
		return replyStatus{Kind: ReplyStatusWaiting, Since: max(m.date, repliedAt)}, true
	}
	if m.in("archive") && !m.in("inbox") { // archived on another device
		return replyStatus{}, false
	}
	if repliedAt >= m.date {
		return replyStatus{Kind: ReplyStatusWaiting, Since: repliedAt}, true
	}
	if m.answered || !directMail(m, p, engaged) {
		return replyStatus{}, false
	}
	return replyStatus{Kind: ReplyStatusUnreplied, NewReply: mark.has}, true
}

// Sender addresses nobody waits for an answer from, by the words of their
// local part, split at - _ . +.
// shortcut: lists of words catch what headers don't; a model can judge the rest once AI conditions come.
var (
	// automatedWords never need a reply, whoever they are from.
	automatedWords = []string{"notification", "notifications", "notify", "alert", "alerts", "news", "newsletter", "newsletters", "update", "updates",
		"announcement", "announcements", "marketing", "promo", "promotions", "receipt", "receipts", "invoice", "invoices", "statement", "statements",
		"digest", "mailer", "daemon", "postmaster", "bounce", "bounces"}
	// roleWords can be a person, but only in answer to the user: first contact from one is a notice.
	roleWords = []string{"info", "hello", "hi", "support", "help", "service", "services", "customerservice", "accounts", "account", "billing",
		"orders", "order", "sales", "contact", "team", "admin", "security", "care", "feedback", "community"}
	// bulkSubdomains are where services send from, as the first label of an
	// address's domain. Not "mail": universities give people those.
	bulkSubdomains = []string{"email", "emails", "e-mails", "em", "mails", "news", "newsletter", "marketing", "update", "updates",
		"notification", "notifications", "notify", "message", "messages", "service"}
)

func addrWords(addr string) []string {
	local, _, _ := strings.Cut(gomail.AddrKey(addr), "@")
	return strings.FieldsFunc(local, func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == '+' })
}

// automatedSender is whether addr is a machine's.
func automatedSender(addr string) bool {
	words := addrWords(addr)
	joined := strings.Join(words, "")
	return strings.Contains(joined, "noreply") || strings.Contains(joined, "donotreply") ||
		slices.ContainsFunc(words, func(w string) bool { return slices.Contains(automatedWords, w) })
}

// impersonalSender is whether addr is a role's or a mailing service's, not a person's.
func impersonalSender(addr string) bool {
	if slices.ContainsFunc(addrWords(addr), func(w string) bool { return slices.Contains(roleWords, w) }) {
		return true
	}
	_, domain, _ := strings.Cut(gomail.AddrKey(addr), "@")
	labels := strings.Split(domain, ".")
	return len(labels) >= 3 && slices.Contains(bulkSubdomains, labels[0])
}

// directMail is whether m was written to the user: to one of their addresses
// (or Bcc, with both To and Cc empty), not to a list and not automated. With
// engaged false, the user has had nothing to do with its sender yet, so a
// role's or a mailing service's address doesn't count either.
func directMail(m *logicalMail, p mailPeople, engaged bool) bool {
	isOwn := func(a string) bool { return p.own[ownKey(a)] }
	if len(m.to) == 0 && len(m.cc) > 0 || len(m.to) > 0 && !slices.ContainsFunc(m.to, isOwn) {
		return false
	}
	if m.listID != "" || m.listUnsub != "" {
		return false
	}
	auto, _, _ := strings.Cut(m.auto, ";")
	if auto = strings.ToLower(strings.TrimSpace(auto)); auto != "" && auto != "no" {
		return false
	}
	if p := strings.ToLower(strings.TrimSpace(m.precedence)); p == "bulk" || p == "list" || p == "junk" {
		return false
	}
	senders := []string{m.from, m.replyTo}
	if slices.ContainsFunc(senders, automatedSender) {
		return false
	}
	return engaged || !slices.ContainsFunc(senders, impersonalSender)
}

// archiveMark is what a conversation's newest message was when it was archived.
type archiveMark struct {
	has  bool
	mid  string
	date int64
}

func (s *Service) archiveMarks(ctx context.Context) (map[int64]archiveMark, error) {
	rows, err := s.db.Query(ctx, "SELECT thread_id, message_id_hdr, date FROM archive_marks")
	if err != nil {
		return nil, err
	}
	out := make(map[int64]archiveMark, len(rows.Values))
	for _, r := range rows.Values {
		out[store.Int(r[0])] = archiveMark{has: true, mid: store.Str(r[1]), date: store.Int(r[2])}
	}
	return out, nil
}
