// Command gen regenerates uiclient/client_gen.go from app/service.go and webui/service.go.
package main

import (
	"fmt"
	"os"

	"github.com/godwit-email/godwit/uiclient"
)

func main() {
	src, err := uiclient.Generate("../app/service.go", "../webui/service.go")
	if err != nil {
		fmt.Fprintln(os.Stderr, "uiclient/cmd/gen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile("client_gen.go", src, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "uiclient/cmd/gen:", err)
		os.Exit(1)
	}
}
