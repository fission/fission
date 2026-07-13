---
applyTo: '**'
description: >-
  Use code-review-graph MCP tools for token-efficient
  codebase exploration and code review.
---

<!-- code-review-graph MCP tools -->
## MCP Tools: code-review-graph

**IMPORTANT: This project has a knowledge graph. ALWAYS use the
code-review-graph MCP tools BEFORE using file/search tools to
explore the codebase.** The graph is faster, cheaper (fewer
tokens), and gives you structural context (callers, dependents,
test coverage) that file scanning cannot.

### When to use graph tools FIRST

- **Exploring code**: `semantic_search_nodes` or `query_graph`
- **Understanding impact**: `get_impact_radius`
- **Code review**: `detect_changes` + `get_review_context`
- **Finding relationships**: `query_graph` callers_of/callees_of
- **Architecture questions**: `get_architecture_overview`

Fall back to file/search tools **only** when the graph doesn't
cover what you need.

### Key Tools

| Tool | Use when |
| ------ | ---------- |
| `detect_changes` | Risk-scored change analysis |
| `get_review_context` | Token-efficient source snippets |
| `get_impact_radius` | Blast radius of a change |
| `get_affected_flows` | Impacted execution paths |
| `query_graph` | Trace callers, callees, imports, tests |
| `semantic_search_nodes` | Find functions/classes by keyword |
| `get_architecture_overview` | High-level structure |
| `refactor_tool` | Rename planning, dead code |

### Workflow

1. The graph auto-updates on file changes (via hooks).
2. Use `detect_changes` for code review.
3. Use `get_affected_flows` to understand impact.
4. Use `query_graph` pattern="tests_for" to check coverage.
