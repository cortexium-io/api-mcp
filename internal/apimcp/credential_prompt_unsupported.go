//go:build !darwin

package apimcp

import (
	"context"
	"fmt"
)

type unsupportedCredentialPrompter struct{}

func platformCredentialPrompter() CredentialPrompter {
	return unsupportedCredentialPrompter{}
}

func (unsupportedCredentialPrompter) Prompt(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("secure credential setup: %w", ErrKeychainUnsupported)
}
