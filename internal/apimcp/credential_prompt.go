package apimcp

import (
	"context"
	"errors"
)

var ErrCredentialSetupCanceled = errors.New("credential setup was canceled")

type CredentialPrompter interface {
	Prompt(context.Context, string) ([]byte, error)
}
