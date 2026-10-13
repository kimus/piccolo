// Package picoclaw models the subset of the PicoClaw config.json schema
// (version 3) that Piccolo generates and validates. Fields verified against
// github.com/sipeed/picoclaw@v0.3.1 pkg/config.
package picoclaw

// SchemaVersion is the PicoClaw config schema version Piccolo targets.
const SchemaVersion = 3

// ContainerHome is the in-container PicoClaw home (the workspace mount
// target). PicoClaw resolves it by default; PICOCLAW_HOME is never set.
const ContainerHome = "/root/.picoclaw"

// Config is the generated config.json root.
type Config struct {
	Version   int     `json:"version"`
	Agents    Agents  `json:"agents"`
	ModelList []Model `json:"model_list"`
	Tools     Tools   `json:"tools"`
	Gateway   Gateway `json:"gateway,omitempty"`
}

// Gateway mirrors the picoclaw gateway section (verified v0.3.1
// pkg/config GatewayConfig).
type Gateway struct {
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
}

// DefaultGatewayPort is the PicoClaw gateway/health port default.
const DefaultGatewayPort = 18790

// HealthPort returns the effective gateway port (config value or default).
func (c Config) HealthPort() int {
	if c.Gateway.Port > 0 {
		return c.Gateway.Port
	}
	return DefaultGatewayPort
}

// Agents holds shared defaults plus the per-agent list.
type Agents struct {
	Defaults Defaults `json:"defaults"`
	List     []Agent  `json:"list,omitempty"`
}

// Defaults are shared agent defaults.
type Defaults struct {
	Workspace                 string       `json:"workspace"`
	RestrictToWorkspace       bool         `json:"restrict_to_workspace"`
	AllowReadOutsideWorkspace bool         `json:"allow_read_outside_workspace"`
	ModelName                 string       `json:"model_name"`
	ImageModel                string       `json:"image_model,omitempty"`
	ToolFeedback              ToolFeedback `json:"tool_feedback,omitempty"`
}

// ToolFeedback mirrors PicoClaw's agents.defaults.tool_feedback config.
type ToolFeedback struct {
	Enabled       bool `json:"enabled"`
	MaxArgsLength int  `json:"max_args_length,omitempty"`
}

// Agent is one entry in agents.list.
type Agent struct {
	ID        string     `json:"id"`
	Default   bool       `json:"default,omitempty"`
	Name      string     `json:"name,omitempty"`
	Workspace string     `json:"workspace,omitempty"`
	ModelName string     `json:"model_name,omitempty"`
	Subagents *Subagents `json:"subagents,omitempty"`
}

// Subagents controls which agents this agent may delegate to.
type Subagents struct {
	AllowAgents []string `json:"allow_agents,omitempty"`
}

// Model is one model_list entry. Agents reference it by ModelName.
// PicoClaw resolves provider/model in two steps: when Provider is set, Model
// is sent to that provider unchanged; when Provider is empty, PicoClaw infers
// it from the first "/" segment of Model. Piccolo emits the explicit Provider
// form (upstream's preferred shape), so Model carries NO provider prefix.
type Model struct {
	ModelName string   `json:"model_name"`
	Provider  string   `json:"provider,omitempty"`
	Model     string   `json:"model"`
	APIKeys   []string `json:"api_keys,omitempty"`
	APIBase   string   `json:"api_base,omitempty"`
}

// Tools toggles the tool groups Piccolo exposes in the wizard.
type Tools struct {
	Web       ToolToggle `json:"web"`
	Exec      ToolToggle `json:"exec"`
	Spawn     ToolToggle `json:"spawn"`
	Subagent  ToolToggle `json:"subagent"`
	LoadImage ToolToggle `json:"load_image"`
}

// ToolToggle is a single tool group. AllowRemote is used by tools.exec;
// omitted elsewhere and from generated defaults unless explicitly set.
type ToolToggle struct {
	Enabled     bool  `json:"enabled"`
	AllowRemote *bool `json:"allow_remote,omitempty"`
}

// AgentWorkspace returns the container-side workspace path for an agent id.
func AgentWorkspace(id string) string {
	return "~/.picoclaw/agents/" + id
}
