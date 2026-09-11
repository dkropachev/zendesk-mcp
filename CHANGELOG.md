# Changelog

All notable changes use [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) structure. Releases follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.4.1] - 2026-09-10

### Fixed

- Bind stored credentials to their Zendesk tenant, reject non-Zendesk API hosts, and harden attachment-storage host validation.
- Consume confirmed write operations before network execution to prevent ambiguous-failure retries from duplicating mutations.
- Enforce Zendesk's 64 KiB comment-body limit using UTF-8 byte length.
- Batch user resolution at Zendesk's 100-ID API limit.
- Preserve safe attachment-upload cleanup after request cancellation without racing ambiguous ticket updates.
- Validate JSON-RPC and negotiate supported MCP protocol versions.
- Return actionable handler failures as MCP tool results with `isError: true`.

## [0.4.0] - 2026-09-10

### Added

- MCP initialization instructions now include tenant-specific quick Copy-as-cURL login steps.
- Auth-free `zendesk_login_help` tool exposes same steps on demand.
- Setup-only MCP mode initializes without existing configuration and exposes login help before authentication.

## [0.3.0] - 2026-09-10

### Added

- `zendesk-mcp login` extracts OAuth bearer tokens and Zendesk Basic API-token credentials from DevTools Copy as cURL input.
- Automatic credential selection prefers bearer token, then API token, then browser cookies.

### Changed

- Login validates detected authentication before writing local secret files and always disables writes after credential replacement.
- README documents the complete Zendesk tickets-page DevTools Copy as cURL flow.

## [0.2.0] - 2026-09-10

### Added

- Read-only ticket, comment, audit, search, user, organization, field, metric, view, and request tools.
- Aggregated support-investigation context with readable custom fields.
- Malware-aware attachment downloads with credential-stripped storage redirects.
- Optional two-step guarded ticket/request writes and attachment uploads.
- Browser-session, OAuth, and API-token authentication modes.
- Race-tested CI, CodeQL, dependency updates, and cross-platform release automation.
- Go 1.25.13 minimum to include required standard-library security fixes.

[0.2.0]: https://github.com/dkropachev/zendesk-mcp/releases/tag/v0.2.0
[0.3.0]: https://github.com/dkropachev/zendesk-mcp/releases/tag/v0.3.0
[0.4.0]: https://github.com/dkropachev/zendesk-mcp/releases/tag/v0.4.0
[0.4.1]: https://github.com/dkropachev/zendesk-mcp/releases/tag/v0.4.1
