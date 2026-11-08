package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
)

// ImportSummary is what an ImportEML did. Canceled: its ctx ended first, and
// the mail imported until then stays.
type ImportSummary struct {
	Imported, Duplicates int
	Failed               []ImportFailure
	Canceled             bool
}

// ImportFailure is a file ImportEML did not import, and why.
type ImportFailure struct {
	Path, Reason string
}

// importBatch is how many files an import handles between its progress and
// messages.changed events.
const importBatch = 50

var errNotEmail = errors.New("not an email")

// ImportEML imports .eml files into folderID of the local archive accountID
// (ADR 0036): each of paths is a file, or a folder searched recursively for
// .eml files. Each file is copied to the data directory as the message's
// original, then stored like synced mail, but as old mail: no receive event,
// rule, plugin hook or AI classification. A file whose content the account
// already has is a duplicate; a damaged, non-email or too large one
// (MaxMessageBytes) fails alone. Each message commits on its own, so a
// failure or a cancelled ctx keeps what was imported. It reports progress as
// import.progress events and connects to no host.
func (s *Service) ImportEML(ctx context.Context, accountID, folderID int64, paths []string) (ImportSummary, error) {
	var sum ImportSummary
	ctx, end, err := s.begin(ctx, accountID)
	if err != nil {
		return sum, err
	}
	defer end()
	if archive, err := s.localArchive(ctx, accountID); err != nil {
		return sum, err
	} else if !archive {
		return sum, errors.New("only a local archive imports .eml files")
	}
	rows, err := s.db.Query(ctx, "SELECT 1 FROM folders WHERE id = ? AND account_id = ?", folderID, accountID)
	if err != nil {
		return sum, err
	}
	if len(rows.Values) == 0 {
		return sum, fmt.Errorf("folder %d is not in account %d", folderID, accountID)
	}

	var files []string
	for _, p := range paths {
		err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				sum.Failed = append(sum.Failed, ImportFailure{path, err.Error()})
			case path == p && !d.IsDir():
				files = append(files, path) // a file chosen by name, whatever its extension
			case d.Type().IsRegular() && strings.EqualFold(filepath.Ext(path), ".eml"):
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return sum, err
		}
	}

	var ids, tids []int64
	flush := func(done int) {
		s.bus.Publish(events.Event{Type: events.ImportProgress, AccountID: accountID, Done: done, Total: len(files)})
		if len(ids) > 0 {
			s.bus.Publish(events.Event{Type: events.MessagesChanged, AccountID: accountID, MessageIDs: ids,
				ThreadIDs: slices.Compact(slices.Sorted(slices.Values(tids)))})
		}
		ids, tids = nil, nil
	}
	flush(0)
	for i, path := range files {
		if ctx.Err() != nil {
			sum.Canceled = true
			flush(i)
			return sum, nil
		}
		l, dup, err := s.importFile(ctx, accountID, folderID, path)
		switch {
		case err != nil && ctx.Err() != nil:
			sum.Canceled = true
			flush(i)
			return sum, nil
		case err != nil:
			sum.Failed = append(sum.Failed, ImportFailure{path, err.Error()})
		case dup:
			sum.Duplicates++
		default:
			sum.Imported++
			ids = append(append(ids, l.ID), l.Moved...)
			tids = append(append(tids, l.ThreadID), l.Merged...)
		}
		if (i+1)%importBatch == 0 {
			flush(i + 1)
		}
	}
	flush(len(files))
	return sum, nil
}

// importFile imports the file at path, or reports it a duplicate.
func (s *Service) importFile(ctx context.Context, accountID, folderID int64, path string) (mailsync.Local, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return mailsync.Local{}, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return mailsync.Local{}, false, err
	}
	if info.Size() > attachLimit {
		return mailsync.Local{}, false, ErrTooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(f, attachLimit+1))
	if err != nil {
		return mailsync.Local{}, false, err
	}
	if int64(len(raw)) > attachLimit { // grew since the stat
		return mailsync.Local{}, false, ErrTooLarge
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])

	unlock, err := s.lockAccount(ctx, accountID) // threads planned and the hash checked stay so until the insert
	if err != nil {
		return mailsync.Local{}, false, err
	}
	defer unlock()
	rows, err := s.db.Query(ctx, "SELECT 1 FROM messages WHERE account_id = ? AND content_hash = ?", accountID, hash)
	if err != nil {
		return mailsync.Local{}, false, err
	}
	if len(rows.Values) > 0 {
		return mailsync.Local{}, true, nil
	}
	p, err := mail.Parse(raw)
	if err != nil {
		return mailsync.Local{}, false, fmt.Errorf("%w: %v", errNotEmail, err)
	}
	if p.From == "" && p.MessageID == "" && p.Subject == "" && p.Date.IsZero() {
		return mailsync.Local{}, false, errNotEmail
	}
	if p.Date.IsZero() {
		p.Date = info.ModTime()
	}
	// A mislabeled charset must not fail the insert in SQLite, as in sync.
	p.Text, p.HTML = string(mail.ValidText([]byte(p.Text), "", false)), string(mail.ValidText([]byte(p.HTML), "", true))
	if p.Text == "" && p.HTML != "" {
		p.Text = mail.HTMLToText(p.HTML)
	}
	dst := s.archivePath(accountID, hash)
	if err := store.WriteFileAtomic(dst, raw); err != nil {
		return mailsync.Local{}, false, err
	}
	l, err := mailsync.InsertLocal(ctx, s.db, accountID, folderID, p, int64(len(raw)), hash)
	if err != nil {
		os.Remove(dst)
		return mailsync.Local{}, false, err
	}
	return l, false, nil
}

// archivePath is the original of a local archive's message with content_hash hash.
func (s *Service) archivePath(accountID int64, hash string) string {
	return filepath.Join(s.dataDir, "archive", strconv.FormatInt(accountID, 10), hash+".eml")
}

// deleteLocal deletes a local archive's messages ts for good, with their
// originals and the files extracted from them (ADR 0036): rows first, so a
// file that cannot be removed is merely unreferenced.
func (s *Service) deleteLocal(ctx context.Context, accountID int64, ts []target) error {
	if len(ts) == 0 {
		return nil
	}
	ids := make([]int64, len(ts))
	for i, m := range ts {
		ids[i] = m.id
	}
	hashes, tids, err := mailsync.DeleteLocal(ctx, s.db, accountID, ids)
	if err != nil {
		return err
	}
	s.bus.Publish(events.Event{Type: events.MessagesChanged, AccountID: accountID, MessageIDs: ids,
		ThreadIDs: slices.Compact(slices.Sorted(slices.Values(tids)))})
	for _, h := range hashes {
		os.Remove(s.archivePath(accountID, h))
		os.RemoveAll(filepath.Join(filepath.Dir(s.archivePath(accountID, h)), "parts", h))
	}
	return nil
}
