package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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
