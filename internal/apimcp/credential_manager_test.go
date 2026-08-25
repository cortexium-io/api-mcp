package apimcp

import (
	"context"
	"errors"
	"testing"
)

func TestCredentialManagerPromptsStoresAndCachesMissingCredential(t *testing.T) {
	ref := &KeychainConfig{Service: "api-mcp.test", Account: "default", PromptOnMissing: true}
	store := &fakeKeychainStore{values: map[string][]byte{}}
	prompter := &fakeCredentialPrompter{value: []byte("local-secret")}
	manager := newCredentialManager("test-api", ResolvedAuthConfig{
		Type:              "apiKey",
		In:                "headers",
		Name:              "X-API-Key",
		Keychain:          ref,
		CredentialMissing: true,
	}, store, prompter)

	auth, err := manager.authForRequest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if auth.CredentialMissing || auth.Value != "local-secret" {
		t.Fatalf("unexpected resolved auth: %+v", auth)
	}
	if got, want := store.setCalls, 1; got != want {
		t.Fatalf("Keychain writes = %d, want %d", got, want)
	}
	if got, want := prompter.calls, 1; got != want {
		t.Fatalf("prompts = %d, want %d", got, want)
	}
	if got := string(store.values[keychainTestKey(*ref)]); got != "local-secret" {
		t.Fatalf("stored credential = %q", got)
	}

	if _, err := manager.authForRequest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := prompter.calls, 1; got != want {
		t.Fatalf("prompts after cached resolution = %d, want %d", got, want)
	}
}

func TestCredentialManagerCancelDoesNotWriteKeychain(t *testing.T) {
	ref := &KeychainConfig{Service: "api-mcp.test", Account: "default", PromptOnMissing: true}
	store := &fakeKeychainStore{values: map[string][]byte{}}
	prompter := &fakeCredentialPrompter{err: ErrCredentialSetupCanceled}
	manager := newCredentialManager("test-api", ResolvedAuthConfig{
		Type:              "bearer",
		Keychain:          ref,
		CredentialMissing: true,
	}, store, prompter)

	_, err := manager.authForRequest(context.Background())
	if err == nil || err.Error() != "credential setup was canceled; no credential was stored" {
		t.Fatalf("unexpected cancel error: %v", err)
	}
	if store.setCalls != 0 {
		t.Fatalf("Keychain writes after cancel = %d", store.setCalls)
	}
}

func TestCredentialManagerUsesCredentialStoredSinceStartupWithoutPrompt(t *testing.T) {
	ref := &KeychainConfig{Service: "api-mcp.test", Account: "default", PromptOnMissing: true}
	store := &fakeKeychainStore{values: map[string][]byte{keychainTestKey(*ref): []byte("stored-secret")}}
	prompter := &fakeCredentialPrompter{err: errors.New("prompt should not be called")}
	manager := newCredentialManager("test-api", ResolvedAuthConfig{
		Type:              "bearer",
		Keychain:          ref,
		CredentialMissing: true,
	}, store, prompter)

	auth, err := manager.authForRequest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := auth.Token, "stored-secret"; got != want {
		t.Fatalf("token = %q, want %q", got, want)
	}
	if prompter.calls != 0 {
		t.Fatalf("prompts = %d, want 0", prompter.calls)
	}
}

type fakeCredentialPrompter struct {
	value []byte
	err   error
	calls int
}

func (p *fakeCredentialPrompter) Prompt(context.Context, string) ([]byte, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return append([]byte(nil), p.value...), nil
}

var _ CredentialPrompter = (*fakeCredentialPrompter)(nil)
