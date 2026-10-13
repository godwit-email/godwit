package imagefetch

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/fyne-io/image/ico"
)

const MaxAvatarBytes = 256 << 10

// ErrAvatarMiss is a deterministic miss (404 or a rejected image), safe to cache for 24 hours.
var ErrAvatarMiss = errors.New("avatar unavailable")

// NewAvatarClient retains the base transport's public-address dial checks and
// redirect policy. The injected base is trusted, just as for ordinary images.
func NewAvatarClient(base *http.Client) *http.Client {
	if base == nil {
		base = NewClient(false)
	}
	c := *base
	c.Jar = nil
	if c.Timeout == 0 || c.Timeout > Timeout {
		c.Timeout = Timeout
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.User != nil {
			return ErrScheme
		}
		if len(via) > MaxRedirects {
			return errTooManyRedirects
		}
		if base.CheckRedirect != nil {
			if err := base.CheckRedirect(req, via); err != nil {
				return err
			}
		}
		req.Header.Del("Referer")
		req.Header.Del("Cookie")
		return nil
	}
	return &c
}

// DownloadAvatar streams at most 256 KiB and permits only HTTPS, including redirects.
func DownloadAvatar(ctx context.Context, client *http.Client, src string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme != "https" || req.URL.User != nil {
		return nil, ErrScheme
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "image/svg+xml,image/webp,image/png,image/jpeg,image/gif,image/x-icon,image/vnd.microsoft.icon;q=0.9")
	resp, err := NewAvatarClient(client).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { cancel(); resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrAvatarMiss
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("avatar: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxAvatarBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxAvatarBytes {
		return nil, fmt.Errorf("%w: %w", ErrAvatarMiss, ErrTooLarge)
	}
	return data, nil
}

// CheckAvatarImage validates source-specific image bytes; it does not establish
// BIMI message trust. ICO is converted to PNG; SVG remains vector data.
func CheckAvatarImage(data []byte, source string) (out []byte, format string, width, height int, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w: %w", ErrAvatarMiss, err)
		}
	}()
	if source != "bimi" && source != "favicon" && source != "gravatar" {
		return nil, "", 0, 0, ErrFormat
	}
	if len(data) > MaxAvatarBytes {
		return nil, "", 0, 0, ErrTooLarge
	}
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
		if source == "gravatar" {
			return nil, "", 0, 0, ErrFormat
		}
		w, h, e := avatarSVG(data, source == "bimi")
		return data, "svg", w, h, e
	}
	if source == "bimi" {
		return nil, "", 0, 0, ErrFormat
	}
	if bytes.HasPrefix(data, []byte{0, 0, 1, 0}) {
		if source != "favicon" {
			return nil, "", 0, 0, ErrFormat
		}
		data, err = avatarICO(data)
		if err != nil {
			return nil, "", 0, 0, err
		}
	}
	format, width, height, err = Dims(data)
	if err != nil {
		return nil, "", 0, 0, err
	}
	if width <= 0 || height <= 0 || width > 1024 || height > 1024 || (source == "favicon" && (width < 32 || height < 32)) {
		return nil, "", 0, 0, ErrPixels
	}
	// Decode once here so a truncated payload cannot become a successful cache entry.
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return nil, "", 0, 0, err
	}
	return data, format, width, height, nil
}

// The ICO dependency reads entries sequentially rather than honoring offsets.
// Validate the directory and decode only the selected frame in a canonical ICO.
func avatarICO(data []byte) (out []byte, err error) {
	defer func() {
		if recover() != nil {
			out = nil
			err = ErrFormat
		}
	}() // malformed DIB must never panic the caller
	if len(data) < 22 {
		return nil, ErrFormat
	}
	n := int(binary.LittleEndian.Uint16(data[4:6]))
	if n == 0 || n > 64 || 6+16*n > len(data) {
		return nil, ErrFormat
	}
	best := -1
	area := 0
	for i := 0; i < n; i++ {
		e := data[6+16*i : 22+16*i]
		size, off := int64(binary.LittleEndian.Uint32(e[8:12])), int64(binary.LittleEndian.Uint32(e[12:16]))
		if size < 8 || off < int64(6+16*n) || off+size > int64(len(data)) {
			return nil, ErrFormat
		}
		w, h := int(e[0]), int(e[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		frame := data[off : off+size]
		if bytes.HasPrefix(frame, []byte("\x89PNG\r\n\x1a\n")) {
			cfg, err := png.DecodeConfig(bytes.NewReader(frame))
			if err != nil || cfg.Width != w || cfg.Height != h {
				return nil, ErrFormat
			}
		} else {
			// shortcut: only uncompressed BITMAPINFOHEADER DIBs below 256px; expand after decoder support is verified.
			if len(frame) < 40 || binary.LittleEndian.Uint32(frame) != 40 || binary.LittleEndian.Uint32(frame[4:]) != uint32(w) || binary.LittleEndian.Uint32(frame[8:]) != uint32(2*h) || binary.LittleEndian.Uint32(frame[16:]) != 0 || binary.LittleEndian.Uint16(frame[12:]) != 1 || w == 256 || h == 256 {
				return nil, ErrFormat
			}
			bits := int(binary.LittleEndian.Uint16(frame[14:]))
			if bits != 1 && bits != 4 && bits != 8 && bits != 24 && bits != 32 {
				return nil, ErrFormat
			}
			if int(binary.LittleEndian.Uint16(e[6:8])) != bits {
				return nil, ErrFormat
			}
			colors := int(binary.LittleEndian.Uint32(frame[32:]))
			if bits <= 8 {
				if colors == 0 {
					colors = 1 << bits
				}
				if colors > 1<<bits {
					return nil, ErrFormat
				}
			} else if colors != 0 {
				return nil, ErrFormat
			}
			need := 40 + colors*4 + ((w*bits+31)/32)*4*h
			if bits != 32 {
				need += ((w + 31) / 32) * 4 * h
			}
			if need > len(frame) {
				return nil, ErrFormat
			}
		}
		if w*h > area {
			best = i
			area = w * h
		}
	}
	e := data[6+16*best : 22+16*best]
	off := int(binary.LittleEndian.Uint32(e[12:]))
	size := int(binary.LittleEndian.Uint32(e[8:]))
	single := make([]byte, 22+size)
	copy(single, data[:6])
	binary.LittleEndian.PutUint16(single[4:], 1)
	copy(single[6:], e)
	binary.LittleEndian.PutUint32(single[18:], 22)
	copy(single[22:], data[off:off+size])
	img, err := ico.Decode(bytes.NewReader(single))
	if err != nil {
		return nil, ErrFormat
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	ew, eh := int(e[0]), int(e[1])
	if ew == 0 {
		ew = 256
	}
	if eh == 0 {
		eh = 256
	}
	if w != ew || h != eh {
		return nil, ErrFormat
	}
	if w < 32 || h < 32 || w > 1024 || h > 1024 {
		return nil, ErrPixels
	}
	var b bytes.Buffer
	if err = png.Encode(&b, img); err != nil {
		return nil, err
	}
	if b.Len() > MaxAvatarBytes {
		return nil, ErrTooLarge
	}
	return b.Bytes(), nil
}

// Static shapes supported by MyGo. Reference-bearing features (including use,
// masks, paint servers and CSS) are deliberately omitted to bound rendering.
// shortcut: reject reference-bearing SVG features; expand only with bounded reference validation.
var avatarElements = map[string]bool{"svg": true, "g": true, "title": true, "desc": true, "path": true, "rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true}
var avatarAttributes = map[string]bool{
	"version": true, "baseProfile": true, "viewBox": true, "width": true, "height": true, "preserveAspectRatio": true,
	"id": true, "d": true, "points": true, "x": true, "y": true, "x1": true, "x2": true, "y1": true, "y2": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true,
	"fill": true, "fill-rule": true, "fill-opacity": true, "stroke": true, "stroke-width": true, "stroke-linecap": true, "stroke-linejoin": true, "stroke-miterlimit": true, "stroke-opacity": true, "opacity": true, "transform": true,
}

func avatarSVG(data []byte, bimi bool) (int, int, error) {
	if len(data) > 32<<10 {
		return 0, 0, ErrTooLarge
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	depth, elements, commands := 0, 0, 0
	root, ended, tiny, version := false, false, false, false
	width, height, viewW, viewH := 0., 0., 0., 0.
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, ErrFormat
		}
		switch t := tok.(type) {
		case xml.Directive:
			return 0, 0, ErrFormat
		case xml.ProcInst:
			if t.Target != "xml" || root {
				return 0, 0, ErrFormat
			}
		case xml.StartElement:
			if ended || !avatarElements[t.Name.Local] || (t.Name.Space != "" && t.Name.Space != "http://www.w3.org/2000/svg") {
				return 0, 0, ErrFormat
			}
			depth++
			elements++
			if depth > 32 || elements > 4096 {
				return 0, 0, ErrTooLarge
			}
			if !root {
				if t.Name.Local != "svg" {
					return 0, 0, ErrFormat
				}
				root = true
			} else if t.Name.Local == "svg" {
				return 0, 0, ErrFormat
			}
			seen := map[string]bool{}
			for _, a := range t.Attr {
				if a.Name.Local == "xmlns" && a.Name.Space == "" && a.Value == "http://www.w3.org/2000/svg" {
					continue
				}
				if a.Name.Space != "" || !avatarAttributes[a.Name.Local] || seen[a.Name.Local] {
					return 0, 0, ErrFormat
				}
				seen[a.Name.Local] = true
				v := strings.ToLower(a.Value)
				if strings.ContainsAny(v, "\\&<>;") || strings.Contains(v, "url") || strings.Contains(v, ":") {
					return 0, 0, ErrFormat
				}
				switch a.Name.Local {
				case "width", "height", "x", "y", "x1", "x2", "y1", "y2", "cx", "cy", "r", "rx", "ry", "fill-opacity", "stroke-width", "stroke-miterlimit", "stroke-opacity", "opacity":
					if math.IsNaN(svgNumber(strings.TrimSuffix(a.Value, "px"))) {
						return 0, 0, ErrFormat
					}
				}
				if depth == 1 {
					switch a.Name.Local {
					case "version":
						version = a.Value == "1.2"
					case "baseProfile":
						tiny = a.Value == "tiny-ps"
					case "width":
						width = svgNumber(strings.TrimSuffix(a.Value, "px"))
					case "height":
						height = svgNumber(strings.TrimSuffix(a.Value, "px"))
					case "viewBox":
						nums := strings.Fields(strings.ReplaceAll(a.Value, ",", " "))
						if len(nums) != 4 {
							return 0, 0, ErrFormat
						}
						for _, s := range nums {
							if math.IsNaN(svgNumber(s)) {
								return 0, 0, ErrFormat
							}
						}
						viewW, viewH = svgNumber(nums[2]), svgNumber(nums[3])
						if viewW <= 0 || viewH <= 0 {
							return 0, 0, ErrFormat
						}
					}
				}
				if a.Name.Local == "d" || a.Name.Local == "points" || a.Name.Local == "transform" {
					rest := a.Value
					for _, loc := range svgNumeric.FindAllStringIndex(a.Value, -1) {
						if math.IsNaN(svgNumber(a.Value[loc[0]:loc[1]])) {
							return 0, 0, ErrFormat
						}
					}
					rest = svgNumeric.ReplaceAllString(rest, "")
					if a.Name.Local == "transform" {
						for _, word := range []string{"matrix", "translate", "scale", "rotate", "skewX", "skewY"} {
							rest = strings.ReplaceAll(rest, word, "")
						}
						rest = strings.Trim(rest, " ,\t\r\n()")
					} else if a.Name.Local == "d" {
						rest = strings.Trim(rest, " ,\t\r\nMmZzLlHhVvCcSsQqTtAa")
					} else {
						rest = strings.Trim(rest, " ,\t\r\n")
					}
					if rest != "" {
						return 0, 0, ErrFormat
					}
				}
				if a.Name.Local == "d" {
					for _, r := range a.Value {
						if strings.ContainsRune("MmZzLlHhVvCcSsQqTtAa", r) {
							commands++
						}
					}
					if commands > 16384 {
						return 0, 0, ErrTooLarge
					}
				}
			}
		case xml.EndElement:
			depth--
			if depth == 0 {
				ended = true
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return 0, 0, ErrFormat
			}
		}
	}
	if width == 0 {
		width = viewW
	}
	if height == 0 {
		height = viewH
	}
	if !root || !ended || depth != 0 || (bimi && (!tiny || !version)) || width <= 0 || height <= 0 || math.IsNaN(width) || math.IsNaN(height) {
		return 0, 0, ErrFormat
	}
	return int(math.Ceil(width)), int(math.Ceil(height)), nil
}

var svgNumeric = regexp.MustCompile(`[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?`)

func svgNumber(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e6 {
		return math.NaN()
	}
	return v
}
