//go:build darwin

package apimcp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type macOSCredentialPrompter struct{}

func platformCredentialPrompter() CredentialPrompter {
	return macOSCredentialPrompter{}
}

func (macOSCredentialPrompter) Prompt(ctx context.Context, applicationName string) ([]byte, error) {
	first, err := runSecureMacOSDialog(ctx, applicationName, "Enter the API credential for "+applicationName+". It will be stored directly in macOS Keychain and will not be sent through the chat.")
	if err != nil {
		return nil, err
	}
	defer zeroBytes(first)
	if len(first) == 0 {
		return nil, errors.New("credential cannot be empty")
	}

	second, err := runSecureMacOSDialog(ctx, applicationName, "Enter the API credential for "+applicationName+" again to confirm it.")
	if err != nil {
		return nil, err
	}
	defer zeroBytes(second)
	if len(first) != len(second) || subtle.ConstantTimeCompare(first, second) != 1 {
		return nil, errors.New("credentials did not match; nothing was stored")
	}
	return append([]byte(nil), first...), nil
}

func runSecureMacOSDialog(ctx context.Context, title, message string) ([]byte, error) {
	const script = `on run argv
set dialogTitle to item 1 of argv
set dialogMessage to item 2 of argv
set dialogResult to display dialog dialogMessage default answer "" with hidden answer buttons {"Cancel", "Continue"} default button "Continue" cancel button "Cancel" with title dialogTitle
return text returned of dialogResult
end run`

	command := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script, title, message)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "User canceled") || strings.Contains(stderr.String(), "(-128)") {
			return nil, ErrCredentialSetupCanceled
		}
		return nil, fmt.Errorf("open secure macOS credential dialog: %w", err)
	}
	output = bytes.TrimSuffix(output, []byte("\n"))
	output = bytes.TrimSuffix(output, []byte("\r"))
	return output, nil
}
