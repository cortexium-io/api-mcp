package apimcp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunKeychainCommandStoresConfirmedCredentialWithoutEchoingIt(t *testing.T) {
	configPath := writeKeychainTestConfig(t)
	store := &fakeKeychainStore{}
	responses := [][]byte{[]byte("top-secret-token"), []byte("top-secret-token")}
	readSecret := func(string) ([]byte, error) {
		response := responses[0]
		responses = responses[1:]
		return response, nil
	}
	var output bytes.Buffer

	err := runKeychainCommand([]string{"store", "--config", configPath}, &output, mapLookup(map[string]string{}), store, readSecret)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := store.setCalls, 1; got != want {
		t.Fatalf("set calls = %d, want %d", got, want)
	}
	if got, want := string(store.lastSet), "top-secret-token"; got != want {
		t.Fatalf("stored credential = %q, want %q", got, want)
	}
	if strings.Contains(output.String(), "top-secret-token") {
		t.Fatalf("command output leaked credential: %q", output.String())
	}
}

func TestRunKeychainCommandRejectsMismatchedConfirmation(t *testing.T) {
	configPath := writeKeychainTestConfig(t)
	store := &fakeKeychainStore{}
	responses := [][]byte{[]byte("first-token"), []byte("second-token")}
	readSecret := func(string) ([]byte, error) {
		response := responses[0]
		responses = responses[1:]
		return response, nil
	}
	var output bytes.Buffer

	err := runKeychainCommand([]string{"store", "--config", configPath}, &output, mapLookup(map[string]string{}), store, readSecret)
	if err == nil || !strings.Contains(err.Error(), "did not match") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
	if store.setCalls != 0 {
		t.Fatalf("credential should not be stored after mismatch; calls = %d", store.setCalls)
	}
}

func TestRunKeychainCommandStatusDoesNotReturnCredential(t *testing.T) {
	configPath := writeKeychainTestConfig(t)
	ref := KeychainConfig{Service: "api-mcp.test", Account: "default"}
	store := &fakeKeychainStore{values: map[string][]byte{keychainTestKey(ref): []byte("top-secret-token")}}
	var output bytes.Buffer

	err := runKeychainCommand([]string{"status", "--config", configPath}, &output, mapLookup(map[string]string{}), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "A credential is stored") {
		t.Fatalf("unexpected status output: %q", output.String())
	}
	if strings.Contains(output.String(), "top-secret-token") {
		t.Fatalf("status output leaked credential: %q", output.String())
	}
}

func TestRunKeychainCommandRequiresInteractiveInput(t *testing.T) {
	configPath := writeKeychainTestConfig(t)
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		readEnd.Close()
		writeEnd.Close()
	})
	var output bytes.Buffer

	err = RunKeychainCommand([]string{"store", "--config", configPath}, readEnd, &output)
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("expected interactive terminal error, got %v", err)
	}
}

func writeKeychainTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
  "spec": "https://api.example.com/openapi.json",
  "auth": {
    "type": "apiKey",
    "in": "headers",
    "name": "X-API-Key",
    "valueEnv": "API_KEY",
    "keychain": {
      "service": "api-mcp.test",
      "account": "default"
    }
  }
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
