package apimcp

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

type secretReader func(string) ([]byte, error)

func RunKeychainCommand(args []string, stdin *os.File, output io.Writer) error {
	readSecret := func(prompt string) ([]byte, error) {
		if stdin == nil || !term.IsTerminal(int(stdin.Fd())) {
			return nil, errors.New("keychain enrollment requires an interactive terminal; the credential cannot be supplied through arguments or piped input")
		}
		if _, err := fmt.Fprint(output, prompt); err != nil {
			return nil, err
		}
		value, err := term.ReadPassword(int(stdin.Fd()))
		if _, printErr := fmt.Fprintln(output); err == nil && printErr != nil {
			err = printErr
		}
		return value, err
	}
	return runKeychainCommand(args, output, os.LookupEnv, platformKeychainStore(), readSecret)
}

func runKeychainCommand(args []string, output io.Writer, lookup LookupEnv, store KeychainStore, readSecret secretReader) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return writeKeychainUsage(output)
	}
	if len(args) > 1 && (args[1] == "--help" || args[1] == "-h") {
		return writeKeychainUsage(output)
	}

	command := args[0]
	if command != "store" && command != "status" {
		return fmt.Errorf("unknown keychain command %q; expected `store` or `status`", command)
	}

	input, _, err := loadConfigInputFromArgs(args[1:], lookup)
	if err != nil {
		return err
	}
	if input.Auth == nil || input.Auth.Keychain == nil {
		return errors.New("config auth must include `keychain.service` and `keychain.account`")
	}
	ref := input.Auth.Keychain
	if err := validateKeychainConfig(ref); err != nil {
		return err
	}

	switch command {
	case "status":
		value, err := store.Get(*ref)
		if errors.Is(err, ErrKeychainItemNotFound) {
			_, writeErr := fmt.Fprintf(output, "No credential is stored for service %q and account %q.\n", ref.Service, ref.Account)
			return writeErr
		}
		if err != nil {
			return err
		}
		zeroBytes(value)
		_, err = fmt.Fprintf(output, "A credential is stored for service %q and account %q.\n", ref.Service, ref.Account)
		return err
	case "store":
		first, err := readSecret("Enter credential: ")
		if err != nil {
			return err
		}
		defer zeroBytes(first)
		if len(first) == 0 {
			return errors.New("credential cannot be empty")
		}

		second, err := readSecret("Confirm credential: ")
		if err != nil {
			return err
		}
		defer zeroBytes(second)
		if len(first) != len(second) || subtle.ConstantTimeCompare(first, second) != 1 {
			return errors.New("credentials did not match; nothing was stored")
		}

		if err := store.Set(*ref, first); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "Stored credential in macOS Keychain for service %q and account %q.\n", ref.Service, ref.Account)
		return err
	default:
		panic("unreachable")
	}
}

func writeKeychainUsage(output io.Writer) error {
	_, err := fmt.Fprint(output, `Usage:
  api-mcp keychain store [--config path/to/config.json]
  api-mcp keychain status [--config path/to/config.json]

The config must define auth.keychain.service and auth.keychain.account.
The store command accepts the credential only from an interactive, non-echoing prompt.
`)
	return err
}
