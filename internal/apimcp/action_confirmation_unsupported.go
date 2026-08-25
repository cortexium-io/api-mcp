//go:build !darwin

package apimcp

import (
	"context"
	"errors"
)

type unsupportedActionConfirmer struct{}

func platformActionConfirmer() ActionConfirmer {
	return unsupportedActionConfirmer{}
}

func (unsupportedActionConfirmer) Confirm(context.Context, ActionConfirmation) error {
	return errors.New("native action confirmation is only supported on macOS")
}
