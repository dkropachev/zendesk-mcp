package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dkropachev/zendesk-mcp/internal/mcp"
)

func loginQuickInstruction(baseURL string) string {
	ticketsURL := strings.TrimRight(baseURL, "/") + "/agent/home/tickets"
	return "LOGIN QUICK HELP: open " + ticketsURL + " in Chrome; DevTools > Network; reload; select a successful Zendesk request (prefer /api/v2/users/me.json); Copy > Copy as cURL; run `zendesk-mcp login` locally; paste and press Ctrl-D; restart Codex. Never paste copied cURL, tokens, or cookies into chat."
}

func registerLoginHelp(server *mcp.Server, baseURL string) {
	ticketsURL := strings.TrimRight(baseURL, "/") + "/agent/home/tickets"
	server.RegisterTool(mcp.Tool{
		Name:        "zendesk_login_help",
		Description: "Show quick local Copy-as-cURL login instructions. Does not access Zendesk or expose credentials.",
		InputSchema: objectSchema(map[string]any{}),
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		Handler: func(_ context.Context, raw json.RawMessage) (*mcp.CallToolResult, error) {
			var args struct{}
			if err := decodeArgs(raw, &args); err != nil {
				return nil, err
			}
			return jsonResult(map[string]any{
				"title": "Zendesk MCP login",
				"steps": []string{
					"Open " + ticketsURL + " in Chrome and complete normal sign-in.",
					"Open DevTools (F12 or Ctrl+Shift+I), select Network, then reload page.",
					"Select a successful tenant request; prefer /api/v2/users/me.json.",
					"Right-click request and choose Copy > Copy as cURL.",
					"Run `zendesk-mcp login` in local terminal, paste copied cURL, then press Ctrl-D.",
					"Restart Codex and use /mcp verbose to confirm full Zendesk tool list.",
				},
				"security": "Never paste copied cURL, tokens, or cookies into chat. Credentials stay in local mode-0600 files.",
			})
		},
	})
}
