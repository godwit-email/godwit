package core

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/quotedprintable"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwitmail/godwit/core/conn"
	"github.com/godwitmail/godwit/core/events"
	"github.com/godwitmail/godwit/core/mail"
	"github.com/godwitmail/godwit/core/store"
)

const (
	MaxCacheBytes   = 2 << 30  // attachment cache budget; oldest files are evicted beyond it
	MaxMessageBytes = 25 << 20 // largest attachment or message accepted
)

// attachLimit is MaxMessageBytes; a variable so tests can lower it.
var attachLimit int64 = MaxMessageBytes

var (
	ErrTooLarge       = errors.New("attachment exceeds the size limit")
	ErrNotOnServerYet = errors.New("message has not reached the server yet")
)

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }

// attRow is an attachment joined with its message's server location.
type attRow struct {
	info                    AttachmentInfo
	part, encoding, charset string
	cachePath               string
	accountID               int64
	folder                  string
	uidValidity             uint32
	uid                     imap.UID
	onServer                bool
}

func (s *Service) loadAttachment(ctx context.Context, id int64) (attRow, error) {
	rows, err := s.db.Query(ctx, `SELECT a.part, a.encoding, a.charset, a.filename, a.content_type, a.size, a.inline, a.cache_path,
		m.account_id, f.name, m.server_uidvalidity, m.server_uid
		FROM attachments a JOIN messages m ON m.id = a.message_id LEFT JOIN folders f ON f.id = m.server_folder_id
		WHERE a.id = ?`, id)
	if err != nil {
		return attRow{}, err
	}
	if len(rows.Values) == 0 {
		return attRow{}, errors.New("attachment not found")
	}
	r := rows.Values[0]
	return attRow{
		info: AttachmentInfo{ID: id, Filename: store.Str(r[3]), ContentType: store.Str(r[4]), Size: int(store.Int(r[5])), Inline: store.Int(r[6]) != 0},
		part: store.Str(r[0]), encoding: store.Str(r[1]), charset: store.Str(r[2]), cachePath: store.Str(r[7]),
		accountID: store.Int(r[8]), folder: store.Str(r[9]), uidValidity: uint32(store.Int(r[10])), uid: imap.UID(store.Int(r[11])),
		onServer: store.Str(r[9]) != "" && r[11] != nil,
	}, nil
}

// cached returns the cache file if it exists, marking it used.
func (s *Service) cached(ctx context.Context, a attRow) (string, bool, error) {
	if a.cachePath == "" {
		return "", false, nil
	}
	if _, err := os.Stat(a.cachePath); err != nil {
		return "", false, nil
	}
	return a.cachePath, true, s.touch(ctx, a.info.ID)
}

// GetAttachment returns the attachment's cached file, downloading it first if needed.
func (s *Service) GetAttachment(ctx context.Context, attachmentID int64) (string, AttachmentInfo, error) {
	a, err := s.loadAttachment(ctx, attachmentID)
	if err != nil {
		return "", AttachmentInfo{}, err
	}
	if p, ok, err := s.cached(ctx, a); ok || err != nil {
		return p, a.info, err
	}
	unlock, err := s.lockAccount(ctx, a.accountID)
	if err != nil {
		return "", AttachmentInfo{}, err
	}
	defer unlock()
	// The row may have changed (cached, moved, deleted) while we waited for the lock.
	if a, err = s.loadAttachment(ctx, attachmentID); err != nil {
		return "", AttachmentInfo{}, err
	}
	if p, ok, err := s.cached(ctx, a); ok || err != nil {
		return p, a.info, err
	}
	info := a.info
	if int64(info.Size) > attachLimit {
		return "", info, ErrTooLarge
	}
	if !a.onServer {
		return "", info, ErrNotOnServerYet
	}
	data, err := s.download(ctx, a)
	if err != nil {
		return "", info, err
	}
	if data, err = mail.DecodePart(data, "", a.charset); err != nil { // transfer encoding was undone while streaming
		return "", info, err
	}
	if int64(len(data)) > attachLimit {
		return "", info, ErrTooLarge
	}
	path, err := filepath.Abs(filepath.Join(s.dataDir, "cache", strconv.FormatInt(a.accountID, 10), strconv.FormatInt(attachmentID, 10)))
	if err != nil {
		return "", info, err
	}
	if err := store.WriteFileAtomic(path, data); err != nil {
		return "", info, err
	}
	if _, err := s.db.Execute(ctx, "UPDATE attachments SET cache_path = ?, last_used = ? WHERE id = ?", path, time.Now().Unix(), attachmentID); err != nil {
		os.Remove(path)
		return "", info, err
	}
	return path, info, s.evict(ctx, MaxCacheBytes, attachmentID)
}

// GetRawMessage downloads the message's full original from the server, for a
// Truncated message or for saving. Messages over MaxMessageBytes give ErrTooLarge.
func (s *Service) GetRawMessage(ctx context.Context, messageID int64) ([]byte, error) {
	rows, err := s.db.Query(ctx, "SELECT account_id FROM messages WHERE id = ?", messageID)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("message %d not found", messageID)
	}
	unlock, err := s.lockAccount(ctx, store.Int(rows.Values[0][0]))
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Read under the lock: a sync may have moved or deleted it meanwhile.
	rows, err = s.db.Query(ctx, `SELECT m.account_id, f.name, m.server_uidvalidity, m.server_uid, m.size
		FROM messages m LEFT JOIN folders f ON f.id = m.server_folder_id WHERE m.id = ?`, messageID)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("message %d not found", messageID)
	}
	r := rows.Values[0]
	if store.Int(r[4]) > attachLimit {
		return nil, ErrTooLarge
	}
	if store.Str(r[1]) == "" || r[3] == nil {
		return nil, ErrNotOnServerYet
	}
	return s.download(ctx, attRow{accountID: store.Int(r[0]), folder: store.Str(r[1]),
		uidValidity: uint32(store.Int(r[2])), uid: imap.UID(store.Int(r[3]))})
}

func (s *Service) touch(ctx context.Context, id int64) error {
	_, err := s.db.Execute(ctx, "UPDATE attachments SET last_used = ? WHERE id = ?", time.Now().Unix(), id)
	return err
}

// download fetches the raw (still transfer-encoded) part from the server, or the
// whole message when a.part is empty.
func (s *Service) download(ctx context.Context, a attRow) ([]byte, error) {
	c, release, abort, err := s.dialAccount(ctx, a.accountID, nil)
	if err != nil {
		if errors.Is(err, conn.ErrAuthFailed) {
			s.bus.Publish(events.Event{Type: events.SyncFailed, AccountID: a.accountID, Error: err.Error()})
		}
		return nil, err
	}
	defer release()
	data, err := fetchPart(c, abort, a.folder, a.uidValidity, a.uid, a.part, a.encoding)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return data, err
}

// fetchPart streams the part and undoes its transfer encoding on the fly. Once
// more than attachLimit decoded bytes arrive it cuts the connection with abort
// and returns ErrTooLarge.
func fetchPart(c *imapclient.Client, abort func(), folder string, uidValidity uint32, uid imap.UID, part, encoding string) ([]byte, error) {
	sel, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, err
	}
	if sel.UIDValidity != uidValidity {
		return nil, errors.New("server folder changed since sync; sync again")
	}
	var path []int // none: the whole message
	if part != "" {
		for p := range strings.SplitSeq(part, ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil, errors.New("bad attachment part " + part)
			}
			path = append(path, n)
		}
	}
	sec := &imap.FetchItemBodySection{Part: path, Peek: true}
	cmd := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}})
	var data []byte
	found := false
	for m := cmd.Next(); m != nil; m = cmd.Next() {
		for item := m.Next(); item != nil; item = m.Next() {
			bs, ok := item.(imapclient.FetchItemDataBodySection)
			if !ok {
				continue
			}
			data, err = readSection(bs.Literal, encoding, abort)
			if err != nil {
				cmd.Close() // after abort this unblocks the client's reader: the dead connection ends the literal
				return nil, err
			}
			found = true
		}
	}
	if err := cmd.Close(); err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("message no longer on the server")
	}
	return data, nil
}

// evict deletes the least recently used cached files until the cache fits max.
// keep is never evicted. cache_path is cleared before the file is removed, so a
// file that cannot be removed is merely unreferenced (the orphan sweep collects it)
// and its error is returned.
func (s *Service) evict(ctx context.Context, max int64, keep int64) error {
	rows, err := s.db.Query(ctx, "SELECT id, cache_path FROM attachments WHERE cache_path IS NOT NULL ORDER BY last_used, id")
	if err != nil {
		return err
	}
	var rmErr error
	sizes := make([]int64, len(rows.Values))
	var total int64
	for i, r := range rows.Values {
		if st, err := os.Stat(store.Str(r[1])); err == nil {
			sizes[i] = st.Size()
			total += sizes[i]
		}
	}
	for i, r := range rows.Values {
		if total <= max {
			break
		}
		id := store.Int(r[0])
		if id == keep {
			continue
		}
		if _, err := s.db.Execute(ctx, "UPDATE attachments SET cache_path = NULL WHERE id = ?", id); err != nil {
			return err
		}
		total -= sizes[i]
		if err := os.Remove(store.Str(r[1])); err != nil && !os.IsNotExist(err) {
			rmErr = errors.Join(rmErr, err) // reported; the file stays for the orphan sweep
		}
	}
	return rmErr
}

// readSection reads a literal (nil is allowed by IMAP) through its transfer
// decoding. Past attachLimit decoded bytes it calls abort, which must cut the
// network connection, and returns ErrTooLarge.
func readSection(lit io.Reader, encoding string, abort func()) ([]byte, error) {
	if lit == nil {
		return nil, errors.New("attachment part not available on the server")
	}
	r := lit
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		r = base64.NewDecoder(base64.RawStdEncoding, &b64Filter{r}) // as tolerant as mail.DecodePart
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	}
	data, err := io.ReadAll(io.LimitReader(r, attachLimit+1))
	if int64(len(data)) > attachLimit {
		abort() // Client.Close would wait for the rest of the literal
		return nil, ErrTooLarge
	}
	return data, err
}

// b64Filter drops whitespace and '=' padding so the unpadded decoder accepts what
// mail.DecodePart accepts.
type b64Filter struct{ r io.Reader }

func (f *b64Filter) Read(p []byte) (int, error) {
	for {
		n, err := f.r.Read(p)
		k := 0
		for _, c := range p[:n] {
			if c != '=' && c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				p[k] = c
				k++
			}
		}
		if k > 0 || err != nil {
			return k, err
		}
	}
}
