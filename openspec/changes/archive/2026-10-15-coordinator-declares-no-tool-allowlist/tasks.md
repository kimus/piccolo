## 1. Generation
- [x] 1.1 agentMD builds frontmatter line-by-line; `tools:` only for non-default roles
- [x] 1.2 toolsFor("main") removed rather than left unused
## 2. Sync and reporting
- [x] 2.1 coordinatorMDPath resolved from agents.list default; both sync paths skip it
- [x] 2.2 frontmatterToolsForConfigTool("mcp") returns nil: no static name can match
- [x] 2.3 tests: generated coordinator has no tools key; specialists still gain enabled tools
