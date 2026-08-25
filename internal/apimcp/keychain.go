package apimcp

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrKeychainItemNotFound = errors.New("keychain item not found")
	ErrKeychainUnsupported  = errors.New("macOS Keychain is unavailable")
)

type KeychainConfig struct {
	Service         string `json:"service"`
	Account         string `json:"account"`
	PromptOnMissing bool   `json:"promptOnMissing,omitempty"`
}

type KeychainStore interface {
	Get(KeychainConfig) ([]byte, error)
	Set(KeychainConfig, []byte) error
	Delete(KeychainConfig) error
}

func validateKeychainConfig(ref *KeychainConfig) error {
	if ref == nil {
		return errors.New("missing keychain configuration")
	}
	if strings.TrimSpace(ref.Service) == "" {
		return errors.New("keychain requires `service`")
	}
	if strings.TrimSpace(ref.Account) == "" {
		return errors.New("keychain requires `account`")
	}
	return nil
}

func keychainSecret(label string, ref *KeychainConfig, store KeychainStore) (string, error) {
	if err := validateKeychainConfig(ref); err != nil {
		return "", fmt.Errorf("invalid keychain source for %s: %w", label, err)
	}
	value, err := store.Get(*ref)
	if err != nil {
		if errors.Is(err, ErrKeychainItemNotFound) {
			return "", fmt.Errorf("%w: keychain credential for service %q and account %q was not found; run `api-mcp keychain store` with this config", ErrKeychainItemNotFound, ref.Service, ref.Account)
		}
		return "", fmt.Errorf("read %s from keychain: %w", label, err)
	}
	defer zeroBytes(value)
	if len(value) == 0 {
		return "", fmt.Errorf("keychain credential for service %q and account %q is empty", ref.Service, ref.Account)
	}
	return string(value), nil
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
