package apimcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const maxActionConfirmationDetails = 64 * 1024

var ErrActionConfirmationCanceled = errors.New("action confirmation was canceled")

type ActionConfirmation struct {
	ApplicationName string
	Profile         *APIProfile
	Action          string
	Destination     string
	Details         any
}

type ActionConfirmer interface {
	Confirm(context.Context, ActionConfirmation) error
}

func actionConfirmationMessage(confirmation ActionConfirmation) (string, error) {
	applicationName := strings.TrimSpace(confirmation.ApplicationName)
	if applicationName == "" {
		applicationName = "api-mcp"
	}
	action := strings.TrimSpace(confirmation.Action)
	if action == "" {
		return "", errors.New("action confirmation requires an action")
	}
	details, err := json.MarshalIndent(confirmation.Details, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode action confirmation details: %w", err)
	}
	if len(details) > maxActionConfirmationDetails {
		return "", fmt.Errorf("action confirmation details exceed %d bytes", maxActionConfirmationDetails)
	}

	account := "Single configured API account"
	if confirmation.Profile != nil {
		account = confirmation.Profile.Name + " (" + confirmation.Profile.ID + ")"
	}
	lines := []string{
		"Application: " + applicationName,
		"Account: " + account,
		"Action: " + action,
	}
	if destination := strings.TrimSpace(confirmation.Destination); destination != "" {
		lines = append(lines, "Destination: "+destination)
	}
	lines = append(lines, "", "Validated request:", string(details), "", "Approve this action?")
	return strings.Join(lines, "\n"), nil
}
