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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

const (
	MaxCacheBytes   = 2 << 30  // attachment cache budget; oldest files are evicted beyond it
	MaxMessageBytes = 25 << 20 // largest attachment or message accepted
)

// attachLimit is MaxMessageBytes; a variable so tests can lower it.
var attachLimit int64 = MaxMessageBytes

// cacheLimit is MaxCacheBytes; a variable so tests can lower it.
var cacheLimit int64 = MaxCacheBytes

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
	at                      backend.Location
	onServer                bool
	contentHash             string // set for a local archive's message: read from its original
}

func (s *Service) loadAttachment(ctx context.Context, id int64) (attRow, error) {
	rows, err := s.db.Query(ctx, `SELECT a.part, a.encoding, a.charset, a.filename, a.content_type, a.size, a.inline, a.cache_path,
		m.account_id, f.server_ref, l.server_ref, m.content_hash
		FROM attachments a JOIN messages m ON m.id = a.message_id LEFT JOIN `+serverLocation+`
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
		accountID: store.Int(r[8]), at: location(r[9], r[10]),
		onServer: store.Str(r[9]) != "" && r[10] != nil, contentHash: store.Str(r[11]),
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
	f := s.newAttachmentFetcher(ctx)
	path, info, err := f.get(attachmentID)
	if cerr := f.close(); err == nil {
		err = cerr
	}
	return path, info, err
}

// FetchedAttachment is one attachment cached on disk by FetchAttachmentsStream.
type FetchedAttachment struct {
	Path string
	Info AttachmentInfo
	Err  error `json:"-"`
}

// StreamError returns the mid-stream failure, if any.
func (a FetchedAttachment) StreamError() error { return a.Err }

func sendStream[T any](ctx context.Context, ch chan<- T, v T) bool {
	select {
	case ch <- v:
		return true
	case <-ctx.Done():
		return false
	}
}

func sendStreamFinal[T any](ctx context.Context, ch chan<- T, v T) {
	select {
	case ch <- v:
	default:
		select {
		case ch <- v:
		case <-ctx.Done():
		}
	}
}

// FetchAttachments gets each attachment's cached file over one connection,
// downloading first if needed, and calls fn for each in order.
func (s *Service) FetchAttachments(ctx context.Context, attachmentIDs []int64, fn func(path string, info AttachmentInfo) error) (err error) {
	f := s.newAttachmentFetcher(ctx)
	defer func() {
		if cerr := f.close(); err == nil {
			err = cerr
		}
	}()
	for _, id := range attachmentIDs {
		path, info, gerr := f.get(id)
		if gerr != nil {
			return gerr
		}
		if ferr := fn(path, info); ferr != nil {
			return ferr
		}
	}
	return nil
}

// FetchAttachmentsStream streams each attachment's cached file path and info in
// order over one connection, downloading first if needed.
func (s *Service) FetchAttachmentsStream(ctx context.Context, attachmentIDs []int64) (<-chan FetchedAttachment, error) {
	fctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	ids := slices.Clone(attachmentIDs)
	ch := make(chan FetchedAttachment, 16)
	f := &attachmentFetcher{s: s, ctx: fctx, end: end}
	go func() {
		defer close(ch)
		for _, id := range ids {
			path, info, gerr := f.get(id)
			if gerr != nil {
				// Before close: it ends fctx, and a full buffer would then drop the error.
				sendStreamFinal(fctx, ch, FetchedAttachment{Err: gerr})
				_ = f.close()
				return
			}
			if !sendStream(fctx, ch, FetchedAttachment{Path: path, Info: info}) {
				_ = f.close()
				return
			}
		}
		if cerr := f.close(); cerr != nil {
			sendStreamFinal(ctx, ch, FetchedAttachment{Err: cerr}) // fctx has ended with close
		}
	}()
	return ch, nil
}

// attachmentFetcher gets attachments one after another over one connection: a
// login per file made a message with a few signature logos take seconds to open.
// It holds the account lock only inside get, so whoever waits for the lock still
// goes between two files. Between files its connection is kept with the account,
// where the lock's next holder closes it: that holder may dial, and a server may
// limit an account's connections. close must be called.
type attachmentFetcher struct {
	s         *Service
	ctx       context.Context
	end       func()  // ends the call begun by the first get
	saved     []int64 // downloaded here; close evicts around them
	accountID int64   // of the last get

	conn *downloadConn // while inside get; nil when there is none yet

	dialFails int // in a row
	dialErr   error
}

// downloadConn is a signed-in receiving session.
type downloadConn struct {
	owner   *attachmentFetcher
	recv    backend.Receiver
	release func()
}

func (s *Service) newAttachmentFetcher(ctx context.Context) *attachmentFetcher {
	return &attachmentFetcher{s: s, ctx: ctx}
}

func (f *attachmentFetcher) get(id int64) (string, AttachmentInfo, error) {
	s := f.s
	if f.end == nil {
		ctx, end, err := s.begin(f.ctx, 0)
		if err != nil {
			return "", AttachmentInfo{}, err
		}
		f.ctx, f.end = ctx, end
	}
	a, err := s.loadAttachment(f.ctx, id)
	if err != nil {
		return "", AttachmentInfo{}, err
	}
	if err := s.bind(f.ctx, a.accountID); err != nil {
		return "", AttachmentInfo{}, err
	}
	if a.contentHash != "" {
		// Under the lock deleteLocal holds, and re-read under it: a message
		// deleted for good meanwhile gets no part written back (ADR 0036).
		unlock, err := s.lockAccount(f.ctx, a.accountID)
		if err != nil {
			return "", AttachmentInfo{}, err
		}
		defer unlock()
		if a, err = s.loadAttachment(f.ctx, id); err != nil {
			return "", AttachmentInfo{}, err
		}
		p, err := s.archivePart(a)
		return p, a.info, err
	}
	if p, ok, err := s.cached(f.ctx, a); ok || err != nil {
		return p, a.info, err
	}
	unlock, err := s.acquireAccount(f.ctx, a.accountID)
	if err != nil {
		return "", AttachmentInfo{}, err
	}
	f.accountID = a.accountID
	if k := s.takeKept(a.accountID, nil); k != nil && k.owner == f {
		f.conn = k
	} else if k != nil {
		k.release()
	}
	defer func() {
		s.keepConn(a.accountID, f.conn)
		f.conn = nil
		unlock()
	}()
	// The row may have changed (cached, moved, deleted) while we waited for the lock.
	if a, err = s.loadAttachment(f.ctx, id); err != nil {
		return "", AttachmentInfo{}, err
	}
	if p, ok, err := s.cached(f.ctx, a); ok || err != nil {
		return p, a.info, err
	}
	info := a.info
	if int64(info.Size) > attachLimit {
		return "", info, ErrTooLarge
	}
	if !a.onServer {
		return "", info, ErrNotOnServerYet
	}
	data, err := f.fetch(a)
	var path string
	if err == nil {
		path, err = s.saveAttachment(f.ctx, a, data)
	}
	if errors.Is(err, ErrTooLarge) {
		// The row understated it: the next open need not download up to the limit again.
		s.db.Execute(f.ctx, "UPDATE attachments SET size = ? WHERE id = ?", attachLimit+1, id)
		return "", info, err
	}
	if err != nil {
		return "", info, err
	}
	f.saved = append(f.saved, id)
	return path, info, nil
}

// fetch downloads a's part. A failure hangs up, as it may have left the
// connection unusable; on a kept connection, which may just have gone stale, a
// gets one more try on a new one.
func (f *attachmentFetcher) fetch(a attRow) ([]byte, error) {
	for {
		reused := f.conn != nil
		data, err := f.fetchOnce(a)
		if err == nil {
			return data, nil
		}
		if f.ctx.Err() != nil {
			return nil, f.ctx.Err()
		}
		f.hangUp()
		if !reused || errors.Is(err, ErrTooLarge) {
			return nil, err
		}
	}
}

func (f *attachmentFetcher) fetchOnce(a attRow) ([]byte, error) {
	if f.conn == nil {
		// A refused login or a server that stays away is not worth a dial per file.
		if f.dialFails == 2 || errors.Is(f.dialErr, backend.ErrAuthFailed) {
			return nil, f.dialErr
		}
		recv, release, err := f.s.dialDownload(f.ctx, a.accountID)
		if err != nil {
			f.dialFails, f.dialErr = f.dialFails+1, err
			return nil, err
		}
		f.conn, f.dialFails = &downloadConn{owner: f, recv: recv, release: release}, 0
	}
	return fetchPart(f.ctx, f.conn.recv, a)
}

func (f *attachmentFetcher) hangUp() {
	if f.conn != nil {
		f.conn.release()
		f.conn = nil
	}
}

// close hangs up and brings the cache back within budget, once for all the files
// saved: eviction scans the whole cache. A cancelled call's files count too.
func (f *attachmentFetcher) close() error {
	if k := f.s.takeKept(f.accountID, f); k != nil {
		k.release()
	}
	if f.end == nil {
		return nil
	}
	defer f.end()
	if len(f.saved) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(f.ctx), 5*time.Second)
	defer cancel()
	return f.s.evict(ctx, cacheLimit, f.saved...)
}

// saveAttachment writes a's downloaded data to the cache and records it. The
// caller holds the account lock.
func (s *Service) saveAttachment(ctx context.Context, a attRow, data []byte) (string, error) {
	// Transfer encoding was undone while streaming. Only text has a charset:
	// a PDF labelled charset=binary or latin-1 keeps its bytes.
	if strings.HasPrefix(strings.ToLower(a.info.ContentType), "text/") {
		if d, err := mail.DecodePart(data, "", a.charset); err == nil {
			data = d
		}
	}
	if int64(len(data)) > attachLimit {
		return "", ErrTooLarge
	}
	path, err := filepath.Abs(filepath.Join(s.cacheDir(a.accountID), strconv.FormatInt(a.info.ID, 10)))
	if err != nil {
		return "", err
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if err := store.WriteFileAtomic(path, data); err != nil {
		return "", err
	}
	if _, err := s.db.Execute(ctx, "UPDATE attachments SET cache_path = ?, last_used = ? WHERE id = ?", path, time.Now().Unix(), a.info.ID); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// archivePart is a local archive's attachment, extracted from its original
// into a file beside it (ADR 0036): no cache row, so eviction and clearing the
// cache leave it, and it is extracted again if gone.
func (s *Service) archivePart(a attRow) (string, error) {
	if int64(a.info.Size) > attachLimit {
		return "", ErrTooLarge
	}
	path, err := filepath.Abs(filepath.Join(s.archivePartsDir(a.accountID, a.contentHash), a.part))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	raw, err := os.ReadFile(s.archivePath(a.accountID, a.contentHash))
	if err != nil {
		return "", err
	}
	p, err := mail.Parse(raw)
	if err != nil {
		return "", err
	}
	for _, att := range p.Attachments {
		if att.Part == a.part { // Parse already undid transfer encoding and charset
			return path, store.WriteFileAtomic(path, att.Data)
		}
	}
	return "", fmt.Errorf("attachment part %s not in the original", a.part)
}

// rawOutDir holds originals RawMessageFile hands to the interface; emptied at start.
func (s *Service) rawOutDir() string { return filepath.Join(s.dataDir, "raw-out") }

// RawMessageFile is GetRawMessage written to a new file (0600) in the data
// directory, its path returned, for the interface: an original of up to
// MaxMessageBytes would not fit one Local API response as base64. The
// caller removes the file once read; what is left is removed at start.
func (s *Service) RawMessageFile(ctx context.Context, messageID int64) (string, error) {
	raw, err := s.GetRawMessage(ctx, messageID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.rawOutDir(), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(s.rawOutDir(), "*.eml")
	if err != nil {
		return "", err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Close()
	} else {
		f.Close()
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// GetRawMessage downloads the message's full original from the server, for a
// Truncated message or for saving. Messages over MaxMessageBytes give ErrTooLarge.
// A local archive's message is read from its original file, offline.
func (s *Service) GetRawMessage(ctx context.Context, messageID int64) ([]byte, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	messageID, err = s.resolveID(ctx, messageID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, "SELECT account_id, content_hash FROM messages WHERE id = ?", messageID)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("message %d not found", messageID)
	}
	if err := s.bind(ctx, store.Int(rows.Values[0][0])); err != nil {
		return nil, err
	}
	if hash := store.Str(rows.Values[0][1]); hash != "" { // a local archive's: its original file
		return os.ReadFile(s.archivePath(store.Int(rows.Values[0][0]), hash))
	}
	unlock, err := s.lockAccount(ctx, store.Int(rows.Values[0][0]))
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Read under the lock: a sync may have moved or deleted it meanwhile.
	rows, err = s.db.Query(ctx, `SELECT m.account_id, f.server_ref, l.server_ref, m.size
		FROM messages m LEFT JOIN `+serverLocation+` WHERE m.id = ?`, messageID)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("message %d not found", messageID)
	}
	r := rows.Values[0]
	if store.Int(r[3]) > attachLimit {
		return nil, ErrTooLarge
	}
	if store.Str(r[1]) == "" || r[2] == nil {
		return nil, ErrNotOnServerYet
	}
	return s.download(ctx, attRow{accountID: store.Int(r[0]), at: location(r[1], r[2])})
}

// location is the server location in a folder's and a message location's
// server_ref columns.
func location(folder, ref any) backend.Location {
	return backend.Location{Folder: backend.FolderRef(store.Str(folder)), Ref: backend.MessageRef(store.Str(ref))}
}

func (s *Service) touch(ctx context.Context, id int64) error {
	_, err := s.db.Execute(ctx, "UPDATE attachments SET last_used = ? WHERE id = ?", time.Now().Unix(), id)
	return err
}

// download fetches the raw (still transfer-encoded) part from the server, or the
// whole message when a.part is empty.
func (s *Service) download(ctx context.Context, a attRow) ([]byte, error) {
	recv, release, err := s.dialDownload(ctx, a.accountID)
	if err != nil {
		return nil, err
	}
	defer release()
	data, err := fetchPart(ctx, recv, a)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return data, err
}

// dialDownload is dialAccount for a download; a refused login is shown like a sync's.
func (s *Service) dialDownload(ctx context.Context, accountID int64) (recv backend.Receiver, release func(), err error) {
	recv, release, err = s.dialAccount(ctx, accountID)
	if errors.Is(err, backend.ErrAuthFailed) {
		s.bus.Publish(events.Event{Type: events.SyncFailed, AccountID: accountID, Error: err.Error(), Kind: string(conn.FailureAuth)})
	}
	return recv, release, err
}

// fetchPart streams a's part, or the whole message when a.part is empty, and
// undoes its transfer encoding on the fly. Once more than attachLimit decoded
// bytes arrive it stops, which cuts the download, and returns ErrTooLarge.
func fetchPart(ctx context.Context, recv backend.Receiver, a attRow) ([]byte, error) {
	if a.part != "" { // parts are stored as the part paths mail.Parse gives them
		for p := range strings.SplitSeq(a.part, ".") {
			if _, err := strconv.Atoi(p); err != nil {
				return nil, errors.New("bad attachment part " + a.part)
			}
		}
	}
	rc, err := recv.Fetch(ctx, backend.ContentRequest{Location: a.at, Part: backend.PartRef(a.part)})
	switch {
	case errors.Is(err, backend.ErrFolderReset):
		return nil, errors.New("server folder changed since sync; sync again")
	case errors.Is(err, backend.ErrNotFound):
		return nil, errors.New("message no longer on the server")
	case err != nil:
		return nil, err
	}
	defer rc.Close()
	return readSection(rc, a.encoding, func() { rc.Close() })
}

// evict deletes the least recently used cached files until the cache fits max.
// keep are never evicted. cache_path is cleared before the file is removed, so a
// file that cannot be removed is merely unreferenced (the orphan sweep collects it)
// and its error is returned. It holds cacheMu throughout: with a save between
// reading the rows and acting on them, it would remove a file just downloaded.
func (s *Service) evict(ctx context.Context, max int64, keep ...int64) error {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
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
		if slices.Contains(keep, id) {
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
		// Drop whitespace and '=' padding so the unpadded decoder accepts what
		// mail.DecodePart accepts.
		r = base64.NewDecoder(base64.RawStdEncoding, &byteFilter{r, func(c byte) bool {
			return c == '=' || c == ' ' || c == '\t' || c == '\r' || c == '\n'
		}})
	case "quoted-printable":
		// Drop raw NULs as mail.DecodePart does; the decoder fails on them.
		r = quotedprintable.NewReader(&byteFilter{r, func(c byte) bool { return c == 0 }})
	}
	data, err := io.ReadAll(io.LimitReader(r, attachLimit+1))
	if int64(len(data)) > attachLimit {
		abort() // stop the download rather than read the rest
		return nil, ErrTooLarge
	}
	return data, err
}

// byteFilter drops the bytes drop reports.
type byteFilter struct {
	r    io.Reader
	drop func(byte) bool
}

func (f *byteFilter) Read(p []byte) (int, error) {
	for {
		n, err := f.r.Read(p)
		k := 0
		for _, c := range p[:n] {
			if !f.drop(c) {
				p[k] = c
				k++
			}
		}
		if k > 0 || err != nil {
			return k, err
		}
	}
}
