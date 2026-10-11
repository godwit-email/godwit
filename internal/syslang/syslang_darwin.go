package syslang

import "os/exec"

func osLanguage() string {
	out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	return parseAppleLanguages(string(out))
}
