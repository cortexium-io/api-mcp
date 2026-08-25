//go:build darwin

package apimcp

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type macOSActionConfirmer struct{}

func platformActionConfirmer() ActionConfirmer {
	return macOSActionConfirmer{}
}

func (macOSActionConfirmer) Confirm(ctx context.Context, confirmation ActionConfirmation) error {
	message, err := actionConfirmationMessage(confirmation)
	if err != nil {
		return err
	}
	title := strings.TrimSpace(confirmation.ApplicationName)
	if title == "" {
		title = "api-mcp"
	}

	const script = `on run argv
set dialogTitle to item 1 of argv
set dialogMessage to item 2 of argv
set dialogResult to display dialog dialogMessage buttons {"Cancel", "Approve"} default button "Cancel" cancel button "Cancel" with title dialogTitle
return button returned of dialogResult
end run`
	command := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script, title, message)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "User canceled") || strings.Contains(stderr.String(), "(-128)") {
			return ErrActionConfirmationCanceled
		}
		return fmt.Errorf("open native macOS action confirmation: %w", err)
	}
	if strings.TrimSpace(string(output)) != "Approve" {
		return ErrActionConfirmationCanceled
	}
	return nil
}
