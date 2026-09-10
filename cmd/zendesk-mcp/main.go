package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dkropachev/zendesk-mcp/internal/mcp"
	toolregistry "github.com/dkropachev/zendesk-mcp/internal/tools"
	"github.com/dkropachev/zendesk-mcp/internal/zendesk"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "zendesk-mcp:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "login":
			return runLogin(args[1:], os.Stdin, os.Stdout)
		case "doctor":
			return runDoctor(os.Stdout)
		case "version", "--version", "-version":
			fmt.Fprintln(os.Stdout, version)
			return nil
		case "help", "--help", "-h":
			printHelp()
			return nil
		default:
			return fmt.Errorf("unknown command %q", args[0])
		}
	}
	cfg, err := zendesk.ConfigFromEnv()
	if err != nil {
		return err
	}
	client, err := zendesk.New(cfg)
	if err != nil {
		return err
	}
	server := newServer(client)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return server.Serve(ctx, os.Stdin, os.Stdout)
}

func printHelp() {
	fmt.Fprintln(os.Stdout, `Usage:
  zendesk-mcp                 run MCP server over stdio
  zendesk-mcp login           extract and verify token/cookie auth from Copy as cURL
  zendesk-mcp doctor          verify config, auth, role, and capabilities
  zendesk-mcp version         print version`)
}

func runDoctor(out *os.File) error {
	cfg, err := zendesk.ConfigFromEnv()
	if err != nil {
		return err
	}
	client, err := zendesk.New(cfg)
	if err != nil {
		return err
	}
	user, err := client.CurrentUser(context.Background())
	if err != nil {
		return err
	}
	data := map[string]any{"status": "ok", "tenant": client.BaseURL(), "user": map[string]any{"id": user.ID, "name": user.Name, "role": user.Role}, "auth_mode": client.AuthMode(), "download_root": client.DownloadRoot(), "attachment_download": client.DownloadRoot() != "", "write_enabled": client.WriteEnabled(), "config_version": cfg.Version}
	encoded, _ := json.Marshal(data)
	fmt.Fprintln(out, string(encoded))
	return nil
}

func newServer(client *zendesk.Client) *mcp.Server { return toolregistry.NewServer(client, version) }
