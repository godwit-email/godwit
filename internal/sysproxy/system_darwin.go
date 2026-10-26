package sysproxy

import (
	"net"
	"net/url"
	"strconv"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const cfStringUTF8 = 0x08000100

// cfFuncs are the CoreFoundation and CFNetwork calls system makes.
type cfFuncs struct {
	copySettings func() uintptr
	createString func(uintptr, string, uint32) uintptr
	getValue     func(uintptr, uintptr) uintptr
	getNumber    func(uintptr, int, unsafe.Pointer) bool
	getString    func(uintptr, unsafe.Pointer, int, uint32) bool
	arrayCount   func(uintptr) int
	arrayValue   func(uintptr, int) uintptr
	release      func(uintptr)
}

var cf = sync.OnceValue(func() *cfFuncs {
	core, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW)
	if err != nil {
		return nil
	}
	network, err := purego.Dlopen("/System/Library/Frameworks/CFNetwork.framework/CFNetwork", purego.RTLD_NOW)
	if err != nil {
		return nil
	}
	f := &cfFuncs{}
	purego.RegisterLibFunc(&f.copySettings, network, "CFNetworkCopySystemProxySettings")
	purego.RegisterLibFunc(&f.createString, core, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&f.getValue, core, "CFDictionaryGetValue")
	purego.RegisterLibFunc(&f.getNumber, core, "CFNumberGetValue")
	purego.RegisterLibFunc(&f.getString, core, "CFStringGetCString")
	purego.RegisterLibFunc(&f.arrayCount, core, "CFArrayGetCount")
	purego.RegisterLibFunc(&f.arrayValue, core, "CFArrayGetValueAtIndex")
	purego.RegisterLibFunc(&f.release, core, "CFRelease")
	return f
})

// system reads System Settings → Network → Proxies (the dictionary
// `scutil --proxy` prints).
// shortcut: no PAC file or auto-discovery; read them when a user's proxy app sets only those.
func system() settings {
	f := cf()
	if f == nil {
		return settings{}
	}
	dict := f.copySettings()
	if dict == 0 {
		return settings{}
	}
	defer f.release(dict)
	value := func(key string) uintptr {
		k := f.createString(0, key, cfStringUTF8)
		defer f.release(k)
		return f.getValue(dict, k)
	}
	number := func(key string) int {
		var n int32
		if v := value(key); v == 0 || !f.getNumber(v, 9 /* kCFNumberSInt32Type */, unsafe.Pointer(&n)) {
			return 0
		}
		return int(n)
	}
	text := func(v uintptr) string {
		var buf [256]byte
		if v == 0 || !f.getString(v, unsafe.Pointer(&buf[0]), len(buf), cfStringUTF8) {
			return ""
		}
		for i, b := range buf {
			if b == 0 {
				return string(buf[:i])
			}
		}
		return ""
	}
	entry := func(prefix, scheme string) *url.URL {
		host, port := text(value(prefix+"Proxy")), number(prefix+"Port")
		if number(prefix+"Enable") != 1 || host == "" || port <= 0 {
			return nil
		}
		return &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(port))}
	}
	s := settings{http: entry("HTTP", "http"), https: entry("HTTPS", "http"), socks: entry("SOCKS", "socks5")}
	if list := value("ExceptionsList"); list != 0 {
		for i := range f.arrayCount(list) {
			s.bypass = append(s.bypass, text(f.arrayValue(list, i)))
		}
	}
	return s
}
