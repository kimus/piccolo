package doctor

// Section is one group of the rendered report. Doctor's checks answer different
// questions, and a flat list of 30 lines makes the user find the grouping them:
// "is my Docker/image setup right", "is the workspace intact", "are my secrets
// handled", "will the assistant boot", "what can it actually do". The five
// sections below are those questions, in the order a broken setup is worth
// debugging — the layer that everything else rides on first.
type Section string

const (
	// SecEnvironment is the Docker daemon, the images, and the container that
	// serves traffic: the ground the workspace stands on.
	SecEnvironment Section = "environment"
	// SecWorkspace is Piccolo's own paperwork: piccolo.yaml, the directory
	// layout, .gitignore, and the record of what Piccolo generated.
	SecWorkspace Section = "workspace"
	// SecCredentials is every file that can hold a secret and whether it is
	// sealed: the sidecar, enc:// values, the passphrase, the key, file modes.
	SecCredentials Section = "credentials"
	// SecAssistant is the runtime configuration the gateway loads: config.json,
	// agents, models, and tool posture.
	SecAssistant Section = "assistant"
	// SecCapabilities is what the assistant can do for the user: integrations,
	// MCP reachability, channels, and the web console's chat.
	SecCapabilities Section = "capabilities"
)

// Sections returns the sections in report order. The renderer walks this list,
// so a section that produced no result prints no heading — many checks are
// deliberately silent (env refs, a stale container, MCP with nothing installed,
// a console with no pico entry), and an empty heading would advertise a problem
// where the code decided there was none.
func Sections() []Section {
	return []Section{SecEnvironment, SecWorkspace, SecCredentials, SecAssistant, SecCapabilities}
}

// Title is the heading shown to the user.
func (s Section) Title() string {
	switch s {
	case SecEnvironment:
		return "Environment"
	case SecWorkspace:
		return "Workspace"
	case SecCredentials:
		return "Credentials"
	case SecAssistant:
		return "Assistant"
	case SecCapabilities:
		return "Channels & integrations"
	case "":
		return "Other"
	default:
		return string(s)
	}
}
