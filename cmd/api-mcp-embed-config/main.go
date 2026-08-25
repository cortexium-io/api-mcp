package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cortexium-io/api-mcp/internal/apimcp"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("api-mcp-embed-config", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	configPath := flags.String("config", "", "config file to embed")
	modulePath := flags.String("module", "", "Go module path for the api-mcp source")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		return fmt.Errorf("missing required --config")
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument: %s", flags.Arg(0))
	}

	embeddedConfig, err := apimcp.EmbeddedConfigForFile(*configPath)
	if err != nil {
		return err
	}

	module := *modulePath
	if module == "" {
		module, err = goModulePath()
		if err != nil {
			return err
		}
	}

	fmt.Printf("-X %s/internal/apimcp.EmbeddedConfigBase64=%s\n", module, embeddedConfig)
	return nil
}

func goModulePath() (string, error) {
	cmd := exec.Command("go", "list", "-m")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	modulePath := strings.TrimSpace(string(output))
	if modulePath == "" {
		return "", fmt.Errorf("could not determine Go module path")
	}
	return modulePath, nil
}
