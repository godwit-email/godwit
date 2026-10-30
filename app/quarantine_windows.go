package app

import "os"

// markFromInternet gives path the Mark of the Web (the Internet zone), so
// SmartScreen and Office check it before it runs.
func markFromInternet(path string) error {
	return os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o644)
}
