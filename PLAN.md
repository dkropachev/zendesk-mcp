# Zendesk MCP implementation plan

## Goal

Turn the current read-only proof of concept into a safe daily support-investigation MCP. Primary user is a Zendesk agent investigating customer tickets. Secondary user is an end user viewing and updating only their own requests.

No live ticket was created while producing this plan. Ticket creation can notify customers, run triggers, affect SLAs, and create permanent audit history, so write testing must use an approved Zendesk sandbox and explicit opt-in.

## Implementation status

Implemented September 10, 2026. All checklist items below are present. Mocked TLS tests cover every write path and safety gate. Pre-publication live read and approved attachment-download acceptance passed against a private test ticket; identifying values are omitted. Downloaded test copy was hash-verified and removed without execution or extraction. Sandbox write suite exists but was not run because no approved sandbox credentials/requester were provided.

## Evidence from current implementation and live validation

Current server already provides eight working read tools:

- authentication check;
- ticket retrieval with user, group, and organization sideloads;
- public and private comments;
- ticket audits;
- ticket search;
- user and organization lookup;
- ticket-field definitions.

Live validation against a private test ticket shows investigation requires more than the ticket record itself:

- follow-up/parent ticket lineage;
- ordered public and internal comments;
- author, assignee, group, and organization resolution;
- custom-field labels instead of raw numeric IDs;
- related-ticket and external-issue links;
- attachment metadata and authenticated file retrieval;
- change history and operational timing.

Attachment validation found a two-hop download:

1. Authenticated same-tenant `https://example.zendesk.com/attachments/...` request.
2. HTTP 302 to a signed `https://*.zdusercontent.com/...` URL that succeeds without Zendesk credentials.

The downloader must never forward cookies or authorization headers to the second host.

## User model

### Agent workflow — primary

1. Find ticket from ID, URL, queue/view, requester, organization, tag, or text search.
2. Load a compact case overview: status, priority, age, assignee, group, requester, organization, product/version, linked tickets, and external issues.
3. Read full conversation chronologically, clearly separating public replies from internal notes.
4. Resolve all author IDs and custom-field IDs to readable names.
5. Review audits and ticket metrics when assignment, SLA, status, or chronology matters.
6. Download selected attachments into a controlled workspace and inspect them without execution.
7. Find similar or related tickets and follow-up lineage.
8. Draft a public reply, internal note, ticket update, follow-up ticket, or new ticket.
9. Preview exact customer-visible effects and field changes.
10. Perform write only after explicit user approval, then return new audit/ticket IDs and final state.

### End-user workflow — secondary

A true Zendesk end user must use the Requests API, not the agent Tickets API. End users can see public comments and selected fields only. They cannot see internal notes or agent audit data.

Initial end-user tools:

- `zendesk_list_requests`
- `zendesk_get_request`
- `zendesk_list_request_comments`
- `zendesk_prepare_request`
- `zendesk_create_request`
- `zendesk_add_request_comment`

Tool handlers must check the authenticated role returned by `/api/v2/users/me.json` and enforce the API's role boundary instead of assuming agent permissions.

## Target tool surface

### Keep and improve existing read tools

| Tool | Planned improvement |
| --- | --- |
| `zendesk_auth_check` | Return auth mode, tenant, role, and capability summary without secrets |
| `zendesk_get_ticket` | Add computed agent URL and normalized readable fields |
| `zendesk_list_ticket_comments` | Support inline-image option, stable cursor sorting, and compact/full body modes |
| `zendesk_list_ticket_audits` | Add event filtering and compact timeline mode |
| `zendesk_search_tickets` | Surface indexing lag, pagination ceiling, and rate-limit metadata |
| `zendesk_get_users` | Deduplicate IDs and preserve requested ordering |
| `zendesk_get_organization` | Optionally include recent organization tickets |
| `zendesk_list_ticket_fields` | Cache definitions briefly and map option values to labels |

### Add investigation tools

| Tool | Purpose |
| --- | --- |
| `zendesk_get_ticket_context` | Aggregate ticket, comments, users, organization, fields, metrics, and related links in one bounded response |
| `zendesk_list_related_tickets` | Follow-up source, follow-ups, problem/incidents, same organization, and explicit ticket links |
| `zendesk_get_ticket_metrics` | Reply, wait, assignment, reopen, and resolution timing |
| `zendesk_list_views` | List queues/views available to current agent |
| `zendesk_list_view_tickets` | Browse `my`, `my_groups`, or a selected view with cursor pagination |
| `zendesk_get_attachment` | Retrieve attachment metadata and malware-scan status by ticket/comment/attachment ID |
| `zendesk_download_attachment` | Stream approved attachment to configured workspace and return path, MIME type, byte count, and SHA-256 |

`zendesk_get_ticket_context` should default to compact output and accept controls such as `include_private_comments`, `comment_limit`, `include_audits`, and `include_metrics`. It must report truncation and cursors rather than silently omitting data.

### Add guarded write tools

| Tool | Purpose |
| --- | --- |
| `zendesk_prepare_ticket_create` | Validate and canonicalize a proposed ticket without network mutation |
| `zendesk_create_ticket` | Create a ticket from an approved prepared payload |
| `zendesk_prepare_followup_ticket` | Validate source is closed and preview follow-up payload |
| `zendesk_create_followup_ticket` | Create with `via_followup_source_id` |
| `zendesk_add_internal_note` | Add private comment; private is fixed, not caller-selectable |
| `zendesk_add_public_reply` | Add public customer-visible comment; tool name makes exposure explicit |
| `zendesk_update_ticket` | Update allowlisted fields using collision protection |
| `zendesk_add_comment_with_attachments` | Upload local files and attach tokens to a new comment in one workflow |

Do not expose a generic arbitrary-path or arbitrary-JSON request tool. It would bypass tenant restrictions, write guards, field validation, and auditability.

## Safety model

### Read safety

- Keep base URL pinned to configured HTTPS Zendesk tenant.
- Keep API paths generated by tool handlers.
- Apply response-size limits before allocation.
- Handle `429` using bounded `Retry-After`; expose remaining rate-limit metadata.
- Treat ticket bodies and attachments as untrusted content. Never execute downloaded files or auto-extract archives.
- Redact authorization, cookies, signed attachment URLs, and upload tokens from logs/errors.

### Attachment download safety

- Accept `ticket_id`, `comment_id`, and `attachment_id`; never accept arbitrary URL.
- Fetch comment/attachment metadata first and prove attachment belongs to accessible ticket.
- Reject `deleted`, `malware_found`, or unknown scan state by default.
- Require HTTPS and exact first-party tenant host for first request.
- Permit only one credential-free redirect to an allowlisted `*.zdusercontent.com` host.
- Build second request from scratch with no `Cookie`, `Authorization`, `Referer`, or browser headers.
- Require configured `ZENDESK_DOWNLOAD_ROOT`.
- Resolve and validate destination under root; reject traversal, symlink escape, overwrite, device files, and absolute out-of-root paths.
- Stream to a mode-`0600` temporary file, enforce configured byte limit, hash while writing, then atomically rename.
- Default maximum 25 MiB; configurable maximum cannot exceed Zendesk's current 50 MiB attachment limit without an explicit future decision.

### Write safety

- Default remains read-only.
- Register or enable write tools only when `ZENDESK_ENABLE_WRITE=true`.
- Prefer OAuth for writes. Browser-cookie writes remain disabled until CSRF requirements are captured from a real agent API mutation request and covered by tests.
- Add MCP tool annotations: read-only/destructive/idempotent/open-world hints.
- Use two-step prepare/commit operations. Prepare returns canonical payload, human-readable effects, warnings, and SHA-256 digest. Commit requires matching digest and `confirm=true`.
- Separate public reply and internal note tools. Never use a boolean whose accidental default can expose a private note.
- Always fetch latest ticket and send `safe_update=true` with `updated_stamp=ticket.updated_at`.
- On collision, return conflict and fresh summary; never retry a write automatically.
- Allowlist writable fields. Reject unknown fields and invalid status/priority/type values.
- Resolve assignee/group compatibility before assignment.
- Show whether triggers may email requester/collaborators. Return created audit ID after success.
- Attachments upload first; consume upload tokens in the same high-level call. Delete unused upload tokens on failure when safe. Never return upload tokens to model output.
- No bulk writes, deletion, redaction, merging, or permanent ticket changes in first write release.

## Architecture changes

### HTTP client

Refactor `internal/zendesk/client.go`:

- add `DoJSON(ctx, method, path, query, requestBody, limit)`;
- support GET, POST, PUT, and DELETE with shared auth/error handling;
- keep secrets loaded per request;
- add typed `APIError` with status, request ID, rate limit, retry delay, and safe sanitized details;
- add bounded rate-limit retry for reads only;
- add `DownloadAttachment` with explicit two-request redirect handling;
- add `Upload` streaming from an already validated local file;
- keep external redirects disabled for ordinary API requests.

### Domain layer

Add:

```text
internal/zendesk/types.go
internal/zendesk/tickets.go
internal/zendesk/requests.go
internal/zendesk/attachments.go
internal/zendesk/rate_limit.go
```

Use typed request objects for writable fields. Do not pass untyped `map[string]any` from MCP arguments directly to Zendesk.

### Tool layer

Move registrations out of the 450-line command file:

```text
internal/tools/read.go
internal/tools/context.go
internal/tools/attachments.go
internal/tools/write.go
internal/tools/requests.go
internal/tools/schema.go
```

Add a shared capability object based on authenticated role, configured auth mode, download root, and write flag.

### Prepared-write state

Prepared operations should be short-lived and process-local:

- canonical JSON payload;
- SHA-256 digest;
- expiry, default 10 minutes;
- authenticated user ID and tenant binding;
- no secrets or attachment upload tokens;
- single-use after successful commit.

Process restart invalidates prepared operations. This is acceptable and safer than persisting write intents.

## Delivery phases

### Phase 1 — client foundation and attachment download

- [x] Refactor client to shared method-aware request path.
- [x] Add typed API errors and `429` handling.
- [x] Add attachment metadata lookup.
- [x] Add secure two-hop downloader with credential stripping.
- [x] Add configured download root and file safety checks.
- [x] Add unit tests for hostile redirects, oversize streams, traversal, symlinks, malware states, and credential leakage.
- [x] Live read-only test against an approved attachment; compare bytes and SHA-256 without extracting or executing it.

Exit gate: attachment from an accessible ticket downloads correctly; no test can make credentials reach external storage host.

### Phase 2 — investigation context

- [x] Add typed ticket/comment/user/organization/field models.
- [x] Add readable custom-field option mapping.
- [x] Add ticket context aggregation with explicit truncation metadata.
- [x] Add related/follow-up ticket discovery.
- [x] Add ticket metrics.
- [x] Add views and view-ticket browsing.
- [x] Add sanitized fixtures modeled on real response shapes.

Exit gate: one tool call supplies enough bounded context for normal triage while retaining cursors for deeper inspection.

### Phase 3 — end-user Requests API

- [x] Detect role and advertise capabilities in auth check.
- [x] Add list/show/comment tools for requests.
- [x] Keep public-only semantics explicit in descriptions and output.
- [x] Add prepare/create/update request flows behind write flag.
- [x] Test agent, end-user, anonymous-disabled, unverified-email, and inaccessible-request errors.

Exit gate: end user cannot access private agent data through any tool or fallback path.

### Phase 4 — guarded agent writes

- [x] Add write configuration and MCP annotations.
- [x] Add prepared-operation store and digest confirmation.
- [x] Add create-ticket and create-follow-up flows.
- [x] Add separate internal-note and public-reply tools.
- [x] Add allowlisted field update with `safe_update`/`updated_stamp`.
- [x] Add comment-with-attachments orchestration and unused-upload cleanup.
- [x] Add write audit logging containing IDs and payload digest, never bodies or secrets.
- [x] Require sandbox-only integration suite before production enablement.

Exit gate: stale update cannot overwrite newer work; public communication requires explicit public tool and confirmation; every mutation returns audit evidence.

### Phase 5 — operations and docs

- [x] Extend `doctor` with role, tenant, auth mode, expiry guidance, download-root status, and write status.
- [x] Add config migration/version field.
- [x] Add structured stderr logging with secret filters.
- [x] Add metrics for calls, latency, status classes, rate limits, bytes, and retries without customer content.
- [x] Update README examples for agent and end-user workflows.
- [x] Document session rotation, OAuth refresh, and write rollback limitations.

Exit gate: fresh user can install, authenticate, investigate, download, and safely prepare writes using documented commands.

## Test strategy

### Unit tests

- MCP schemas reject unknown or unsafe fields.
- All generated paths stay under allowed Zendesk endpoints.
- OAuth, API token, and browser auth apply correct headers.
- Secrets never appear in errors.
- Read retries obey `Retry-After`; writes never auto-retry.
- Cursor pagination and response truncation remain explicit.
- Safe-update conflict produces no retry.
- Public/private tools generate fixed visibility.
- Prepare digest changes for any payload change and expires correctly.

### HTTP integration tests

Use `httptest` servers for every verb and response class:

- 200/201/204 success;
- 302 attachment redirect;
- 400/401/403/404/409/422/429/5xx;
- HTML Okta/Cloudflare login response;
- oversized and interrupted streams;
- malicious external redirects;
- Zendesk trigger/audit response shapes.

### Live tests

- Read tests may run with `ZENDESK_INTEGRATION=1` against explicitly named ticket IDs.
- Attachment test downloads to a temporary configured root and deletes the local test copy afterward.
- Write tests require all of:
  - dedicated Zendesk sandbox tenant;
  - `ZENDESK_INTEGRATION_ALLOW_WRITE=1`;
  - explicit requester designated for testing;
  - unique `mcp-integration-test` tag;
  - captured created ticket and audit IDs in test output.
- Require `ZENDESK_INTEGRATION_SANDBOX_ACK` to exactly match the configured sandbox URL before write integration.

## API references

- [Tickets API](https://developer.zendesk.com/api-reference/ticketing/tickets/tickets/)
- [Ticket Comments API](https://developer.zendesk.com/api-reference/ticketing/tickets/ticket_comments/)
- [Attachments API](https://developer.zendesk.com/api-reference/ticketing/tickets/ticket-attachments/)
- [Adding ticket attachments](https://developer.zendesk.com/documentation/ticketing/managing-tickets/adding-ticket-attachments-with-the-api/)
- [Requests API](https://developer.zendesk.com/api-reference/ticketing/tickets/ticket-requests/)
- [Ticket Metrics API](https://developer.zendesk.com/api-reference/ticketing/tickets/ticket_metrics/)
- [Views API](https://developer.zendesk.com/api-reference/ticketing/business-rules/views/)
- [Creating and updating tickets](https://developer.zendesk.com/documentation/ticketing/managing-tickets/creating-and-updating-tickets/)
- [Security and authentication](https://developer.zendesk.com/api-reference/introduction/security-and-auth/)

## Definition of done

- Existing eight read tools remain backward compatible.
- Agent can investigate a ticket without manually joining numeric IDs.
- Attachment download is bounded, workspace-confined, malware-aware, and credential-safe.
- Agent and end-user APIs are separated by authenticated role.
- Ticket creation and updates are disabled by default and use two-step confirmation when enabled.
- Writes use collision protection and return audit evidence.
- No customer content, cookies, tokens, signed URLs, or attachment bytes enter git or logs.
- Unit, race, vet, mocked HTTP integration, and approved live acceptance tests pass.
