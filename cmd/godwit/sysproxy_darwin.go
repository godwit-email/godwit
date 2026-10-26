package main

import (
	"net"
	"net/url"
	"strconv"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/godwit-email/godwit/internal/imagefetch"
)

func init() { imagefetch.SystemProxy = systemProxy }

const cfStringUTF8 = 0x08000100

// cfProxyFuncs are the CoreFoundation and CFNetwork calls systemProxy makes.
type cfProxyFuncs struct {
	copySettings func() uintptr
	createString func(uintptr, string, uint32) uintptr
	getValue     func(uintptr, uintptr) uintptr
	getNumber    func(uintptr, int, unsafe.Pointer) bool
	getString    func(uintptr, unsafe.Pointer, int, uint32) bool
	release      func(uintptr)
}

var cfProxy = sync.OnceValue(func() *cfProxyFuncs {
	cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW)
	if err != nil {
		return nil
	}
	network, err := purego.Dlopen("/System/Library/Frameworks/CFNetwork.framework/CFNetwork", purego.RTLD_NOW)
	if err != nil {
		return nil
	}
	f := &cfProxyFuncs{}
	purego.RegisterLibFunc(&f.copySettings, network, "CFNetworkCopySystemProxySettings")
	purego.RegisterLibFunc(&f.createString, cf, "CFStringCreateWithCString")
	purego.RegisterLibFunc(&f.getValue, cf, "CFDictionaryGetValue")
	purego.RegisterLibFunc(&f.getNumber, cf, "CFNumberGetValue")
	purego.RegisterLibFunc(&f.getString, cf, "CFStringGetCString")
	purego.RegisterLibFunc(&f.release, cf, "CFRelease")
	return f
})

// systemProxy is the proxy macOS's network settings give for u: the HTTP or
// HTTPS one by u's scheme, else SOCKS; nil for none.
// shortcut: ignores the bypass list and PAC files; images go to public hosts, which a bypass rarely names.
func systemProxy(u *url.URL) *url.URL {
	f := cfProxy()
	if f == nil {
		return nil
	}
	settings := f.copySettings()
	if settings == 0 {
		return nil
	}
	defer f.release(settings)
	value := func(key string) uintptr {
		k := f.createString(0, key, cfStringUTF8)
		defer f.release(k)
		return f.getValue(settings, k)
	}
	number := func(key string) int {
		var n int32
		if v := value(key); v == 0 || !f.getNumber(v, 9 /* kCFNumberSInt32Type */, unsafe.Pointer(&n)) {
			return 0
		}
		return int(n)
	}
	text := func(key string) string {
		var buf [256]byte
		if v := value(key); v == 0 || !f.getString(v, unsafe.Pointer(&buf[0]), len(buf), cfStringUTF8) {
			return ""
		}
		for i, b := range buf {
			if b == 0 {
				return string(buf[:i])
			}
		}
		return ""
	}
	kinds := []struct{ prefix, scheme string }{{"HTTP", "http"}, {"SOCKS", "socks5"}}
	if u.Scheme == "https" {
		kinds[0].prefix = "HTTPS"
	}
	for _, k := range kinds {
		host, port := text(k.prefix+"Proxy"), number(k.prefix+"Port")
		if number(k.prefix+"Enable") == 1 && host != "" && port > 0 {
			return &url.URL{Scheme: k.scheme, Host: net.JoinHostPort(host, strconv.Itoa(port))}
		}
	}
	return nil
}
