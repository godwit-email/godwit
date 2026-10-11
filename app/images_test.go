package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// testLoader is an imageLoader whose posted callbacks run in wait.
func testLoader(t *testing.T, allowLoopback bool) (*imageLoader, func(desc string, cond func() bool)) {
	t.Helper()
	posted := make(chan func(), 1024)
	l := newImageLoader(context.Background(), func(f func()) { posted <- f }, allowLoopback)
	t.Cleanup(l.cancel)
	wait := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for !cond() {
			select {
			case f := <-posted:
				f()
			case <-deadline:
				t.Fatalf("timed out waiting for %s", desc)
			}
		}
	}
	return l, wait
}

func (l *imageLoader) settled(src string) bool { e := l.imgs[src]; return e != nil && e.done }

// pngHeader is a PNG's signature and a valid IHDR chunk declaring w×h.
func pngHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	data := binary.BigEndian.AppendUint32(nil, w)
	data = binary.BigEndian.AppendUint32(data, h)
	data = append(data, 8, 6, 0, 0, 0)
	chunk := append([]byte("IHDR"), data...)
	b.Write(binary.BigEndian.AppendUint32(nil, uint32(len(data))))
	b.Write(chunk)
	b.Write(binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(chunk)))
	return b.Bytes()
}

func TestImageBudgets(t *testing.T) {
	pic := pngBytes(t, 4, 4)
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(pic) })
	mux.HandleFunc("/huge.png", func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(pngHeader(100000, 100000)) })
	mux.HandleFunc("/big.png", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(append(pngHeader(10, 10), make([]byte, 6<<20)...))
	})
	mux.HandleFunc("/ftp.png", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "ftp://files.example.com/x.png", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("at most 50 remote images", func(t *testing.T) {
		hits.Store(0)
		l, wait := testLoader(t, true)
		var srcs []string
		for i := range 51 {
			src := fmt.Sprintf("%s/img/%d.png", srv.URL, i)
			srcs = append(srcs, src)
			l.Load(src)
		}
		wait("all settled", func() bool {
			for _, s := range srcs {
				if !l.settled(s) {
					return false
				}
			}
			return true
		})
		if n := hits.Load(); n != 50 {
			t.Fatalf("%d requests, want 50", n)
		}
		if l.Load(srcs[0]) == nil || l.Load(srcs[50]) != nil || !errors.Is(l.imgs[srcs[50]].err, errTooManyImages) {
			t.Fatalf("first %v, 51st %v", l.imgs[srcs[0]], l.imgs[srcs[50]])
		}
	})
	t.Run("pixels checked before decoding", func(t *testing.T) {
		l, wait := testLoader(t, true)
		src := srv.URL + "/huge.png"
		l.Load(src)
		wait("settled", func() bool { return l.settled(src) })
		if bm := l.Load(src); bm != nil || !errors.Is(l.imgs[src].err, errImagePixels) {
			t.Fatalf("got %v, %v", bm, l.imgs[src].err)
		}
	})
	t.Run("5 MB at most", func(t *testing.T) {
		l, wait := testLoader(t, true)
		src := srv.URL + "/big.png"
		l.Load(src)
		wait("settled", func() bool { return l.settled(src) })
		if !errors.Is(l.imgs[src].err, errImageTooLarge) {
			t.Fatalf("err %v", l.imgs[src].err)
		}
	})
	t.Run("redirect to ftp refused", func(t *testing.T) {
		l, wait := testLoader(t, true)
		src := srv.URL + "/ftp.png"
		l.Load(src)
		wait("settled", func() bool { return l.settled(src) })
		if !errors.Is(l.imgs[src].err, errImageScheme) {
			t.Fatalf("err %v", l.imgs[src].err)
		}
	})
	t.Run("loopback refused", func(t *testing.T) {
		hits.Store(0)
		l, wait := testLoader(t, false)
		src := srv.URL + "/img/a.png"
		l.Load(src)
		wait("settled", func() bool { return l.settled(src) })
		if !errors.Is(l.imgs[src].err, errImageHost) || hits.Load() != 0 {
			t.Fatalf("err %v, %d requests", l.imgs[src].err, hits.Load())
		}
	})
	t.Run("data images by content", func(t *testing.T) {
		l, wait := testLoader(t, false)
		good := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pic)
		bmp := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("BM\x3a\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00\x28\x00\x00\x00\x01\x00\x00\x00\x01\x00\x00\x00\x01\x00\x18\x00\x00\x00\x00\x00\x04\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"))
		l.Load(good)
		l.Load(bmp)
		wait("settled", func() bool { return l.settled(good) && l.settled(bmp) })
		if l.Load(good) == nil || !errors.Is(l.imgs[bmp].err, errImageFormat) {
			t.Fatalf("png %v, bmp %v", l.imgs[good], l.imgs[bmp])
		}
	})
	t.Run("cancel drops results", func(t *testing.T) {
		l, wait := testLoader(t, true)
		src := srv.URL + "/img/c.png"
		l.Load(src)
		l.cancel()
		wait("settled", func() bool { return l.settled(src) })
		if l.Load(src) != nil {
			t.Fatal("an image loaded after cancel")
		}
	})
}

// pipeListener serves HTTP over net.Pipe: a write returns only once the
// other end has read it, so what the server sent is what the client read
// (plus the client's 4 KiB read buffer).
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   *sync.Once
}

func (p pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-p.conns:
		return c, nil
	case <-p.closed:
		return nil, net.ErrClosed
	}
}
func (p pipeListener) Close() error   { p.once.Do(func() { close(p.closed) }); return nil }
func (p pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

// TestImageTotalBudget: the 20 MB budget is reserved as bytes are read;
// once it is gone no further request goes out and reads in flight stop.
func TestImageTotalBudget(t *testing.T) {
	const n, size, chunk = 20, 3 << 20, 32 << 10 // 60 MB offered, written 32 KiB at a time
	var hits, sent atomic.Int64
	buf := make([]byte, chunk)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(pngHeader(10, 10))
		for left := size; left > 0; left -= chunk {
			k, err := w.Write(buf[:min(chunk, left)])
			sent.Add(int64(k))
			if err != nil {
				return
			}
		}
	})}
	ln := pipeListener{make(chan net.Conn), make(chan struct{}), &sync.Once{}}
	go srv.Serve(ln)
	defer srv.Close()
	l, wait := testLoader(t, true)
	l.client.Transport.(*http.Transport).DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		select {
		case ln.conns <- server:
			return client, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var srcs []string
	for i := range n {
		srcs = append(srcs, fmt.Sprintf("http://images.example/%d.png", i))
		l.Load(srcs[i])
	}
	wait("all settled", func() bool {
		for _, s := range srcs {
			if !l.settled(s) {
				return false
			}
		}
		return true
	})
	if left := l.budget.BytesLeft(); left < 0 {
		t.Errorf("the budget overran by %d bytes", -left)
	}
	// Each of the imageWorkers requests may have one chunk in the server's
	// write and the client's read buffer when the budget runs out.
	if s, most := sent.Load(), int64(maxImagesBytes+imageWorkers*chunk); s > most {
		t.Errorf("the server sent %d bytes, at most %d expected; read %d, %d requests", s, most, maxImagesBytes-l.budget.BytesLeft(), hits.Load())
	}
	if h := hits.Load(); h >= n {
		t.Errorf("%d of %d requests sent after the budget ran out", h, n)
	}
	if !slices.ContainsFunc(srcs, func(s string) bool { return errors.Is(l.imgs[s].err, errImagesTooLarge) }) {
		t.Error("no image refused for the total budget")
	}
}

func dataPNG(b []byte) string { return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b) }

// TestInlineImageBudgets: data: images share the remote images' count,
// workers and a total pixel budget, and a cancelled view decodes no more.
func TestInlineImageBudgets(t *testing.T) {
	// decoding counts how many decodes run at once and waits for gate.
	decoding := func(l *imageLoader, gate chan struct{}) (calls, most *atomic.Int64) {
		calls, most = new(atomic.Int64), new(atomic.Int64)
		var now atomic.Int64
		l.decode = func([]byte) (*ui.Bitmap, error) {
			calls.Add(1)
			n := now.Add(1)
			defer now.Add(-1)
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
			<-gate
			return &ui.Bitmap{}, nil
		}
		return calls, most
	}
	all := func(l *imageLoader, srcs []string) func() bool {
		return func() bool {
			return !slices.ContainsFunc(srcs, func(s string) bool { return !l.settled(s) })
		}
	}

	t.Run("count and workers", func(t *testing.T) {
		l, wait := testLoader(t, false)
		gate := make(chan struct{})
		calls, most := decoding(l, gate)
		var srcs []string
		for i := range 60 {
			srcs = append(srcs, dataPNG(pngHeader(uint32(i+1), 1)))
			l.Load(srcs[i])
		}
		time.Sleep(20 * time.Millisecond) // let the decodes pile up behind the gate
		close(gate)
		wait("all settled", all(l, srcs))
		if n := calls.Load(); n > maxImages {
			t.Errorf("%d inline images decoded, at most %d", n, maxImages)
		}
		if m := most.Load(); m > imageWorkers {
			t.Errorf("%d decodes at once, at most %d", m, imageWorkers)
		}
		if !errors.Is(l.imgs[srcs[59]].err, errTooManyImages) {
			t.Errorf("the 60th image: %v", l.imgs[srcs[59]].err)
		}
	})
	t.Run("total pixels", func(t *testing.T) {
		l, wait := testLoader(t, false)
		gate := make(chan struct{})
		close(gate)
		calls, _ := decoding(l, gate)
		var srcs []string
		for i := range 10 { // 16 MP each: six fit in 100 MP
			srcs = append(srcs, dataPNG(pngHeader(4000+uint32(i), 4000)))
			l.Load(srcs[i])
		}
		wait("all settled", all(l, srcs))
		refused := 0
		for _, s := range srcs {
			if errors.Is(l.imgs[s].err, errImagesPixels) {
				refused++
			}
		}
		if n := calls.Load(); n != 6 || refused != 4 {
			t.Errorf("%d decoded, %d refused for the total; want 6 and 4", n, refused)
		}
	})
	t.Run("cancel stops queued decodes", func(t *testing.T) {
		l, wait := testLoader(t, false)
		gate := make(chan struct{})
		calls, _ := decoding(l, gate)
		var srcs []string
		for i := range 20 {
			srcs = append(srcs, dataPNG(pngHeader(uint32(i+1), 1)))
			l.Load(srcs[i])
		}
		for calls.Load() < imageWorkers { // decodes post nothing until the gate opens
			time.Sleep(time.Millisecond)
		}
		l.cancel()
		close(gate)
		wait("all settled", all(l, srcs))
		if n := calls.Load(); n != imageWorkers {
			t.Errorf("%d decodes after cancel, want only the %d running", n, imageWorkers)
		}
	})
}
