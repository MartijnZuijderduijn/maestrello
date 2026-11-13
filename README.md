# Maestrello

A local kanban board where Claude Code agents work your cards: coding tasks, and read-only research, planning and plain chat. See [SPEC.md](SPEC.md) for the design.

## Install and run

```bash
go install ./cmd/maestrello
```

This puts a `maestrello` binary in `$(go env GOPATH)/bin` (add it to your `PATH`). The web UI is embedded, so it runs from any directory.

```bash
maestrello
```

`maestrello` (same as `maestrello start`) serves the board on http://127.0.0.1:7878 with data in `~/.maestrello`, and opens it in your browser. If a board is already running on that address, it opens that one instead of starting a second. Other commands:

- `maestrello start -open=false`: start without opening the browser. See `maestrello start -h` for all flags (`-addr`, `-data`, `-claude`, `-opencode`, `-mistral`, `-sandbox-root`).
- `maestrello open`: open the running board.
- `maestrello status`: show whether a board is running, and its data dir.
- `maestrello stop`: stop the running board (Ctrl+C in its terminal does the same).
- `maestrello mcp`: stdio bridge to the board's MCP endpoint (see below).
- `maestrello restart`: rebuild from source, stop the running board, and start the new build in this terminal. Pass the flags the board was started with (e.g. `maestrello restart -sandbox-root testdata`). If the build fails, the running board is left alone.

### Driving the board from another MCP client

The board also serves an MCP endpoint for the whole board at `http://127.0.0.1:7878/mcp`: list, read, create, edit and move cards, answer questions, approve, discard and send back. It is for you, not for the card agents. Start the board first.

Claude Code:

```bash
claude mcp add --transport http maestrello http://127.0.0.1:7878/mcp
```

Claude Desktop starts MCP servers as commands, so use the stdio bridge in `claude_desktop_config.json` (add `"-addr", "host:port"` to `args` if the board is not on the default address):

```json
{"mcpServers": {"maestrello": {"command": "/path/to/maestrello", "args": ["mcp"]}}}
```

Changes made this way show up live in open browsers, and the card notes say they came "via MCP".

The new-card form is a single Prompt box (other settings sit under a collapsed Options section). The card's title is derived from the prompt, and refinement (which only rewrites the card text into a clear prompt and never reads code) later replaces it with a short chat-style title; a title you type yourself (card editor, or `title` over the API/MCP) is never overwritten.

A client that already has a detailed prompt can save the refinement tokens: `create_card` takes `skip_refinement` (the description, with the title in front when you gave one, becomes the prompt) or a finished `refined_prompt` with optional `repos`, and the card lands in Ready, or runs at once with `auto_run`. In the browser, tick "Skip refinement" under Options in the new-card form (or set it as the default in Settings).

Automatic model selection (`auto_model`) replaces the manual model setting for a card. It is an option per card (a checkbox in the new-card and edit forms, `auto_model` on `create_card`/`update_card` and the API); the `auto_model` setting in Settings (off by default) only sets the default for new cards. With it on for a card, the refiner is given every model of every available adapter that has capability/cost metadata (`tier` 1-5 and a relative `cost`, 0 = free) and answers with `complexity`, `model`, `harness` and a `model_reason`: the smallest, cheapest model that can do the task well, on a cheaper or free adapter when that is capable enough. The card then runs with that model and adapter (its model, harness, and, when the adapter changes, effort and permission mode are replaced, so the board and card edit show what was chosen), and the choice and reason are logged on the card. The card's manual model select is disabled while its option is on, and the manual model is ignored for it once refined. Built-in metadata covers the claude models (haiku tier 1/cost 1, sonnet 3/3, opus 5/15: rough relative figures, not prices); add or override entries, e.g. for opencode/Ollama models, with `model_info` in Settings (a JSON box, or `PUT /api/settings`): `{"opencode/ollama/qwen3-coder:30b": {"tier": 3, "cost": 0}}`. A model without metadata is never offered. If the refiner's choice is missing, unknown or unavailable, the board uses its own rule (cheapest model whose tier covers the `complexity`) when the task was rated, and otherwise keeps the card's manual model; a card that skips refinement also keeps its manual model. With the option off nothing changes.

Every card runs with a harness, the coding-agent CLI behind it (`claude`, `opencode` or `mistral`; mistral drives Mistral Vibe (`vibe`) with no effort setting and no session resume, see SPEC.md for what was not verified live; opencode drives the `opencode` CLI, so it can use Ollama models such as `ollama/qwen3-coder:30b`, and has no effort setting). The board card shows it first in its meta line (`#12 · claude · sonnet · medium · auto`), the new-card, edit and Settings forms have a harness select (the default for new cards is in Settings), and `GET /api/harnesses` lists the harnesses with the efforts and permission modes each accepts. The board checks in the background which harness CLIs work on this machine (binary found and `--version` runs; point `-claude` / `-opencode` / `-mistral` at other paths if needed), shows it in Settings with a re-check button, marks unavailable harnesses in the selects and refuses to run cards on them with the reason. opencode has no permission callback (permission modes are `auto` and `plan`) and no structured output (refinement JSON is read from its text). `list_cards` includes each card's `harness`, and `create_card`/`update_card` take it.

### Card kinds

Every card has a kind (a select in the new-card form and the card editor; `kind` over the API and MCP). They share the lifecycle, the MCP endpoint and the UI:

- `code` (default): edits repos in worktrees (or local work); `report_done`, then you review the diffs and approve. On approve each repo gets an action: **Merge into <base>** (merges the card branch into the base branch of your local checkout, only when that checkout is on that branch with no uncommitted changes to tracked files; fast-forward when possible, else a merge commit `maestrello: merge #<id> <title>`; a conflict is aborted and the branch kept; the branch is deleted after a merge; never pushes), keep local, push branch, or push + PR.
- `research`: reads the codebase under the root folder (Read, Grep, Glob only; no worktrees, no leases) and ends with a written report via `report_done`. Review shows it as the Report; Approve closes the card.
- `plan`: reads the codebase and calls `report_plan(plan, tasks)`. Review shows the plan and a checklist of the proposed tasks; "Create N cards" approves the plan and the board creates one Todo card per ticked task in the same project (over MCP: `approve_card` with `tasks`, the indices to create). Agents never create cards themselves. Send back with feedback revises the plan.
- `chat`: a conversation with no built-in tools (unless you switch on toolbox tools such as web search) and no refinement. Each reply lands in Needs input; your answer resumes the same session; "Close chat" (or moving the card to Done) ends it.

The board card shows the kind in its meta line when it is not `code` (`#12 · research · claude · sonnet …`).

### Settings layers

Settings come in layers, so projects and card kinds can run with different harnesses, models and limits. For every setting the most specific value that is set wins:

1. card kind within a project (`kinds.<kind>` in the project's settings)
2. the project (the project's own settings)
3. card kind at the global level (`kinds.<kind>` in the global settings)
4. the global settings
5. the built-in defaults (what the global settings start as)

A layer that leaves a setting out falls through to the next one; it never resets it. A value can still be set on purpose to "empty" (`"effort": ""`, `"max_turns": 0`, `"auto_run": false`). Layers can set: `harness`, `model`, `effort`, `permission_mode`, `max_turns`, `max_minutes`, `push_default`, `refine_model`, `refine_effort`, `auto_run`, `skip_refinement`, `auto_model` and `tools` (the toolbox ids new cards of that kind start with). The root folder and `model_info` stay global. If the resolved harness does not accept the resolved effort or permission mode (e.g. a layer switches to `opencode`, which has no effort), the value is dropped or replaced by the harness's first mode, like in a card edit.

Where each layer lives (existing storage, no new format): the global layer is the `settings` JSON, where `kinds` is new; a project's layer is the `settings` field of the project (a new column on the `projects` table, added on start; existing databases and settings need no change). Example:

```json
{"harness": "claude", "model": "sonnet",
 "kinds": {"research": {"model": "opus"}}}
```

```json
{"model": "haiku",
 "kinds": {"research": {"harness": "opencode", "model": "ollama/qwen3-coder:30b"}}}
```

View and edit: in the browser, Settings has the global fields, a JSON box "Overrides per card kind", and a project picker with a JSON box for that project's settings. Over HTTP, `GET/PUT /api/settings` (with `kinds`; a PUT without `kinds` leaves them alone, `{}` clears them), `PATCH /api/projects/<id>` with `settings` (replaces the project's settings, `{}` clears them; also returned by `GET /api/projects`), and `GET /api/settings/resolved?project=<id>&kind=<kind>`, which shows what a card of that kind in that project starts with and which layer each overridden value came from.

Where they apply: a new card is seeded from the settings resolved for its project and kind (the project is the one named, or the existing one for its root folder), and anything the input sets still wins. A card keeps the values it was created with, so changing a layer affects new cards, not existing ones, and the card runs with its own harness, model and limits. Refinement resolves for the card's project and kind when it runs, so a layer can also pick the refinement harness, model and effort. With no layers configured, everything behaves as before.

### Additional context and the folder picker

A card can carry additional context: folders and files outside its repos that the agent may read (a design doc, a sibling project, a spec). Add them under "Additional context" in the card editor (Todo and Ready), or with `context`, a list of absolute paths, on the API and on `create_card`/`update_card` (at most 20; each must exist and be inside the sandbox when one is on; chat cards ignore it). Refinement lists the paths, and a deep refinement can read them; sessions get each folder (for a file, its parent folder) through `--add-dir`. Context is read-only by instruction only: the agent is told not to change it, the board does not enforce that.

Folder and file choices (the root-folder **Browse…** buttons and **Add folder… / Add file…** for context) use an in-page picker backed by `GET /api/fs/list`, so no desktop dialog tool is needed. It only lists what the sandbox allows, never the board's data folder, and only answers requests whose `Host` is `localhost`, `127.0.0.1` or `::1`.

### Projects

Cards are grouped into projects: a named board with a root folder. Pick what to show with the project button in the header: one project looks like the plain board, several show one swimlane per project (collapsible), each with its own Clear and Start all. The selection is remembered and lives in the URL (`#p=1,3`), so a view can be bookmarked. "New project…" asks for a name and root folder; new cards in a project start with that root folder, which you can still change per card. A project can only be deleted when it has no cards. Existing boards are migrated on first start: one project per distinct root folder. Over HTTP see `/api/projects` and `?project=<id>`; over MCP see `list_projects` and the `project`/`project_id` arguments.

### Using the board to improve the board

The web UI is compiled into the binary, so changes an agent makes to this folder (Go code or anything under `frontend/`) only show up after a rebuild. Restarting the old binary is not enough. Use `maestrello restart`, which rebuilds from the folder the binary was built from (or `-src <folder>`). On start, `maestrello` warns when that source is newer than the binary.

Stopping a board ends its agent sessions. A card that was working goes to Failed (send it back to continue the same session). A card waiting on your answer stays in Needs input, and answering it after the next start resumes the session.

## Layout

- `cmd/maestrello`: the `main` package, a thin call into `internal/cli`.
- `internal/cli`: the command line (`start`, `stop`, `restart`, `mcp`, ...), rebuild and the stdio MCP bridge.
- `internal/board`: the board itself: card lifecycle, runner, leases, review, HTTP API and MCP endpoints.
- `internal/model`, `internal/store`, `internal/settings`: the data types, the SQLite store, and layered settings resolution and validation.
- `internal/harness`: the agent CLI adapters (Claude Code, opencode, Mistral Vibe). `internal/gitops`: git and `gh`. `internal/sandbox`: the path guard. `internal/textutil`: small string helpers.
- `internal/testenv`: what the tests share: the `testdata/` fixtures, scratch dirs and fake agent CLIs.
- `frontend/`: the web UI (plain ES modules, no build step), embedded into the binary.

## Develop and test (sandbox only)

Never point a dev build at real repos. Sandbox mode refuses any path outside the sandbox root, allows only local remotes inside it, and stubs out `gh pr create`.

```bash
./testdata/setup.sh                 # fake repos in testdata/project, bare origins in testdata/origins
go run ./cmd/maestrello start -sandbox-root testdata  # data in testdata/work; use testdata/project as the root folder
```

To exercise the board without a model, use the fake CLI (`testdata/fake-claude.sh`, see its header for the keywords it reacts to):

```bash
go run ./cmd/maestrello -sandbox-root testdata -data testdata/work-fake -claude testdata/fake-claude.sh -opencode testdata/fake-opencode.sh -mistral testdata/fake-mistral.sh
```

`testdata/fake-opencode.sh` does the same for the opencode harness (a fake `--version`, `models`, and `run --format json`); `testdata/fake-mistral.sh` does it for the mistral harness (`--version` and `-p --output streaming|text`).

Its `PLAN` keyword makes a plan card propose two tasks, and `CHAT` (or any prompt of a chat card) makes it reply and end its turn.

`go test ./...` runs the unit tests.
