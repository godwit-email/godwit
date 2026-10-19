//go:build darwin

package webui

import (
	"slices"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
)

var webSafeFonts = map[string]bool{
	"American Typewriter": true,
	"Arial":               true,
	"Arial Black":         true,
	"Avenir Next":         true,
	"Baskerville":         true,
	"Charter":             true,
	"Courier New":         true,
	"Futura":              true,
	"Georgia":             true,
	"Gill Sans":           true,
	"Helvetica":           true,
	"Helvetica Neue":      true,
	"Impact":              true,
	"Lucida Grande":       true,
	"Menlo":               true,
	"Monaco":              true,
	"Optima":              true,
	"Palatino":            true,
	"Tahoma":              true,
	"Times New Roman":     true,
	"Trebuchet MS":        true,
	"Verdana":             true,
}

var fallbackFonts = []string{
	"American Typewriter", "Arial", "Avenir Next", "Baskerville", "Charter", "Courier New",
	"Futura", "Georgia", "Gill Sans", "Helvetica Neue", "Menlo", "Monaco", "Optima",
	"Palatino", "SF Pro", "Times New Roman", "Trebuchet MS", "Verdana",
}

func isExcludedFont(name string) bool {
	lower := strings.ToLower(name)
	for _, sub := range []string{"symbol", "emoji", "ding", "webding", "wingding", "ornament", "math", "resort", "braille"} {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

var systemFontsOnce = sync.OnceValue(func() []string {
	ct, err := purego.Dlopen("/System/Library/Frameworks/CoreText.framework/CoreText", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		return fallbackFonts
	}
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		return fallbackFonts
	}
	var copyFamilies func() uintptr
	purego.RegisterLibFunc(&copyFamilies, ct, "CTFontManagerCopyAvailableFontFamilyNames")

	var cfArrayGetCount func(uintptr) int
	purego.RegisterLibFunc(&cfArrayGetCount, cf, "CFArrayGetCount")

	var cfArrayGetValueAtIndex func(uintptr, int) uintptr
	purego.RegisterLibFunc(&cfArrayGetValueAtIndex, cf, "CFArrayGetValueAtIndex")

	var cfStringGetCString func(uintptr, *byte, int, uint32) bool
	purego.RegisterLibFunc(&cfStringGetCString, cf, "CFStringGetCString")

	var cfRelease func(uintptr)
	purego.RegisterLibFunc(&cfRelease, cf, "CFRelease")

	arr := copyFamilies()
	if arr == 0 {
		return fallbackFonts
	}
	defer cfRelease(arr)

	count := cfArrayGetCount(arr)
	buf := make([]byte, 256)
	var fonts []string
	for i := 0; i < count; i++ {
		val := cfArrayGetValueAtIndex(arr, i)
		if cfStringGetCString(val, &buf[0], len(buf), 0x08000100) { // kCFStringEncodingUTF8
			idx := slices.Index(buf, 0)
			if idx < 0 {
				idx = len(buf)
			}
			name := string(buf[:idx])
			if name != "" && !strings.HasPrefix(name, ".") && !isExcludedFont(name) && webSafeFonts[name] {
				fonts = append(fonts, name)
			}
		}
	}
	if len(fonts) == 0 {
		return fallbackFonts
	}
	slices.Sort(fonts)
	return slices.Compact(fonts)
})

func systemFonts() []string {
	return systemFontsOnce()
}
