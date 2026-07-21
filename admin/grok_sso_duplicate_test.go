package admin

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/codex2api/database"
)

func TestGrokSSOAllowDuplicateFlowsIntoOAuthCredentials(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "grok-sso-allow-duplicate.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("database.New(sqlite) = %v", err)
	}
	defer db.Close()

	h := &Handler{db: db}
	ctx := context.Background()

	seed := tokenCredentialSeed{
		refreshToken:   "rt-duplicate",
		accessToken:    "at-duplicate",
		idToken:        "eyJhbGciOiJSUzI1NiJ9.eyJlbWFpbCI6InNzb0BleGFtcGxlLmNvbSIsImh0dHBzOi8vYXBpLm9wZW5haS5jb20vYXV0aCI6eyJjaGF0Z3B0X2FjY291bnRfaWQiOiJhY2N0LTExMSIsImNoYXRncHRfcGxhbl90eXBlIjoicHJvIn19.sig",
		expiresIn:      3600,
		platform:       "grok",
		baseURL:        "https://api.x.ai/v1",
		allowDuplicate: true,
	}

	id, updated, err := h.upsertOAuthIdentityAccount(ctx, "sso-dup", "", seed, "grok_sso")
	if err != nil {
		t.Fatalf("first upsert = %v", err)
	}
	if updated {
		t.Fatal("first upsert unexpectedly updated an existing row")
	}

	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatalf("GetAccountByID = %v", err)
	}
	if got := row.GetCredential("allow_duplicate"); got != "true" {
		t.Fatalf("allow_duplicate credential = %q, want true", got)
	}

	normalSeed := seed
	normalSeed.allowDuplicate = false
	_, updated, err = h.upsertOAuthIdentityAccount(ctx, "sso-normal", "", normalSeed, "oauth")
	if err != nil {
		t.Fatalf("second upsert = %v", err)
	}
	if updated {
		t.Fatal("normal oauth import should not merge into allow_duplicate copy")
	}

	duplicateID, err := db.FindActiveAccountByOAuthIdentity(ctx, "sso@example.com", "acct-111")
	if err != nil {
		t.Fatalf("FindActiveAccountByOAuthIdentity = %v", err)
	}
	if duplicateID == id {
		t.Fatal("allow_duplicate copy must not be used as the dedupe anchor")
	}
}
