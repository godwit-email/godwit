package main

import (
	"context"
	"os"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/zalando/go-keyring"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}

func TestSeedDataDirPopulatesSyncedAccountsAndCleansKeychain(t *testing.T) {
	ctx := context.Background()
	scale := seedScale{
		Accounts:       2,
		ExtraFolders:   2,
		InboxMessages:  8,
		FolderMessages: 3,
	}
	srv, err := startLoopbackMail(scale)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	dir := t.TempDir()
	if err := seedDataDir(ctx, dir, srv, scale); err != nil {
		t.Fatal(err)
	}

	svc, err := core.New(ctx, core.Options{DataDir: dir, AutoSync: false})
	if err != nil {
		t.Fatal(err)
	}
	accs, err := svc.ListAccounts(ctx)
	if err != nil {
		svc.Close()
		t.Fatal(err)
	}
	if len(accs) != 2 {
		svc.Close()
		t.Fatalf("len(accs) = %d, want 2", len(accs))
	}
	for _, acc := range accs {
		if acc.Status != "ok" {
			svc.Close()
			t.Fatalf("account %s status = %q (%s), want ok", acc.Email, acc.Status, acc.StatusDetail)
		}
		folders, err := svc.ListFolders(ctx, acc.ID)
		if err != nil {
			svc.Close()
			t.Fatal(err)
		}
		if len(folders) != 6 { // INBOX, Sent, Trash, Archive + 2 Projects/*
			svc.Close()
			t.Fatalf("len(folders) = %d, want 6", len(folders))
		}
		var inboxTotal int
		for _, f := range folders {
			if f.Role == "inbox" {
				inboxTotal = f.Total
			}
		}
		if inboxTotal != 8 {
			svc.Close()
			t.Fatalf("inboxTotal = %d, want 8", inboxTotal)
		}
	}
	page, err := svc.ListThreads(ctx, core.View{Unified: "inbox"}, "", 50)
	svc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 4 { // 2 accounts * (8 messages / 4 per thread)
		t.Fatalf("len(threads) = %d, want 4", len(page.Items))
	}

	if err := cleanupSeededAccounts(ctx, dir); err != nil {
		t.Fatal(err)
	}
	svc2, err := core.New(ctx, core.Options{DataDir: dir, AutoSync: false})
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()
	left, err := svc2.ListAccounts(ctx)
	if err != nil || len(left) != 0 {
		t.Fatalf("remaining accounts = %v, %v", left, err)
	}
}
