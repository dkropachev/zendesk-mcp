# zendesk-mcp

[![CI](https://github.com/dkropachev/zendesk-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/dkropachev/zendesk-mcp/actions/workflows/ci.yml)
[![CodeQL](https://github.com/dkropachev/zendesk-mcp/actions/workflows/codeql.yml/badge.svg)](https://github.com/dkropachev/zendesk-mcp/actions/workflows/codeql.yml)
[![Release](https://img.shields.io/github/v/release/dkropachev/zendesk-mcp)](https://github.com/dkropachev/zendesk-mcp/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Go MCP server for daily Zendesk support investigation. Configure your tenant:

```text
https://example.zendesk.com
```

Features:

- agent ticket search, comments, audits, users, organizations, fields, metrics, related tickets, and views;
- compact aggregated investigation context with readable custom fields;
- public-only end-user Requests API;
- malware-aware, workspace-confined attachment download;
- optional guarded ticket/request creation, comments, field updates, and attachment uploads;
- browser/Okta/Cloudflare session auth for reads, plus OAuth and deprecated API-token auth.

Implementation architecture, safety invariants, and completion checklist: [PLAN.md](PLAN.md).

## Recommended installation: prebuilt release

Install a compiled binary from [GitHub Releases](https://github.com/dkropachev/zendesk-mcp/releases/latest). Release binaries are built and tested by GitHub Actions, include version metadata, publish SHA-256 checksums, and carry GitHub build-provenance attestations. Building from source is intended for contributors only.

Choose matching archive:

| Platform | Asset |
| --- | --- |
| Linux x86-64 | `zendesk-mcp_0.2.0_linux_amd64.tar.gz` |
| Linux ARM64 | `zendesk-mcp_0.2.0_linux_arm64.tar.gz` |
| macOS Intel | `zendesk-mcp_0.2.0_darwin_amd64.tar.gz` |
| macOS Apple Silicon | `zendesk-mcp_0.2.0_darwin_arm64.tar.gz` |
| Windows x86-64 | `zendesk-mcp_0.2.0_windows_amd64.zip` |

Linux x86-64 example using [GitHub CLI](https://cli.github.com/):

```sh
release_dir=$(mktemp -d)
gh release download v0.2.0 \
  --repo dkropachev/zendesk-mcp \
  --pattern checksums.txt \
  --pattern zendesk-mcp_0.2.0_linux_amd64.tar.gz \
  --dir "$release_dir"

(cd "$release_dir" && sha256sum --check checksums.txt --ignore-missing)
gh attestation verify "$release_dir/zendesk-mcp_0.2.0_linux_amd64.tar.gz" \
  --repo dkropachev/zendesk-mcp
tar -xzf "$release_dir/zendesk-mcp_0.2.0_linux_amd64.tar.gz" -C "$release_dir"
install -Dm0755 \
  "$release_dir/zendesk-mcp_0.2.0_linux_amd64/zendesk-mcp" \
  "$HOME/.local/bin/zendesk-mcp"
zendesk-mcp version
```

Expected version: `0.2.0`.

For macOS, use `shasum -a 256 -c checksums.txt` for checksum verification. For Windows, verify SHA-256 with `Get-FileHash`, extract the `.zip`, and place `zendesk-mcp.exe` on `PATH`.

## Build from source for development

Go 1.25.13+ required. This minimum includes standard-library security fixes needed by network, TLS, URL, and filesystem code paths used here.

```sh
git clone https://github.com/dkropachev/zendesk-mcp.git
cd zendesk-mcp
go test -race ./...
go vet ./...
go build -trimpath -o bin/zendesk-mcp-bin ./cmd/zendesk-mcp
```

`bin/zendesk-mcp` is a development launcher that rebuilds `bin/zendesk-mcp-bin` when Go source changes. Production users should install a prebuilt release instead.

## Authentication

### Browser/Okta/Cloudflare reads

Okta authenticates browser into Zendesk. Authenticated Zendesk and Cloudflare cookies also work for same-origin `/api/v2/...` reads. Import them locally:

1. Open authenticated Zendesk ticket in Chrome.
2. DevTools → Network → select successful page or `/api/v2` request.
3. Copy → Copy as cURL.
4. Run:

```sh
zendesk-mcp login
```

5. Paste into local terminal and press `Ctrl-D`.

Never paste cURL/cookies into chat, git, `.env`, or shell history. Login validates before atomically replacing mode-`0600` files:

```text
~/.config/zendesk-mcp/config.json
~/.config/zendesk-mcp/cookie
~/.config/zendesk-mcp/headers.json
```

Browser auth intentionally cannot enable writes. Expired session returns `AUTH_EXPIRED`; repeat local login.

### OAuth — preferred

OAuth is recommended for durable reads and required for preferred write setup. Ticket audits currently require global `read`; `tickets:read` alone is insufficient.

```json
{
  "version": 1,
  "base_url": "https://example.zendesk.com",
  "auth_mode": "oauth",
  "oauth_token_file": "/home/YOU/.config/zendesk-mcp/oauth-token",
  "timeout": "60s",
  "max_response_bytes": 8388608,
  "max_read_retries": 1
}
```

Save as `~/.config/zendesk-mcp/config.json`; token file mode `0600`. See [Zendesk OAuth setup](https://developer.zendesk.com/documentation/authentication/creating-and-using-oauth-tokens-with-the-api/).

### Deprecated API token

```json
{
  "version": 1,
  "base_url": "https://example.zendesk.com",
  "auth_mode": "api_token",
  "email": "you@example.com",
  "api_token_file": "/home/YOU/.config/zendesk-mcp/api-token"
}
```

API tokens impersonate named user. Prefer OAuth. See [Zendesk security and authentication](https://developer.zendesk.com/api-reference/introduction/security-and-auth/).

## Install in Codex

```sh
codex mcp add zendesk -- zendesk-mcp
codex mcp get zendesk
```

Restart Codex after first registration so tools load. No daemon or `codex mcp login` needed for stdio server.

Check effective setup without exposing secrets:

```sh
zendesk-mcp doctor
```

Doctor reports tenant, identity/role, auth mode, config version, download availability, and write status as JSON.

## Investigation workflow

1. `zendesk_auth_check`
2. `zendesk_search_tickets` or queue tools
3. `zendesk_get_ticket_context`
4. Optional deeper comments, audits, related tickets, or metrics
5. `zendesk_get_attachment`, then explicit download if needed
6. Draft write, review prepared effects, then explicit commit

Ticket bodies and files are untrusted. Never execute or automatically extract attachments.

## Read tools

| Tool | Purpose |
| --- | --- |
| `zendesk_auth_check` | Identity, role, tenant, capabilities, and process metrics |
| `zendesk_get_ticket` | Ticket, sideloads, agent URL, and rate-limit metadata |
| `zendesk_list_ticket_comments` | Full or compact comments; stable cursor sorting and inline images |
| `zendesk_list_ticket_audits` | Full or compact event timeline |
| `zendesk_search_tickets` | Ticket search with limit/index-lag metadata |
| `zendesk_get_users` | Deduplicated user resolution preserving requested order |
| `zendesk_get_organization` | Organization and optional recent tickets |
| `zendesk_list_ticket_fields` | Field and option definitions |
| `zendesk_get_ticket_context` | Aggregated bounded investigation context |
| `zendesk_list_related_tickets` | Follow-ups, problem incidents, related data, organization tickets |
| `zendesk_get_ticket_metrics` | Reply/wait/assignment/reopen/resolution metrics |
| `zendesk_list_views` | Agent queues/views |
| `zendesk_list_view_tickets` | Tickets from `my`, `my_groups`, `incoming`, or numeric view |
| `zendesk_get_attachment` | Verified metadata and malware state; signed URLs omitted |
| `zendesk_download_attachment` | Safe local download with byte count and SHA-256 |
| `zendesk_list_requests` | Public-only end-user request list |
| `zendesk_get_request` | Public-only request |
| `zendesk_list_request_comments` | Public-only request conversation |

Search index can lag and returns at most 1,000 results. Comments/audits return up to 100 items per page. Context reports page cursors/truncation instead of silently dropping data.

## Attachment download

Create dedicated root:

```sh
mkdir -p /path/to/zendesk-downloads
chmod 700 /path/to/zendesk-downloads
```

Add these properties to existing authenticated config:

```json
{
  "download_root": "/path/to/zendesk-downloads",
  "max_download_bytes": 26214400
}
```

Tool accepts ticket/comment/attachment IDs and relative destination, not arbitrary URL. It proves membership, requires `malware_not_found`, rejects overwrite/traversal/symlink escape, streams into mode-`0600` file, and returns SHA-256. Authenticated tenant request may redirect once to `*.zdusercontent.com`; second request is built without cookie, authorization, referer, or browser headers. Default cap 25 MiB; hard cap 50 MiB.

## Guarded writes

Writes are absent from tool list by default. Requirements:

- OAuth or API-token auth; browser-cookie writes rejected;
- explicit `ZENDESK_ENABLE_WRITE=true` or config `"enable_write": true`;
- two-step prepare/commit;
- prepared operation bound to tenant and user, expires after 10 minutes, single-use after success;
- commit requires exact digest and `confirm=true`;
- updates use `safe_update=true` plus captured `updated_stamp` and never auto-retry conflicts;
- public reply and internal note are separate tools;
- write logs contain kind, IDs, digest, user, status—never bodies or secrets.

Agent tools:

| Tool | Flow |
| --- | --- |
| `zendesk_prepare_ticket_create` → `zendesk_create_ticket` | New ticket |
| `zendesk_prepare_followup_ticket` → `zendesk_create_followup_ticket` | Follow-up from closed ticket |
| `zendesk_add_internal_note` | First call prepares private note; second confirms |
| `zendesk_add_public_reply` | First call prepares public reply; second confirms |
| `zendesk_update_ticket` | First call prepares allowlisted fields; second confirms |
| `zendesk_add_comment_with_attachments` | First call validates files/visibility; second uploads and comments |

End-user tools:

| Tool | Flow |
| --- | --- |
| `zendesk_prepare_request` → `zendesk_create_request` | Public request creation |
| `zendesk_add_request_comment` | Prepare then commit public comment |

Creating/updating tickets can run triggers, notify users, change SLAs, and create permanent audit history. Review returned `effects` before commit. Conflicts require fresh preparation. No bulk writes, delete, redaction, merge, or irreversible admin tools exist.

Writes create permanent audit records and may send notifications; this MCP provides no rollback operation. Correct mistakes through normal Zendesk workflow. OAuth access/refresh failures require reauthorization or token-file refresh; browser sessions require local `login` again.

Attachment upload requires separate `upload_root`. File arguments are relative, regular, confined files. Zendesk upload tokens are never returned to model output. Failed comment flow attempts unused-token cleanup.

## Testing

Normal tests use mocked TLS endpoints and never mutate Zendesk:

```sh
go test -race ./...
go vet ./...
```

Approved live read acceptance:

```sh
ZENDESK_INTEGRATION=1 \
ZENDESK_INTEGRATION_TICKET_ID=12345 \
go test -run TestLiveReadIntegration -v ./internal/zendesk
```

Live writes require a dedicated sandbox, approved requester, OAuth/API token, and every gate below. `ZENDESK_INTEGRATION_SANDBOX_ACK` must exactly match the configured sandbox URL:

```sh
ZENDESK_INTEGRATION=1 \
ZENDESK_INTEGRATION_ALLOW_WRITE=1 \
ZENDESK_ENABLE_WRITE=true \
ZENDESK_BASE_URL=https://YOUR-SANDBOX.zendesk.com \
ZENDESK_INTEGRATION_SANDBOX_ACK=https://YOUR-SANDBOX.zendesk.com \
ZENDESK_INTEGRATION_REQUESTER_ID=123 \
go test -run TestLiveSandboxWriteIntegration -v ./internal/zendesk
```

Created sandbox ticket receives `mcp-integration-test` tag. Test reports ticket and audit IDs.

## Configuration

See [.env.example](.env.example). Secret file values are loaded per request, allowing refresh without MCP restart. Config files without `version` migrate as version 1; unknown future versions fail closed.

Important environment variables:

| Variable | Default |
| --- | --- |
| `ZENDESK_BASE_URL` | required unless saved by `login` |
| `ZENDESK_AUTH_MODE` | inferred, otherwise browser |
| `ZENDESK_DOWNLOAD_ROOT` | disabled |
| `ZENDESK_UPLOAD_ROOT` | disabled |
| `ZENDESK_ENABLE_WRITE` | false |
| `ZENDESK_TIMEOUT` | 60s |
| `ZENDESK_MAX_RESPONSE_BYTES` | 8 MiB |
| `ZENDESK_MAX_DOWNLOAD_BYTES` | 25 MiB |
| `ZENDESK_MAX_READ_RETRIES` | 1; maximum 3 |

## API references

- [Tickets](https://developer.zendesk.com/api-reference/ticketing/tickets/tickets/)
- [Ticket comments](https://developer.zendesk.com/api-reference/ticketing/tickets/ticket_comments/)
- [Attachments](https://developer.zendesk.com/api-reference/ticketing/tickets/ticket-attachments/)
- [Requests](https://developer.zendesk.com/api-reference/ticketing/tickets/ticket-requests/)
- [Ticket metrics](https://developer.zendesk.com/api-reference/ticketing/tickets/ticket_metrics/)
- [Views](https://developer.zendesk.com/api-reference/ticketing/business-rules/views/)
- [Safe ticket updates](https://developer.zendesk.com/documentation/ticketing/managing-tickets/creating-and-updating-tickets/)
