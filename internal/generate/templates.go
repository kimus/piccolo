package generate

import (
	"fmt"
	"strings"
)

// agentFile is one rendered agent workspace file, in deterministic order.
type agentFile struct {
	Rel     string
	Content []byte
}

// agentFiles renders the workspace files for one agent. role is "main" or a
// specialist id.
func agentFiles(role string, in Inputs) []agentFile {
	files := []agentFile{
		{Rel: "AGENT.md", Content: []byte(agentMD(role, in))},
		{Rel: "SOUL.md", Content: []byte(soulMD(role, in))},
	}
	if role == "main" {
		files = append(files,
			agentFile{Rel: "USER.md", Content: []byte(userMD(in))},
			agentFile{Rel: "memory/MEMORY.md", Content: []byte(memoryMD(in))},
		)
	}
	return files
}

// toolsFor returns the verified v0.3.1 runtime tool names for a role.
func toolsFor(role string, in Inputs) []string {
	var tools []string
	addWeb := func() {
		if in.WebSearch {
			tools = append(tools, "web_search", "web_fetch")
		}
	}
	switch role {
	case "main":
		addWeb()
		tools = append(tools, "message", "read_file", "list_dir", "spawn", "subagent")
	case "research":
		addWeb()
		tools = append(tools, "message", "read_file")
	case "planner":
		addWeb()
		tools = append(tools, "message", "read_file")
	case "coder":
		tools = append(tools, "message", "read_file", "write_file", "edit_file", "list_dir")
		if in.ShellExec {
			tools = append(tools, "exec")
		}
	case "ops":
		tools = append(tools, "message", "read_file", "list_dir")
		if in.ShellExec {
			tools = append(tools, "exec")
		}
	}
	return tools
}

func purposeBlurb(purpose string) string {
	switch purpose {
	case "personal":
		return "a personal assistant: everyday questions, planning, reminders of context, and getting things done"
	case "research":
		return "a research assistant: investigation, reading, synthesis, and well-sourced answers"
	case "coding":
		return "a coding/project assistant: code, reviews, debugging, and project organization"
	case "operations":
		return "an operations assistant: checklists, diagnostics, and reliable follow-through"
	default:
		return "a general-purpose assistant"
	}
}

func toneLine(purpose string) string {
	switch purpose {
	case "research":
		return "Thorough and precise. Cite sources, separate fact from inference, and flag uncertainty."
	case "coding":
		return "Direct and pragmatic. Prefer working code and clear explanations over ceremony."
	case "operations":
		return "Methodical and calm. Checklists, verification steps, and explicit status."
	default:
		return "Concise, helpful, and proactive. Answer first, elaborate only when useful."
	}
}

func agentMD(role string, in Inputs) string {
	name := role
	desc := ""
	var body string

	tools := toolsFor(role, in)

	switch role {
	case "main":
		name = in.AssistantName
		desc = fmt.Sprintf("Coordinator assistant for %s.", in.OwnerName)
		body = fmt.Sprintf(`You are %[1]s, the coordinator assistant for %[2]s, running on the PicoClaw runtime.

## Role

You are %[3]s. You are the default agent: every conversation starts with you.

## Delegation

When a request clearly belongs to a specialist, delegate it with the spawn/subagent
tools instead of doing everything yourself. Available specialists: %[4]s.
Keep ownership of the conversation: summarize specialist results for the user.

## Honesty

Do not claim capabilities this setup does not have. There is no custom Piccolo
memory database, reminders database, or native Piccolo runtime — durable notes
live in memory/MEMORY.md inside your workspace, managed by PicoClaw.

## Working style

- %[5]s
- Verify before asserting; use tools when action is required.
- Keep personal context up to date in memory/MEMORY.md when something seems memorable.
`, in.AssistantName, in.OwnerName, purposeBlurb(in.Purpose), specialistList(in.Specialists), toneLine(in.Purpose))
	default:
		desc = specialistDesc(role)
		body = fmt.Sprintf(`You are the %[1]s specialist agent for %[2]s, running on the PicoClaw runtime.

## Role

%[3]s

## Working style

- %[4]s
- You are invoked by the coordinator agent; return focused, actionable results.
- Stay inside your specialty; hand other work back to the coordinator.
`, role, in.AssistantName, specialistMission(role, in), toneLine(in.Purpose))
	}

	return fmt.Sprintf("---\nname: %s\ndescription: %s\ntools: [%s]\n---\n\n%s",
		name, desc, strings.Join(tools, ", "), body)
}

func specialistList(specs []string) string {
	if len(specs) == 0 {
		return "none configured — handle requests directly"
	}
	return strings.Join(sortedSpecialists(specs), ", ")
}

func specialistDesc(role string) string {
	switch role {
	case "research":
		return "Specialist for investigation, web search, reading, and synthesis."
	case "planner":
		return "Specialist for decomposing goals, producing plans, and organizing work."
	case "coder":
		return "Specialist for coding and project assistance."
	case "ops":
		return "Specialist for operational checklists, diagnostics, and troubleshooting."
	}
	return "Specialist agent."
}

func specialistMission(role string, in Inputs) string {
	switch role {
	case "research":
		return "Investigate questions using web search and reading, then synthesize findings with sources."
	case "planner":
		return "Break goals into concrete, ordered steps with owners, risks, and verification criteria."
	case "coder":
		if in.ShellExec {
			return "Help with code: write, review, and debug; run commands when needed to verify."
		}
		return "Help with code: write, review, and debug. Shell execution is disabled in this setup — provide commands for the user to run instead of executing them."
	case "ops":
		if in.ShellExec {
			return "Maintain operational checklists, run diagnostics, and troubleshoot step by step."
		}
		return "Maintain operational checklists and troubleshoot step by step. Shell execution is disabled — provide commands for the user to run."
	}
	return "Assist within your specialty."
}

func soulMD(role string, in Inputs) string {
	if role == "main" {
		return fmt.Sprintf(`# Soul

You are %s.

%s

You care about being genuinely useful to %s, not about sounding impressive.
Prefer honest "I don't know, here is how I'd find out" over confident guesses.
`, in.AssistantName, toneLine(in.Purpose), in.OwnerName)
	}
	return fmt.Sprintf(`# Soul

You are the %s specialist.

%s

Your pride is in the quality of the artifact you hand back: a synthesis,
a plan, a patch, or a checklist — complete, correct, and ready to use.
`, role, toneLine(in.Purpose))
}

func userMD(in Inputs) string {
	return fmt.Sprintf(`# User

- Name: %s
- Assistant: %s
- Setup purpose: %s

Add preferences, timezone, and context here — the agent reads this file.
`, in.OwnerName, in.AssistantName, in.Purpose)
}

func memoryMD(in Inputs) string {
	return fmt.Sprintf(`# Memory

Durable notes for %s about %s. PicoClaw reads this file at session start and
updates it when something seems memorable. Add or edit entries freely.

(No memories yet.)
`, in.AssistantName, in.OwnerName)
}
