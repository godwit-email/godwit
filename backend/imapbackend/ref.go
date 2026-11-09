// Package imapbackend is the IMAP protocol backend (ADR 0037): it speaks IMAP,
// with Gmail's extensions, and hands over change sets.
//
// Its 服务器位置 (backend.MessageRef) and folder cursors are UIDVALIDITY:UID,
// each zero-padded to 10 digits, so the refs of one UIDVALIDITY sort as text
// by UID. A folder's backend.FolderRef is its name.
package imapbackend

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Encode is the server_ref of UID n under validity, or a folder cursor whose
// last seen UID is n.
func Encode(validity, n uint32) string { return fmt.Sprintf("%010d:%010d", validity, n) }

// Decode is Encode's inverse; "" (NULL) and anything malformed decode to 0, 0,
// as the NULL UIDVALIDITY and last_uid of a folder never synced did.
func Decode(ref string) (validity, n uint32) {
	v, u, ok := strings.Cut(ref, ":")
	if !ok {
		return 0, 0
	}
	vv, err1 := strconv.ParseUint(v, 10, 32)
	uu, err2 := strconv.ParseUint(u, 10, 32)
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return uint32(vv), uint32(uu)
}

// Validity is the SQL expression of the UIDVALIDITY part of col, a server_ref
// or cursor column (NULL for NULL), to compare two of them.
func Validity(col string) string { return "substr(" + col + ", 1, 10)" }

// ZeroValidity is the SQL expression of col with its UIDVALIDITY set to 0,
// which no server uses: the folder or location then reads as recreated.
func ZeroValidity(col string) string { return "'0000000000' || substr(" + col + ", 11)" }

// GmailFeature in accounts.features marks a server with Gmail's IMAP
// extensions (X-GM-EXT-1).
const GmailFeature = "X-GM-EXT-1"

// Gmail reports whether accounts.features (not NULL) has Gmail's extensions.
func Gmail(features string) bool { return slices.Contains(strings.Fields(features), GmailFeature) }

// Features is accounts.features for a server with or without them.
func Features(gmail bool) string {
	if gmail {
		return GmailFeature
	}
	return ""
}
