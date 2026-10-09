// Package assets embeds Godwit's brand assets.
package assets

import _ "embed"

// Logo is the Godwit mark as a single-colour SVG (fill="currentColor").
//
//go:embed logo.svg
var Logo []byte
