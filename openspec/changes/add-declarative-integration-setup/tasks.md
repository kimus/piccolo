# Tasks: add-declarative-integration-setup

## 1. Descriptor and payload parsing

- [x] 1.1 Parse optional `actions[]` from the descriptor (id, role, title, inputs, requires) keeping the schema identifier check unchanged
- [x] 1.2 Validate `inputs[].type` against the closed set (`text`, `secret`, `path`, `select`, `multiselect`, `confirm`) and `requires` against the integration's declared state keys
- [x] 1.3 Ignore unknown roles and unknown descriptor fields without erroring
- [x] 1.4 Parse the state payload (`piccolo.integration.state.v1`) into a fact set
- [x] 1.5 Parse NDJSON action events into typed step/open_url/wait_redirect/result/error values
- [x] 1.6 Accept an optional `{label, url}` on status check remediation and render it

## 2. Menu rendering and dispatch

- [x] 2.1 Render the action menu with huh from descriptor actions plus live state facts
- [x] 2.2 Gate entries on unmet required facts, explaining the missing prerequisite and which actions satisfy it
- [x] 2.3 Render action inputs as huh widgets (input, masked secret, file picker, select, multiselect, confirm)
- [x] 2.4 Refuse to render when no interactive terminal is available, with guidance to run setup interactively
- [x] 2.5 Keep the legacy `setup.command` TTY path working when a descriptor declares no actions

## 3. Action invocation transport

- [x] 3.1 Invoke `<binary> call <action-id>` over the existing attach path
- [x] 3.2 Write collected answers as one JSON document to the action's stdin
- [x] 3.3 Assert no answer value reaches argv or container env, including for secrets
- [x] 3.4 Stream the action's stderr to the user as human logs
- [x] 3.5 Preserve one-shot naming, signal cleanup, and abandoned-container reclaim during long-running actions

## 4. Event consumption and rendering

- [x] 4.1 Render `step` events as progress lines while the action runs
- [x] 4.2 Surface non-JSON stdout lines as logs without aborting the action
- [x] 4.3 Render `open_url` with its label, printing the raw URL always
- [x] 4.4 Skip browser launching when the session is headless or remote
- [x] 4.5 Treat `result` as the only source of configuration proposals
- [x] 4.6 Report `error` events with code, message, remediation, and optional link without applying config

## 5. File brokering

- [x] 5.1 Support `path` inputs via huh's file picker on the host filesystem
- [x] 5.2 Copy the selected file to the declared destination under the integration state directory with mode `0600`
- [x] 5.3 Confirm before overwriting an existing staged file
- [x] 5.4 Pass the container-side path to the action, never the host path
- [x] 5.5 Reject missing, directory, or unreadable selections without staging

## 6. Callback courier

- [x] 6.1 Derive the callback port from the event URL's `redirect_uri` and reject any other port or non-loopback address
- [x] 6.2 Bind `127.0.0.1:<port>`, serve exactly one request, then close
- [x] 6.3 Relay the raw request path and query into the running action container's loopback
- [x] 6.4 Never parse, log, or persist callback query material
- [x] 6.5 On bind conflict, report it and offer to retry rather than silently reusing another port
- [x] 6.6 On timeout with no callback, report that authorization did not complete
- [x] 6.7 Print a host-forward hint when the session is remote

## 7. Piccolo-owned config application

- [x] 7.1 Define a fixed in-code allowlist for integration patches limited to the integration's own MCP server entry
- [x] 7.2 Validate a proposed patch against the allowlist before touching the file, refusing out-of-scope paths and naming them
- [x] 7.3 Apply accepted patches through the existing diff preview, confirmation, `config.json.bak` backup, and atomic write
- [x] 7.4 Make Piccolo the only writer of `config.json` for integration flows
- [x] 7.5 Never read, parse, or modify integration-owned state files

## 8. Verification

- [x] 8.1 Unit-test descriptor action parsing: valid, unknown role, unknown field, unknown input type, bad `requires` key
- [x] 8.2 Unit-test event parsing for each event type plus malformed lines
- [x] 8.3 Unit-test the allowlist refusal of an out-of-scope patch and acceptance of the MCP entry
- [x] 8.4 Test that answers never appear in container spec argv/env
- [x] 8.5 Test file staging: destination, mode `0600`, overwrite confirmation, container-path rewrite, rejection cases
- [x] 8.6 Test the courier: port derived from URL, single-request close, loopback-only, relay payload untouched
- [x] 8.7 Test menu gating from state facts and the no-terminal refusal
- [x] 8.8 Test that a descriptor without actions still uses the legacy TTY path
- [x] 8.9 Run `go build ./...`, `go vet ./...`, `go test ./...`
- [ ] 8.10 Live verify with `piccolo-gws`: add → setup menu → import credentials via file picker → browser login via courier → confirmed config diff → `status` green
- [x] 8.11 Update README for Piccolo-owned setup UI and the action/state/call contract, and AGENTS.md for the descriptor seam
- [x] 8.12 Document the contract surface for integration authors (actions, state keys, event envelope, patch scope)

## 9. Option A follow-up: intent menu, not step menu

- [x] 9.1 Re-render the menu after each action, re-reading `state`, with a `Done` exit row
- [x] 9.2 Apply the pending config patch once, on `Done`, instead of per action
- [x] 9.3 Order the menu so available actions come first and gated ones sink below with their reason
- [x] 9.4 Add optional `inputs[].whenUnmet` so satisfied prerequisites stop re-prompting
- [x] 9.5 Container name per integration+action (`piccolo-setup-<name>-<action>`), still deterministic and reclaimable
- [x] 9.6 Document the loop, conditional inputs, and credential-value model in README
