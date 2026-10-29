package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"mcpcloud/internal/setup"
)

func clientConfig(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("client-config", flag.ContinueOnError)
	path := flags.String("config", "", "MCPCloud configuration file path")
	binary := flags.String("binary", "", "executable path (default: current executable)")
	name := flags.String("name", "mcpcloud", "MCP server name")
	client := flags.String("client", "json", "client format: json (mcpServers) or codex (TOML)")
	destination := flags.String("output", "", "write a new snippet file; never overwrite an existing file")
	install := flags.Bool("install", false, "merge the Codex server into its settings; preserve existing entries")
	codexPath := flags.String("codex-config", "", "installation target (default: CODEX_HOME/config.toml or ~/.codex/config.toml)")
	var extraEnv []string
	flags.Func("env-var", "additional environment variable NAME to forward to Codex stdio (repeatable; no values)", func(name string) error {
		extraEnv = append(extraEnv, name)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("client-config does not accept positional arguments")
	}
	if *client != "json" && *client != "codex" {
		return fmt.Errorf("--client must be json or codex")
	}
	if *client != "codex" && (*install || *codexPath != "" || len(extraEnv) > 0) {
		return fmt.Errorf("--install, --codex-config and --env-var require --client codex")
	}
	if (*install && *destination != "") || (!*install && *codexPath != "") {
		return fmt.Errorf("--install cannot use --output; --codex-config requires --install")
	}
	absolute, err := setupPath(*path)
	if err != nil {
		return err
	}
	if *binary == "" {
		*binary, err = os.Executable()
		if err != nil {
			return err
		}
	}
	var content []byte
	if *client == "codex" {
		content, err = setup.CodexClientConfig(*binary, absolute, *name, extraEnv)
	} else {
		content, err = setup.ClientConfig(*binary, absolute, *name)
	}
	if err != nil {
		return err
	}
	if *install {
		result, err := setup.InstallCodex(*codexPath, *name, content)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	if *destination != "" {
		return setup.WriteNewFile(*destination, content)
	}
	_, err = output.Write(content)
	return err
}
