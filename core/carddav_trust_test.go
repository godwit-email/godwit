package core_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cardPNG(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 24, 24))))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestCardDAVContactsDoNotGrantImageTrustOrForwardConfirmation(t *testing.T) {
	ctx := context.Background()
	e := coretest.New(t)
	acc := e.AddAccount(t, "alice@example.com")

	dav := e.StartCardDAV(t)
	dav.PutCollection("contacts", "Contacts")
	dav.PutCard("contacts", "carol", strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"FN:Carol Partner",
		"EMAIL;TYPE=PREF:carol@partner.example",
		"PHOTO;ENCODING=b;TYPE=PNG:" + cardPNG(t),
		"END:VCARD",
	}, "\r\n"))

	_, err := e.Svc.AddAddressBook(ctx, core.AddressBookInput{
		Preset:   core.PresetCustom,
		URL:      dav.URL(),
		Username: "alice",
		Password: "secret",
	})
	require.NoError(t, err)

	// 1. CardDAV sync does NOT add the contact to TrustedImageSenders.
	trusted, err := e.Svc.TrustedImageSenders(ctx)
	require.NoError(t, err)
	assert.Empty(t, trusted)

	// Deliver HTML mail from Carol containing an external body image.
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg(
		"Carol Partner <carol@partner.example>",
		"alice@example.com",
		"Quarterly report",
		"Plain text",
		coretest.HTML(`<p>Report</p><img src="https://cdn.partner.example/banner.png" width="320" height="120">`),
	))

	msgs, err := e.Svc.Search(ctx, acc.ID, "Quarterly report", 10)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	msgID := msgs[0].ID

	for _, mode := range []string{"ask", "trusted"} {
		st, err := e.Svc.GetSettings(ctx)
		require.NoError(t, err)
		st.RemoteImages = mode
		_, err = e.Svc.SetSettings(ctx, st)
		require.NoError(t, err)

		// Local CardDAV PHOTO still displays in reader avatar...
		av, err := e.Svc.SenderAvatar(ctx, msgID, false)
		require.NoError(t, err)
		assert.Equal(t, "carddav", av.Source)

		// ...while remote body images stay blocked under both "ask" and "trusted".
		allowed, err := e.Svc.ImagesAllowed(ctx, msgID)
		require.NoError(t, err)
		assert.False(t, allowed, "mode=%s", mode)

		full, err := e.Svc.GetMessage(ctx, msgID, false)
		require.NoError(t, err)
		assert.Equal(t, 1, full.RemoteImagesBlocked, "mode=%s", mode)
		assert.NotContains(t, full.HTML, "https://cdn.partner.example/banner.png", "mode=%s", mode)
	}

	// 2. CardDAV sync does NOT mark never-emailed contacts as known recipients for forwarding rules.
	firstTime, err := e.Svc.FirstTimeRecipients(ctx, []string{"carol@partner.example"})
	require.NoError(t, err)
	assert.Equal(t, []string{"carol@partner.example"}, firstTime)

	// Saving a forwarding rule without explicit recipient confirmation leaves Confirmed empty
	// and blocks unattended forwarding when new mail arrives.
	ruleDoc := core.RuleDoc{
		Version: core.RuleVersion,
		Name:    "Forward invoices to Carol",
		Trigger: core.TriggerReceive,
		Match:   core.MatchAll,
		Conditions: []core.RuleCondition{
			{Field: core.FieldSubject, Op: core.OpContains, Value: "Invoice"},
		},
		Actions: []core.RuleAction{
			{Kind: core.ActionForward, To: "carol@partner.example"},
		},
	}
	ruleID, err := e.Svc.SaveRule(ctx, core.Rule{
		Enabled: true,
		Doc:     ruleDoc,
	})
	require.NoError(t, err)

	rules, err := e.Svc.Rules(ctx)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Empty(t, rules[0].Confirmed)

	e.Deliver(t, acc.ID, "INBOX", coretest.Msg(
		"billing@vendor.example",
		"alice@example.com",
		"Invoice #42",
		"Please pay",
	))

	require.Eventually(t, func() bool {
		hits, err := e.Svc.RuleHits(ctx, ruleID)
		return err == nil && len(hits) == 1 && hits[0].State == core.RuleRunFailed
	}, 3*time.Second, 20*time.Millisecond)

	hits, err := e.Svc.RuleHits(ctx, ruleID)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Len(t, hits[0].Actions, 1)
	assert.Equal(t, core.RuleActionFailed, hits[0].Actions[0].State)
	assert.Equal(t, core.RuleFailUnconfirmed, hits[0].Actions[0].Failure)
	e.AdvanceClock(core.UndoSendWindow)
	assert.Empty(t, e.Sent(acc.ID), "unconfirmed CardDAV contact must never receive forwarded mail")

	// Wit propose_rule targeting Carol also requires explicit recipient confirmation.
	llm := e.LLM(t)
	propArgs, err := json.Marshal(ruleDoc)
	require.NoError(t, err)
	llm.Script(coretest.Turn{
		Calls: []coretest.Call{{ID: "c1", Name: "propose_rule", Args: string(propArgs)}},
	})
	conv, err := e.Svc.NewConversation(ctx, "ask", 0)
	require.NoError(t, err)
	stream, err := e.Svc.Ask(ctx, conv.ID, "Forward invoices to Carol")
	require.NoError(t, err)

	var approvalID int64
	for ev := range stream {
		if len(ev.Approvals) > 0 {
			approvalID = ev.Approvals[0].ID
		}
	}
	require.NotZero(t, approvalID)
	assert.ErrorIs(t, e.Svc.ResolveApproval(ctx, approvalID, true), core.ErrRecipientUnconfirmed)
}
