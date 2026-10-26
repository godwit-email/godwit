package sysproxy

import "golang.org/x/sys/windows/registry"

// system reads Internet Options' manual proxy, which most proxy apps set.
func system() settings {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return settings{}
	}
	defer k.Close()
	if on, _, err := k.GetIntegerValue("ProxyEnable"); err != nil || on == 0 {
		return settings{}
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return settings{}
	}
	override, _, _ := k.GetStringValue("ProxyOverride")
	return parseWindows(server, override)
}
