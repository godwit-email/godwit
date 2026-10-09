package syslang

import "golang.org/x/sys/windows"

func osLanguage() string {
	l, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil || len(l) == 0 {
		return ""
	}
	return l[0]
}
