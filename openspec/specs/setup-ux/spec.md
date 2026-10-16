# setup-ux Specification

## Purpose
TBD - created by archiving change configure-runtime-toolchain. Update Purpose after archive.
## Requirements
### Requirement: Setup toggles tool feedback
Piccolo setup SHALL allow users to enable or disable PicoClaw `agents.defaults.tool_feedback` without manually editing `config.json`.

#### Scenario: Enable tool feedback
- **WHEN** the user enables tool feedback through setup
- **THEN** Piccolo writes `agents.defaults.tool_feedback.enabled: true` to `config.json`
- **AND** preserves or sets a sensible `max_args_length`

#### Scenario: Disable tool feedback
- **WHEN** the user disables tool feedback through setup
- **THEN** Piccolo writes `agents.defaults.tool_feedback.enabled: false` or removes the setting according to the chosen config convention
- **AND** does not alter unrelated agent defaults

#### Scenario: Remote exec recommends feedback
- **WHEN** remote exec is enabled through setup
- **THEN** setup recommends enabling tool feedback for audit/debug visibility

### Requirement: Setup toggles web console consistently
Piccolo setup SHALL allow users to enable or disable the optional web console by updating container image/port settings consistently.

#### Scenario: Enable web console
- **WHEN** the user enables the web console through setup
- **THEN** Piccolo configures a launcher-compatible image or image flavor
- **AND** ensures a `127.0.0.1:18800:18800` port binding is present

#### Scenario: Disable web console
- **WHEN** the user disables the web console through setup
- **THEN** Piccolo removes the web console port binding
- **AND** configures a gateway-compatible image or image flavor

#### Scenario: Custom runtime image flavor is preserved
- **WHEN** a workspace uses a Piccolo runtime image
- **AND** the user toggles the web console
- **THEN** setup chooses or requests the matching gateway/launcher runtime image flavor rather than silently losing runtime toolchain support

### Requirement: Setup uses ESC navigation
Piccolo SHALL implement and advertise ESC as the local navigation key on every interactive screen, including integration setup, not only on `piccolo setup`.

#### Scenario: ESC quits home
- **WHEN** the setup home menu is focused
- **AND** the user presses ESC
- **THEN** setup exits cleanly without applying unrelated changes

#### Scenario: ESC goes back from submenu
- **WHEN** a setup submenu or form is focused
- **AND** the user presses ESC
- **THEN** setup cancels that screen and returns to the previous menu

#### Scenario: Help text matches behavior
- **WHEN** setup renders home or submenu help text
- **THEN** it describes ESC quit/back behavior
- **AND** it does not advertise `q` quit/back unless `q` is actually implemented

#### Scenario: ESC leaves an integration action form
- **WHEN** an integration action is collecting inputs
- **AND** the user presses ESC
- **THEN** that action saves nothing and the menu is redisplayed
- **AND** a configuration proposal earned by an earlier action is not lost

#### Scenario: ESC at the integration menu quits
- **WHEN** the integration action menu is focused
- **AND** the user presses ESC
- **THEN** the session ends without an error and nothing is written

#### Scenario: Declining a confirmation by escaping
- **WHEN** a yes/no gate is focused and the user presses ESC
- **THEN** it is treated as choosing the negative answer
- **AND** it is not reported as a failure

#### Scenario: Navigation keys stay available
- **WHEN** a form is focused
- **THEN** enter still submits and advances a field
- **AND** tab still advances, so the abort key does not consume the keys fields need


### Requirement: Setup edits model aliases without orphaning references
Piccolo setup SHALL treat a model_list entry as one object: editing its alias renames that entry in place, updates every reference to it, and never leaves a second entry behind.

#### Scenario: Rename an entry that is in use
- **WHEN** the user edits a model entry and changes its alias
- **THEN** Piccolo renames that entry in place, keeping its position in `model_list` and any keys Piccolo does not model
- **AND** re-points `agents.defaults.model_name`, `agents.defaults.image_model`, both fallback lists, and every `agents.list[].model` / `agents.list[].subagents.model` that named the old alias
- **AND** the confirmation names each reference it re-points

#### Scenario: Rename onto an alias that already exists
- **WHEN** the new alias matches a different entry
- **THEN** Piccolo refuses and leaves both entries untouched

#### Scenario: An AGENT.md pins the old alias
- **WHEN** a rename orphans a `model:` key in an agent's own frontmatter
- **THEN** Piccolo reports that file instead of editing it, because agent workspace files are user-owned

### Requirement: Setup selects the default text model
Piccolo setup SHALL set `agents.defaults.model_name` — the alias agents inherit — from the configured models, without hand-editing `config.json`.

#### Scenario: Choose a default
- **WHEN** the user opens the models screen and selects another configured model as the default
- **THEN** Piccolo writes `agents.defaults.model_name` and leaves `image_model` and every agent's own model untouched

#### Scenario: The list shows what each entry does
- **WHEN** the models screen renders
- **THEN** each entry is marked with the role it currently holds, and the default and image-model items show their current alias

### Requirement: Setup deletes a model entry
Piccolo setup SHALL remove a model_list entry, and SHALL refuse while an agent still needs it.

#### Scenario: Delete a model still referenced as load-bearing
- **WHEN** the chosen alias is `agents.defaults.model_name`, `agents.defaults.image_model`, or any agent's own `model`
- **THEN** Piccolo deletes nothing and reports each referencing path, pointing at the screen that moves the reference

#### Scenario: Delete a model that appears only in a fallback list
- **WHEN** the chosen alias is named only in a fallback list
- **THEN** Piccolo drops those entries along with the model, deleting an emptied fallback key

#### Scenario: Delete the last remaining model
- **WHEN** the confirmed write leaves `model_list` empty
- **THEN** the next screen warns that doctor will fail until a model is added

### Requirement: Doctor validates every model reference
Piccolo doctor SHALL verify that every model alias config.json names resolves against `model_list`, and SHALL name the field that holds an unresolved one.

#### Scenario: A fallback names a missing model
- **WHEN** `agents.defaults.model_fallbacks` holds an alias absent from `model_list`
- **THEN** doctor fails with that list position in the message

#### Scenario: An agent pins a missing model
- **WHEN** an `agents.list` entry's `model` field — its bare-alias or `primary` form — or an `AGENT.md` frontmatter `model:` names an alias absent from `model_list`
- **THEN** doctor fails, naming the agent and where the reference lives

#### Scenario: A workspace carries the field PicoClaw ignores
- **WHEN** an `agents.list` entry sets `model_name` instead of `model`
- **THEN** doctor warns that PicoClaw ignores it and names the field that works, rather than failing on a reference nothing reads

### Requirement: Generated workspaces leave the exec path guard off
Piccolo SHALL generate `agents.defaults.restrict_to_workspace` as an explicit `false`, because PicoClaw defaults the flag to `true` when the key is absent and its exec guard consumes that flag directly rather than the read access relaxed by `agents.defaults.allow_read_outside_workspace`.

#### Scenario: A fresh workspace can run ordinary commands
- **WHEN** `piccolo init` writes `config.json` with shell execution enabled
- **THEN** `agents.defaults.restrict_to_workspace` is present and `false`
- **AND** the generated file does not rely on the key being absent to mean off

#### Scenario: Tool guidance still prefers workspace outputs
- **WHEN** Piccolo generates the file-handling skill
- **THEN** it continues to advise writing intermediates under the agent workspace
- **AND** it does not present path blocking as the normal state of a Piccolo container

### Requirement: Setup toggles the workspace path guard
Piccolo setup SHALL enable or disable `agents.defaults.restrict_to_workspace` from the Tools screen without hand-editing `config.json`.

#### Scenario: Turn the guard off
- **WHEN** the user disables the workspace path guard
- **THEN** Piccolo writes `agents.defaults.restrict_to_workspace: false`
- **AND** notes that agents can then read, write, and run commands anywhere in the container

#### Scenario: Turn the guard on
- **WHEN** the user enables the workspace path guard
- **THEN** Piccolo writes `agents.defaults.restrict_to_workspace: true`
- **AND** states that shell commands naming paths outside an agent workspace will be refused

#### Scenario: A workspace that never recorded the field
- **WHEN** the Tools screen renders for a `config.json` with no `restrict_to_workspace` key
- **THEN** the entry shows as ON, matching the PicoClaw default for an absent key

### Requirement: Doctor reports the exec path guard
Piccolo doctor SHALL warn while `agents.defaults.restrict_to_workspace` is in effect, naming the guard that will refuse commands and the screen that changes it, and SHALL NOT fail on it.

#### Scenario: Guard on with shell execution enabled
- **WHEN** `restrict_to_workspace` is true, or absent, and `tools.exec.enabled` is true
- **THEN** doctor warns that PicoClaw blocks commands naming paths outside the agent workspace
- **AND** says `allow_read_outside_workspace` does not relax that exec check
- **AND** names `piccolo setup` → Tools as the way to change it

#### Scenario: Guard on without shell execution
- **WHEN** `restrict_to_workspace` is true and `tools.exec` is disabled
- **THEN** doctor warns that write and edit tools stay confined to each agent workspace

#### Scenario: A generated workspace
- **WHEN** doctor runs on a workspace as `piccolo init` generated it
- **THEN** no path-guard finding is reported

### Requirement: Setup toggles the MCP tool
Piccolo setup SHALL show and edit `tools.mcp.enabled` from the Tools screen, so a toggle Piccolo writes on an integration's behalf stays visible and reversible, and SHALL verify the handshake before turning it on — the same verification the registration path runs, because a manual enable can break the gateway just as completely.

#### Scenario: The toggle is off while servers are registered
- **WHEN** the Tools screen renders and `tools.mcp.enabled` is false or absent while `tools.mcp.servers` has entries
- **THEN** the entry shows OFF

#### Scenario: Enabling verifies every registered server first
- **WHEN** the user enables the MCP tool
- **THEN** Piccolo handshakes each registered server with an `initialize` request before writing anything
- **AND** writes `tools.mcp.enabled: true` only when every one answers with a JSON-RPC result
- **AND** names the image each verification ran in, since a pass in one image says nothing about another

#### Scenario: The running container is verified too
- **WHEN** the container serving traffic was created from a different image than `piccolo.yaml` names
- **THEN** Piccolo probes both images, and a server that fails in the running image is reported as a failure

#### Scenario: Verification fails and the user is asked
- **WHEN** any registered server cannot complete the handshake
- **THEN** Piccolo prints each failing server with its reason and states that enabling anyway fails every chat message
- **AND** asks an explicit question that defaults to leaving MCP off, and writes nothing if it is declined or aborted

#### Scenario: No engine to ask
- **WHEN** the container daemon is unreachable
- **THEN** Piccolo reports the check as unverifiable rather than treating it as a pass

#### Scenario: Nothing registered yet
- **WHEN** no `tools.mcp.servers` entry has a spawnable command
- **THEN** Piccolo enables the tool with a note that there was nothing to verify, since an absent server cannot fail

#### Scenario: Enabling MCP changes no agent allowlist
- **WHEN** MCP is toggled from the Tools screen
- **THEN** Piccolo writes no `tools:` key into any `AGENT.md`, because MCP tool names are only known at runtime

### Requirement: The generated coordinator declares no tool allowlist
Piccolo SHALL generate the default agent's `AGENT.md` without a `tools:` frontmatter key, and SHALL NOT add one during an AGENT.md allowlist sync, because PicoClaw reads a declared key as a strict exact-name allowlist that cannot contain the runtime-named `mcp_<server>_<tool>` entries an installed integration provides.

#### Scenario: Coordinator frontmatter after init
- **WHEN** `piccolo init` writes the default agent's `AGENT.md`
- **THEN** the frontmatter carries no `tools:` key
- **AND** specialists keep their reduced `tools:` lists

#### Scenario: Syncing enabled tools
- **WHEN** `piccolo setup` syncs enabled tools into agent allowlists
- **THEN** the default agent's frontmatter is not edited
- **AND** each specialist gains the runtime names of the enabled tools it may use

### Requirement: Doctor and status report the image actually serving traffic
Piccolo doctor SHALL compare the image of the container named `container.name` with `container.image`, reporting a match as an OK line and a mismatch as a warning that names both images and `piccolo restart`, and SHALL stay silent when no such container exists. Piccolo status SHALL report the same mismatch.

#### Scenario: A restart is pending after an integration upgrade
- **WHEN** `piccolo integration add` has baked a new image and rewritten `container.image`, while the running container still holds the previous one
- **THEN** doctor warns that the running gateway holds the old integration binaries while `config.json` is re-read immediately
- **AND** names both the running image and the image `piccolo.yaml` names

#### Scenario: The container matches the manifest
- **WHEN** the running container's image equals `container.image`
- **THEN** doctor prints an OK line naming that image

#### Scenario: No container yet
- **WHEN** no container with the configured name exists
- **THEN** doctor reports no running-image finding, the stale-container check having nothing to say either

### Requirement: Doctor reports MCP reachability
Piccolo doctor SHALL report whether registered MCP integration servers can reach the coordinator, and SHALL state the healthy case as a status line rather than silence.

#### Scenario: MCP tool disabled while servers are registered
- **WHEN** `tools.mcp.servers` has a registered entry and `tools.mcp.enabled` is not true
- **THEN** doctor warns that PicoClaw skips MCP initialization so agents receive none of the integration's tools, naming the servers and the screen that enables the toggle

#### Scenario: Coordinator allowlist blocks integration tools
- **WHEN** MCP is enabled and the default agent's `AGENT.md` declares a `tools:` list
- **THEN** doctor warns on that agent, naming why a written-out list cannot hold `mcp_<server>_<tool>` names

#### Scenario: Registered and reachable
- **WHEN** MCP is enabled, at least one server is registered, and the coordinator declares no allowlist
- **THEN** doctor prints an OK line naming the reachable servers and the coordinator

#### Scenario: Nothing installed
- **WHEN** no MCP server entry is registered
- **THEN** doctor reports no MCP finding

#### Scenario: A server registered but individually disabled
- **WHEN** a `tools.mcp.servers.<key>` entry sets its own `enabled` to false
- **THEN** doctor warns that its tools are not offered to any agent

### Requirement: Doctor reports an installed but unregistered integration
Piccolo doctor SHALL warn when `piccolo.yaml` records an integration that names an MCP server key while `config.json` has no matching `tools.mcp.servers.<key>` entry, and SHALL name the command that completes it.

#### Scenario: Setup was declined before registration
- **WHEN** an installed integration with a `mcp_server_key` has no server entry in `config.json`
- **THEN** doctor warns that the integration is installed but never registered and its tools are offered to no agent
- **AND** names the server key and `piccolo integration setup <ref>` as the way to finish it
- **AND** the finding is a warning, never a failure

#### Scenario: Registration completes the pair
- **WHEN** the server entry exists
- **THEN** doctor reports no unregistered finding for that integration

#### Scenario: An integration that needs no MCP server
- **WHEN** an installed integration records no `mcp_server_key`
- **THEN** doctor does not report a missing server entry for it

### Requirement: Doctor judges a credential by the file that governs it
PicoClaw merges `picoclaw/.security.yml` over `config.json` on load, and its own save path relocates every secure field there, so a field's presence in `config.json` SHALL NOT be treated as the state the gateway will use. Doctor SHALL read the sidecar before judging any model key or channel token, SHALL report which file decides, and SHALL never print a credential value.

#### Scenario: A key that moved to the sidecar is not a missing key
- **WHEN** `config.json` carries no `api_keys` for a model and `picoclaw/.security.yml` names that model with a usable key
- **THEN** doctor reports the model as having a key and says it comes from `picoclaw/.security.yml`
- **AND** does not warn about a placeholder or empty key

#### Scenario: A sidecar key overrides one config.json still displays
- **WHEN** both files carry a usable key for the same model
- **THEN** doctor warns that `config.json`'s value is ignored and names the sidecar key to edit
- **AND** the warning is a warning, never a failure

#### Scenario: A sidecar entry clears a displayed credential
- **WHEN** the sidecar names a model's `api_keys` with an empty list, or a channel's token with an empty string, while `config.json` still shows a value
- **THEN** doctor warns that the gateway runs without that credential

#### Scenario: A null sidecar entry does not shadow
- **WHEN** the sidecar names a model key with no body at all
- **THEN** doctor treats `config.json`'s own value as in force and reports no shadowing

#### Scenario: A sidecar key guards nothing
- **WHEN** the sidecar carries a model key that no `config.json` model_list entry resolves to
- **THEN** doctor warns that the key matches no model, naming the key and never its value

#### Scenario: The sidecar does not parse
- **WHEN** `picoclaw/.security.yml` exists and cannot be parsed
- **THEN** doctor fails, stating that PicoClaw will not load `config.json` while it cannot read the sidecar

#### Scenario: No sidecar at all
- **WHEN** `picoclaw/.security.yml` does not exist
- **THEN** doctor judges every credential from `config.json` alone and reports no sidecar finding

### Requirement: Setup refuses to store a credential the gateway cannot read
When `picoclaw/.security.yml` governs a model's `api_keys` or a channel's token field, writing that credential into `config.json` cannot take effect. Setup SHALL decline the credential, SHALL name the sidecar key that decides instead, and SHALL NOT report a successful write. Non-secret fields on the same entry SHALL still be applied.

#### Scenario: Model key declined with a remedy
- **WHEN** the user enters an API key for a model whose `api_keys` the sidecar overrides
- **THEN** setup does not write the key
- **AND** names the sidecar key to edit
- **AND** still applies provider, model id and base URL for that entry

#### Scenario: The refusal is visible before the input
- **WHEN** setup shows the API key or channel token field for an overridden entry
- **THEN** the field's own description says a write there is inert and names the file that decides
- **AND** the field still renders and remains reviewable

#### Scenario: A credential about to be refused is not validated
- **WHEN** setup would refuse the token a user entered for an overridden channel
- **THEN** setup skips the live provider check rather than reporting success for a credential it will not store

#### Scenario: A rename strands the old sidecar key
- **WHEN** a model whose key lives in the sidecar is renamed
- **THEN** setup reports that the old sidecar key now guards nothing and names the key the renamed model needs

#### Scenario: A delete leaves the key behind
- **WHEN** a model covered by a sidecar key is deleted
- **THEN** setup reports the orphaned key and the consequence for a model re-added under that name

#### Scenario: Piccolo never writes the sidecar
- **WHEN** any setup or init flow touches credentials
- **THEN** Piccolo does not create, edit, or delete `picoclaw/.security.yml`

### Requirement: Serve refuses to start over an unreadable sidecar
Piccolo serve SHALL fail before starting a container when `picoclaw/.security.yml` exists and cannot be parsed, because PicoClaw aborts its whole config load on that error and the health gate would otherwise report a failure naming the wrong file.

#### Scenario: Malformed sidecar blocks startup
- **WHEN** `picoclaw/.security.yml` does not parse
- **THEN** serve exits non-zero without starting a container
- **AND** names the sidecar file and the parse error
