package core

import (
	"context"
	"encoding/json"
	"strings"
)

// AccountExportVersion is the format version ExportAccounts writes.
const AccountExportVersion = 1

// AccountExport is an exported accounts file: configuration only, never
// passwords, tokens, OAuth client secrets or mail.
type AccountExport struct {
	Version  int               `json:"version"`
	Accounts []ExportedAccount `json:"accounts"`
}

// ExportedAccount is one mail account in an AccountExport. Local archives
// (KindLocalArchive) have no server and are not exported.
type ExportedAccount struct {
	Email          string             `json:"email"`
	Name           string             `json:"name,omitempty"`
	Alias          string             `json:"alias,omitempty"`
	Username       string             `json:"username"`
	Provider       string             `json:"provider,omitempty"`
	OAuthClientID  string             `json:"oauth_client_id,omitempty"`
	IsDefault      bool               `json:"is_default,omitempty"`
	ReceiveOnly    bool               `json:"receive_only,omitempty"`
	Receiving      ExportedServer     `json:"receiving"`
	Sending        ExportedServer     `json:"sending"`
	Sync           ExportedSyncRange  `json:"sync"`
	LabelKeywords  bool               `json:"label_keywords,omitempty"`
	AIInstructions string             `json:"ai_instructions,omitempty"`
	Signature      *ExportedSignature `json:"signature,omitempty"`
}

// ExportedServer is an account's receiving or sending server in an AccountExport.
type ExportedServer struct {
	Protocol string   `json:"protocol"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Security Security `json:"security"`
	Path     string   `json:"path,omitempty"`
	Auth     string   `json:"auth"`
}

// ExportedSyncRange is an account's sync range in an AccountExport; both zero
// means all mail.
type ExportedSyncRange struct {
	Limit  int `json:"limit,omitempty"`
	Months int `json:"months,omitempty"`
}

// ExportedSignature is an account's default signature in an AccountExport.
// Inline signature images are carried as data: URLs inside HTML.
type ExportedSignature struct {
	Name string `json:"name"`
	HTML string `json:"html"`
}

func exportedServer(sv Server) ExportedServer {
	return ExportedServer{
		Protocol: sv.Protocol,
		Host:     sv.Host,
		Port:     sv.Port,
		Security: sv.Security,
		Path:     sv.Path,
		Auth:     sv.Auth,
	}
}

// ExportAccounts exports every mail account's configuration as versioned,
// human-readable JSON, without passwords, tokens, OAuth client secrets, mail
// or local archives.
func (s *Service) ExportAccounts(ctx context.Context) ([]byte, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()

	accs, err := s.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	sigs, err := s.ListSignatures(ctx)
	if err != nil {
		return nil, err
	}
	sigByID := make(map[int64]Signature, len(sigs))
	for _, sig := range sigs {
		sigByID[sig.ID] = sig
	}
	inlined := make(map[int64]*ExportedSignature)

	out := make([]ExportedAccount, 0, len(accs))
	for _, acc := range accs {
		if acc.Kind == KindLocalArchive {
			continue
		}
		var expSig *ExportedSignature
		if sig, ok := sigByID[acc.SignatureID]; ok && acc.SignatureID != 0 {
			expSig = inlined[sig.ID]
			if expSig == nil {
				html := sig.HTML
				if rest, ok := strings.CutPrefix(html, SignatureHTMLPrefix); ok {
					rest, err = s.InlineSignatureImages(ctx, rest)
					if err != nil {
						return nil, err
					}
					html = SignatureHTMLPrefix + rest
				}
				expSig = &ExportedSignature{Name: sig.Name, HTML: html}
				inlined[sig.ID] = expSig
			}
		}
		out = append(out, ExportedAccount{
			Email:          acc.Email,
			Name:           acc.Name,
			Alias:          acc.Alias,
			Username:       acc.Username,
			Provider:       acc.Provider,
			OAuthClientID:  acc.OAuthClientID,
			IsDefault:      acc.IsDefault,
			ReceiveOnly:    acc.ReceiveOnly,
			Receiving:      exportedServer(acc.Receiving),
			Sending:        exportedServer(acc.Sending),
			Sync:           ExportedSyncRange{Limit: acc.Sync.Limit, Months: acc.Sync.Months},
			LabelKeywords:  acc.LabelKeywords,
			AIInstructions: acc.AIInstructions,
			Signature:      expSig,
		})
	}

	b, err := json.MarshalIndent(AccountExport{Version: AccountExportVersion, Accounts: out}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
