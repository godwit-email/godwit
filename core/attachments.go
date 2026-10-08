package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwitmail/godwit/core/conn"
	"github.com/godwitmail/godwit/core/mail"
	"github.com/godwitmail/godwit/core/store"
	"github.com/zalando/go-keyring"
)

const (
	MaxCacheBytes   = 2 << 30  // attachment cache budget; oldest files are evicted beyond it
	MaxMessageBytes = 25 << 20 // largest attachment or message accepted
)

var (
	ErrTooLarge       = errors.New("attachment exceeds the size limit")
	ErrNotOnServerYet = errors.New("message has not reached the server yet")
)

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }

// GetAttachment returns the attachment's cached file, downloading it first if needed.
func (s *Service) GetAttachment(ctx context.Context, attachmentID int64) (string, AttachmentInfo, error) {
	rows, err := s.db.Query(ctx, `SELECT a.part, a.encoding, a.charset, a.filename, a.content_type, a.size, a.inline, a.cache_path,
		m.account_id, f.name, m.server_uidvalidity, m.server_uid
		FROM attachments a JOIN messages m ON m.id = a.message_id LEFT JOIN folders f ON f.id = m.server_folder_id
		WHERE a.id = ?`, attachmentID)
	if err != nil {
		return "", AttachmentInfo{}, err
	}
	if len(rows.Values) == 0 {
		return "", AttachmentInfo{}, errors.New("attachment not found")
	}
	r := rows.Values[0]
	info := AttachmentInfo{ID: attachmentID, Filename: store.Str(r[3]), ContentType: store.Str(r[4]), Size: int(store.Int(r[5])), Inline: store.Int(r[6]) != 0}
	if p := store.Str(r[7]); p != "" {
		if _, err := os.Stat(p); err == nil {
			s.touch(ctx, attachmentID)
			return p, info, nil
		}
	}
	if info.Size > MaxMessageBytes {
		return "", info, ErrTooLarge
	}
	accountID, folder := store.Int(r[8]), store.Str(r[9])
	if folder == "" || r[11] == nil {
		return "", info, ErrNotOnServerYet
	}
	mu := s.accountLock(accountID)
	mu.Lock()
	defer mu.Unlock()
	// Another call may have cached it while we waited for the lock.
	if cur, err := s.db.Query(ctx, "SELECT cache_path FROM attachments WHERE id = ?", attachmentID); err == nil && len(cur.Values) > 0 {
		if p := store.Str(cur.Values[0][0]); p != "" {
			if _, err := os.Stat(p); err == nil {
				s.touch(ctx, attachmentID)
				return p, info, nil
			}
		}
	}
	data, err := s.download(ctx, accountID, folder, uint32(store.Int(r[10])), imap.UID(store.Int(r[11])), store.Str(r[0]))
	if err != nil {
		return "", info, err
	}
	if len(data) > MaxMessageBytes {
		return "", info, ErrTooLarge
	}
	if data, err = mail.DecodePart(data, store.Str(r[1]), store.Str(r[2])); err != nil {
		return "", info, err
	}
	if len(data) > MaxMessageBytes {
		return "", info, ErrTooLarge
	}
	dir := filepath.Join(s.dataDir, "cache", strconv.FormatInt(accountID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", info, err
	}
	path := filepath.Join(dir, strconv.FormatInt(attachmentID, 10)) // absolute: dataDir is absolute or stable for the process
	if path, err = filepath.Abs(path); err != nil {
		return "", info, err
	}
	if err := store.WriteFileAtomic(path, data); err != nil {
		return "", info, err
	}
	if _, err := s.db.Execute(ctx, "UPDATE attachments SET cache_path = ?, last_used = ? WHERE id = ?", path, time.Now().Unix(), attachmentID); err != nil {
		os.Remove(path)
		return "", info, err
	}
	s.evict(ctx, MaxCacheBytes, attachmentID)
	return path, info, nil
}

func (s *Service) touch(ctx context.Context, id int64) {
	s.db.Execute(ctx, "UPDATE attachments SET last_used = ? WHERE id = ?", time.Now().Unix(), id)
}

// download fetches the raw (still transfer-encoded) part from the server.
func (s *Service) download(ctx context.Context, accountID int64, folder string, uidValidity uint32, uid imap.UID, part string) ([]byte, error) {
	acc, err := s.account(ctx, accountID)
	if err != nil {
		return nil, err
	}
	pw, err := keyring.Get(keyringService, keyringUser(accountID))
	if err != nil {
		return nil, err
	}
	c, err := conn.DialIMAP(ctx, conn.Endpoint{Host: acc.IMAPHost, Port: acc.IMAPPort, Security: acc.IMAPSecurity,
		Username: acc.Username, Password: pw})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	defer context.AfterFunc(ctx, func() { c.Close() })()
	data, err := fetchPart(c, folder, uidValidity, uid, part)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return data, err
}

func fetchPart(c *imapclient.Client, folder string, uidValidity uint32, uid imap.UID, part string) ([]byte, error) {
	sel, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, err
	}
	if sel.UIDValidity != uidValidity {
		return nil, errors.New("server folder changed since sync; sync again")
	}
	var path []int
	for _, p := range strings.Split(part, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, errors.New("bad attachment part " + part)
		}
		path = append(path, n)
	}
	sec := &imap.FetchItemBodySection{Part: path, Peek: true}
	bufs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}}).Collect()
	if err != nil {
		return nil, err
	}
	if len(bufs) != 1 {
		return nil, errors.New("message no longer on the server")
	}
	return bufs[0].FindBodySection(sec), nil
}

// evict deletes the least recently used cached files until the cache fits max.
// keep is never evicted. Failures are ignored: the cache is only an optimisation.
func (s *Service) evict(ctx context.Context, max int64, keep int64) {
	rows, err := s.db.Query(ctx, "SELECT id, cache_path FROM attachments WHERE cache_path IS NOT NULL ORDER BY last_used, id")
	if err != nil {
		return
	}
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
			return
		}
		if id := store.Int(r[0]); id != keep {
			if err := os.Remove(store.Str(r[1])); err == nil || os.IsNotExist(err) {
				if _, err := s.db.Execute(ctx, "UPDATE attachments SET cache_path = NULL WHERE id = ?", id); err == nil {
					total -= sizes[i]
				}
			}
		}
	}
}
