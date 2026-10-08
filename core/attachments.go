package core

import (
	"context"
	"errors"
	"io"
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
	mu := s.accountLock(a.accountID)
	mu.Lock()
	defer mu.Unlock()
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
	if data, err = mail.DecodePart(data, a.encoding, a.charset); err != nil {
		return "", info, err
	}
	if int64(len(data)) > attachLimit {
		return "", info, ErrTooLarge
	}
	dir := filepath.Join(s.dataDir, "cache", strconv.FormatInt(a.accountID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", info, err
	}
	path, err := filepath.Abs(filepath.Join(dir, strconv.FormatInt(attachmentID, 10)))
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

func (s *Service) touch(ctx context.Context, id int64) error {
	_, err := s.db.Execute(ctx, "UPDATE attachments SET last_used = ? WHERE id = ?", time.Now().Unix(), id)
	return err
}

// download fetches the raw (still transfer-encoded) part from the server.
func (s *Service) download(ctx context.Context, a attRow) ([]byte, error) {
	c, release, err := s.dialAccount(ctx, a.accountID)
	if err != nil {
		if errors.Is(err, conn.ErrAuthFailed) {
			s.bus.Publish(events.Event{Type: events.SyncFailed, AccountID: a.accountID, Error: err.Error()})
		}
		return nil, err
	}
	defer release()
	data, err := fetchPart(c, a.folder, a.uidValidity, a.uid, a.part)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return data, err
}

// fetchPart streams the part, giving up with ErrTooLarge once it exceeds what a
// transfer-encoded attachment of attachLimit bytes can take (base64 plus line breaks).
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
	cmd := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}})
	var data []byte
	found := false
	for m := cmd.Next(); m != nil; m = cmd.Next() {
		for item := m.Next(); item != nil; item = m.Next() {
			bs, ok := item.(imapclient.FetchItemDataBodySection)
			if !ok {
				continue
			}
			data, err = io.ReadAll(io.LimitReader(bs.Literal, 2*attachLimit+1))
			if err != nil {
				cmd.Close()
				return nil, err
			}
			if int64(len(data)) > 2*attachLimit {
				// ponytail: the rest is read and discarded (memory stays bounded) because the
				// client cannot be closed mid-literal; a huge stream still costs bandwidth.
				io.Copy(io.Discard, bs.Literal)
				cmd.Close()
				return nil, ErrTooLarge
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
// file that cannot be removed is merely unreferenced (the orphan sweep collects it).
func (s *Service) evict(ctx context.Context, max int64, keep int64) error {
	rows, err := s.db.Query(ctx, "SELECT id, cache_path FROM attachments WHERE cache_path IS NOT NULL ORDER BY last_used, id")
	if err != nil {
		return err
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
			return nil
		}
		id := store.Int(r[0])
		if id == keep {
			continue
		}
		if _, err := s.db.Execute(ctx, "UPDATE attachments SET cache_path = NULL WHERE id = ?", id); err != nil {
			return err
		}
		total -= sizes[i]
		os.Remove(store.Str(r[1])) // best effort, see above
	}
	return nil
}
