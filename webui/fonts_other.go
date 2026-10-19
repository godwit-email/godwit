//go:build !darwin

package webui

var fallbackFonts = []string{
	"Arial", "Courier New", "Georgia", "Helvetica", "Times New Roman", "Trebuchet MS", "Verdana",
}

func systemFonts() []string {
	return fallbackFonts
}
