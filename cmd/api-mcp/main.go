package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cortexium-io/api-mcp/internal/apimcp"
)

var (
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Fprintf(os.Stdout, "api-mcp %s (commit %s, built %s)\n", apimcp.DefaultVersion, commit, buildDate)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "keychain" {
		if err := apimcp.RunKeychainCommand(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	cfg, err := apimcp.LoadConfigFromArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := apimcp.RunStdio(context.Background(), cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
