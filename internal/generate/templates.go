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
	addImage := func() {
		tools = append(tools, "load_image")
	}
	switch role {
	case "main":
		addWeb()
		tools = append(tools, "message", "read_file", "list_dir")
		addImage()
		tools = append(tools, "spawn", "subagent")
	case "research":
		addWeb()
		tools = append(tools, "message", "read_file")
		addImage()
	case "planner":
		addWeb()
		tools = append(tools, "message", "read_file")
		addImage()
	case "coder":
		tools = append(tools, "message", "read_file", "write_file", "edit_file", "list_dir")
		addImage()
		if in.ShellExec {
			tools = append(tools, "exec")
		}
	case "ops":
		tools = append(tools, "message", "read_file", "list_dir")
		addImage()
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
- For exact file decoding, extraction, parsing, and metadata tasks, use the active file-handling skill instead of guessing from vision or prose inspection.
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
- For exact file decoding, extraction, parsing, and metadata tasks, use the active file-handling skill instead of guessing from vision or prose inspection.
`, role, in.AssistantName, specialistMission(role, in), toneLine(in.Purpose))
	}

	return fmt.Sprintf("---\nname: %s\ndescription: %s\ntools: [%s]\nskills: [%s]\n---\n\n%s",
		name, desc, strings.Join(tools, ", "), FileHandlingSkillName, body)
}

// FileHandlingSkillName is the generated PicoClaw skill that teaches agents how
// to perform exact extraction/decoding/parsing for common file types.
const FileHandlingSkillName = "file-handling"

// FileHandlingSkillMD renders the workspace skill loaded by PicoClaw when an
// agent declares skills: [file-handling] in AGENT.md frontmatter.
func FileHandlingSkillMD() string {
	return `---
name: file-handling
description: Read, decode, extract, parse, and inspect common file types using deterministic tools instead of guessing from vision.
---

# File Handling

Use this skill when the user asks to read, decode, extract, parse, inspect, or interpret a file or image and exact contents matter.

## Principles

- Use image understanding for visual descriptions.
- Use deterministic tools for exact payloads, URLs, identifiers, numbers, tables, metadata, or structured data.
- Do not guess exact hidden or machine-readable contents from vision alone.
- In the Piccolo runtime image, the file-handling tools are baked in and available directly on PATH from any working directory: ` + "`zbarimg`" + `, ` + "`file`" + `, ` + "`identify`" + `, ` + "`magick`" + `, ` + "`convert`" + `, ` + "`python3`" + `, ` + "`pdftotext`" + `, ` + "`pdfinfo`" + `, ` + "`pdftoppm`" + `, ` + "`pdftocairo`" + `, ` + "`tesseract`" + `, ` + "`soffice`" + `, ` + "`libreoffice`" + `, ` + "`pandoc`" + `, ` + "`jq`" + `, ` + "`yq`" + `, ` + "`xmllint`" + `, ` + "`exiftool`" + `, ` + "`ffprobe`" + `, ` + "`ffmpeg`" + `, ` + "`sqlite3`" + `, ` + "`unzip`" + `, ` + "`7z`" + `. Call them directly with relative or absolute paths; they behave like ordinary commands.
- Do not use ` + "`apt-get`" + ` (the base image is Alpine) and do not guess exact contents from vision.
- Prefer the deterministic CLI tools above over Python image libraries. Do not import ` + "`cv2`" + ` for QR/barcode/OCR/image normalization unless the user explicitly asks for OpenCV-style processing or the simpler tools fail.
- If a tool is unexpectedly missing, the runtime image was built without it: say so in your answer instead of trying to install anything. Do not run any package manager (apt-get, apk, devbox, nix-env) and do not install tooling at runtime, because the container's toolchain is fixed by the image.
- Do not ask the user before using routine file-handling utilities needed for the requested task. Ask only if exec is unavailable, the setup fails, the package is unusually invasive/heavy, the operation could alter user data, or the request is ambiguous.
- Uploaded media arrives under ` + "`/tmp/picoclaw_media/<name>`" + ` and is readable; use the real path shown in the message's ` + "`[image:/path]`" + ` or ` + "`[file:/path]`" + ` tag directly as an input path.
- Write derived outputs/intermediates under the current workspace, for example ` + "`./work`" + `, ` + "`./converted`" + `, or ` + "`./ocr`" + `. Do not write outputs to generic ` + "`/tmp`" + ` paths and do not use ` + "`../`" + ` traversal. If a command is blocked by the safety guard, retry with media paths only as inputs and workspace-relative output directories.
- Report installation, extraction, parsing, or decoding failures clearly.

## Common mappings

- QR/barcode image: run ` + "`zbarimg --quiet --raw <image-path>`" + ` (package ` + "`zbar`" + `); return the decoded text exactly. If quality is poor, preprocess into ` + "`./work`" + ` with ` + "`magick`" + ` or ` + "`convert`" + `, then retry ` + "`zbarimg`" + `.
- Screenshot/image text: inspect with ` + "`file`" + `/` + "`identify`" + `, optionally normalize with ` + "`magick`" + `, then OCR with ` + "`tesseract <image-path> stdout`" + `.
- Text PDF: inspect with ` + "`pdfinfo <file.pdf>`" + `, then extract with ` + "`pdftotext <file.pdf> -`" + `.
- Scanned/image PDF: render pages into a workspace directory with ` + "`pdftoppm`" + ` or ` + "`pdftocairo`" + `, then OCR with ` + "`tesseract`" + `.
- DOCX/ODT/EPUB/office documents: use ` + "`pandoc <file> -t plain`" + ` or unzip/XML inspection for text-only extraction; use ` + "`soffice --headless --convert-to ... --outdir ./converted <file>`" + ` / ` + "`libreoffice`" + ` when rendered conversion is needed.
- CSV/TSV: parse with Python or another structured parser, not visual guessing.
- JSON: parse with ` + "`jq`" + ` or Python.
- YAML: parse with ` + "`yq`" + ` or Python YAML tooling.
- XML/SVG: parse with ` + "`xmllint`" + ` or Python; inspect SVG as XML unless visual rendering is needed.
- Archives: list contents first and do not blindly extract unsafe or untrusted paths.
- Image metadata: inspect with ` + "`exiftool`" + ` when exact EXIF/GPS/camera data matters.
- Audio/video/media: inspect with ` + "`ffprobe`" + ` or extract frames/audio with ` + "`ffmpeg`" + ` before OCR, vision, or transcription.
- SQLite databases: query with ` + "`sqlite3`" + `.
- Logs/source code: search and parse with text tools or code.
- Unknown files: identify first with ` + "`file <path>`" + `.
`
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
