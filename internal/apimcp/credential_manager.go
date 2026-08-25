package apimcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type credentialManager struct {
	mu              sync.Mutex
	applicationName string
	auth            ResolvedAuthConfig
	store           KeychainStore
	prompter        CredentialPrompter
}

func newCredentialManager(applicationName string, auth ResolvedAuthConfig, store KeychainStore, prompter CredentialPrompter) *credentialManager {
	return &credentialManager{
		applicationName: applicationName,
		auth:            auth,
		store:           store,
		prompter:        prompter,
	}
}

func (m *credentialManager) supportsSetup() bool {
	return m.auth.Keychain != nil && m.auth.Keychain.PromptOnMissing
}

func (m *credentialManager) authForRequest(ctx context.Context) (ResolvedAuthConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.auth.CredentialMissing {
		return m.auth, nil
	}
	if !m.supportsSetup() {
		return ResolvedAuthConfig{}, errors.New("API credential is not configured")
	}

	value, err := m.store.Get(*m.auth.Keychain)
	if errors.Is(err, ErrKeychainItemNotFound) {
		value, err = m.promptAndStoreLocked(ctx)
	}
	if err != nil {
		return ResolvedAuthConfig{}, credentialSetupError(err)
	}
	defer zeroBytes(value)
	m.auth = authWithCredential(m.auth, value)
	return m.auth, nil
}

func (m *credentialManager) setup(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.supportsSetup() {
		return errors.New("secure Keychain setup is not enabled for this MCP server")
	}
	value, err := m.promptAndStoreLocked(ctx)
	if err != nil {
		return credentialSetupError(err)
	}
	defer zeroBytes(value)
	m.auth = authWithCredential(m.auth, value)
	return nil
}

func (m *credentialManager) status() (bool, error) {
	_, stored, err := m.statusDetails()
	return stored, err
}

func (m *credentialManager) statusDetails() (available bool, stored bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.supportsSetup() {
		return !m.auth.CredentialMissing, false, nil
	}
	value, err := m.store.Get(*m.auth.Keychain)
	if errors.Is(err, ErrKeychainItemNotFound) {
		return !m.auth.CredentialMissing, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("check macOS Keychain: %w", err)
	}
	zeroBytes(value)
	return true, true, nil
}

func (m *credentialManager) setApplicationName(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applicationName = name
}

func (m *credentialManager) promptAndStoreLocked(ctx context.Context) ([]byte, error) {
	value, err := m.prompter.Prompt(ctx, m.applicationName)
	if err != nil {
		return nil, err
	}
	if len(value) == 0 {
		return nil, errors.New("credential cannot be empty")
	}
	if err := m.store.Set(*m.auth.Keychain, value); err != nil {
		zeroBytes(value)
		return nil, fmt.Errorf("store credential in macOS Keychain: %w", err)
	}
	return value, nil
}

func authWithCredential(auth ResolvedAuthConfig, value []byte) ResolvedAuthConfig {
	auth.CredentialMissing = false
	switch auth.Type {
	case "bearer":
		auth.Token = string(value)
	case "basic":
		auth.Password = string(value)
	case "apiKey":
		auth.Value = string(value)
	}
	return auth
}

func credentialSetupError(err error) error {
	if errors.Is(err, ErrCredentialSetupCanceled) {
		return errors.New("credential setup was canceled; no credential was stored")
	}
	return err
}
