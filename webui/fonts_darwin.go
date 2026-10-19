//go:build darwin

package webui

import (
	"slices"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
)

var fallbackFonts = []string{
	"American Typewriter", "Arial", "Avenir Next", "Baskerville", "Charter", "Courier New",
	"Futura", "Georgia", "Gill Sans", "Helvetica Neue", "Menlo", "Monaco", "Optima",
	"Palatino", "SF Pro", "Times New Roman", "Trebuchet MS", "Verdana",
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
			if name != "" && !strings.HasPrefix(name, ".") {
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
