# Maestrello — spec v0.1

A local kanban board where each card is a task carried out by a headless Claude Code session: a coding task by default, or one of three non-coding kinds (research, plan, chat; see *Card kinds*). Single user, runs on localhost. I create and review cards; agents do the work in between.

## Goals and non-goals

**Goals**
- Create a card, have a model refine it into a self-contained prompt, press Run, review the result.
- Changes may span many git repos under one folder.
- All work happens in per-card git worktrees; original checkouts are never modified.
- I decide whether anything gets pushed.

**Non-goals (v1)**
- Multiple users, auth, remote hosting.
- Agents creating or moving cards on their own (beyond their own card's status reports). A plan card proposes tasks, but the board creates the cards, and only when I approve the plan. The board-level `/mcp` endpoint is for me and my own harnesses, not for card agents.
- Pull-based scheduling. The board pushes work to agents. (The one exception is local work: see *Local-folder leases*.)
- Concurrency caps. Every card I run starts immediately, unless it needs a local folder another card is editing.

## Stack

- **Go** single binary: HTTP server, SQLite (`modernc.org/sqlite`, no cgo), embedded static frontend.
- **Live updates** via Server-Sent Events.
- **Agents** run as child processes of the `claude` CLI in headless mode, using my normal Claude login.
- **Board MCP server**: served by the same Go binary over HTTP on localhost; each agent gets it via `--mcp-config`.

## Card lifecycle

```
Todo ──refine──▶ Refining ──▶ Ready ──Run──▶ Running ──▶ Review ──approve/discard──▶ Done
                   │                          │  ▲
                   ▼                          ▼  │ answer
                 Failed                    Needs input
                                              │
                                              ▼ discard
                                             Done
Refining ──(auto_run)──▶ Running        (refinement succeeded and I opted in for this card)
Todo ──(skip_refinement)──▶ Ready       (no refinement: the card text is the prompt; with auto_run on to Running)
Running ──crash / report_failed──▶ Failed
Review / Failed ──(me) send back with feedback──▶ Running (resumed session)
any column ──(me) drag──▶ any column (manual move, see below)
```

| Column | Meaning | Who moves it out |
|---|---|---|
| Todo | Raw card I wrote | Me (Refine); System at once for a card that skips refinement |
| Refining | One-shot refinement session running | System |
| Ready | Refined prompt + repo list available, editable | Me (Run, or Start all); System when `auto_run` is set |
| Running | Agent session active, or queued for a local folder another card is editing ("waiting on #N") | System / agent tools |
| Needs input | Waiting on me: question, permission request, turn limit, time limit, usage limit | Me |
| Review | Agent reported done | Me |
| Failed | Crash, non-zero exit, or `report_failed` | Me |
| Done | Approved or discarded; worktrees removed | — (can be deleted from the board) |

Rules:
- Only I move cards backwards. Sending a card back from Review or Failed **resumes the same session** with my feedback as the next user message.
- Agents cannot create cards. A plan agent only proposes tasks (`report_plan`); the board creates one Todo card per task I select when I approve the plan, so agents still never create cards themselves. Card agents only get their own `/mcp/<card-id>` endpoint via `--mcp-config`; the board-level `/mcp` endpoint (see *Board MCP tools (whole board)*) is for me and my own harnesses (Claude Desktop, Claude Code, …) and is never handed to an agent.
- Crash, or refinement failing → Failed.
- **Auto-run:** a card with `auto_run` set does not wait in Ready for review. As soon as its refinement succeeds the board runs it, exactly as if I had pressed Run (it passes through Ready for an instant). This is a board transition I opt into per card; agents still cannot move cards. A failed refinement goes to Failed and never runs. If Run refuses the card, the refusal is logged on the card and it stays in Ready. Nothing is started while the board is shutting down.
- Turn limit, wall-clock limit and usage-limit (rate limit) stops → **Needs input**, with the actions *continue/retry* or *discard*.

Manual moves: I can drag a card from any column to any other column (`POST /api/cards/<id>/move` with `{"status": …}`). The arrows above are the agent/system paths. A manual move overrides them, but it does not change them:
- A card in Refining can't be moved until refinement ends. A card with a live session (normally in Running) is not moved silently: the plain move is refused. The board asks me to confirm ("The agent is still running. Stop it and move the card to …?"); cancelling leaves the card where it is. On confirmation the board sends `{"status": …, "stop": true}`, which stops the session (as Stop does), waits until the run has ended so its Failed status is settled, and only then applies the move, so the dragged-to column wins. Moves that would be refused anyway (Refining) or are no-ops never kill the agent. Without a live session `stop` changes nothing.
- Group moves: Shift+click toggles cards in a selection (Escape or a click on empty board space clears it). Dragging a selected card drags the whole selection; dragging an unselected card moves only that card. A group move is exactly the same as moving each card individually (one `POST /api/cards/<id>/move` per card, in board order, skipping cards already in the target column), with a single confirm naming the cards that have live agents and a single alert summarising failures as `#id: message`. Cards that moved stay moved when others are refused.
- **→ Done** keeps the work: leftovers are committed, each repo's branch is kept locally (never pushed, whatever `push_default` says; repos without changes are dropped), and the worktrees are removed. This works from Needs input, Review, Failed, or any other column. Dragging a card back out of Done reopens it: each repo gets its worktree back on the kept branch (or a fresh branch from the current base if the branch was deleted), local-work folders are claimed again, and Running resumes the session.
- **→ Refining** starts refinement (same rules as Refine, including auto-run). **→ Running** does not need a refined prompt. It runs the card fresh if it never had a session, and otherwise resumes the session with a "continue" message.
- **→ Ready** does not need a refined prompt either: refining is optional, and a card without one gets the card text (as for *Skipping refinement*) as its prompt, so the agent works with the context available. Send back works the same way. Other targets only change the status.
- A Todo card with `skip_refinement` set needs no refined prompt for **→ Ready** or **→ Running**: the board first skips its refinement (see *Skipping refinement*), keeping a prompt the card already has. Run and Start all treat such a card the same way. Cards without the flag get the same prompt fallback, but are not otherwise skipped.
- Leaving Needs input closes its open question/limit input.

## Card fields

| Field | Notes |
|---|---|
| title, description | What I write. Either may be left out, not both ("a title or description is required"). |
| title_auto | True when the title was derived from the text rather than typed (`deriveTitle`: first non-blank line, markdown markers dropped, cut at ~60 runes on a word boundary with "…"). A derived title is re-derived when the description of an auto card changes, and replaced by the short title the refinement session returns (≤ 80 runes). Sending a different title clears the flag, so a title I typed is never overwritten; sending the same title again keeps it. Existing cards migrate with false. |
| root_folder | The folder I'd open a normal session in. Repos are found under it. |
| project_id | The project (board) the card belongs to; every card has exactly one. See *Projects*. Editable in Todo and Ready like the other fields; it never changes how `root_folder` is used. |
| kind | `code` (default), `research`, `plan` or `chat`; see *Card kinds*. Validated in `cardInput.apply` (shared by HTTP and MCP): anything else is refused with `invalid kind`, and `""` means `code`. Editable in Todo and Ready like the other fields. Setting `chat` forces `skip_refinement`. Stored in `cards.kind` (`TEXT NOT NULL DEFAULT 'code'`, added with an idempotent `ALTER TABLE`; existing rows become `code`). There is no default-kind setting: code is the default. |
| tools | Toolbox ids switched on for this card (`[]string`, see *Toolbox*). Validated in `cardInput.apply`: an unknown id is refused with `unknown tool <id>`, duplicates are dropped. Left out on create, the card is seeded from `Settings.default_tools` for its kind (plan-approved cards too); given, also as `[]`, it wins. Editable in Todo and Ready like the other fields; accepted by MCP `create_card` / `update_card`. Stored in `cards.tools` (`TEXT NOT NULL DEFAULT ''`, comma-separated ids, added with an idempotent `ALTER TABLE`; existing rows have none). |
| context | Additional read-only reference material for the agent: folders and files outside the workspace. Stored as JSON `[{path,type}]` (`type` is `dir` or `file`) in the `cards.context` column (`""` for none; the API always returns a list, `[]` when empty). API and MCP input is `context: string[]` of paths; on edit the list replaces the current one and `[]` clears it. Validated by `normalizeContext` (shared by HTTP and MCP; run by `CreateCard` and `EditCard` before anything is applied): each path is trimmed and cleaned, must be non-empty and absolute, must pass the sandbox guard (a file's parent folder too) and exist, and must not be inside the board's data directory; duplicates are dropped; the type is taken from the file system; at most 20 items (`maxContextItems`, "at most 20 context items"). Editable in Todo and Ready like the other fields; accepted by MCP `create_card` and `update_card`. Ignored for chat cards (they get no context in their session or refinement). See *Refinement*, *Running a card* and *Harness adapters*. |
| plan_tasks | JSON list of `{title, description, kind?, repos?}` a plan card proposed with `report_plan` (`cards.plan_tasks`, empty for other kinds). |
| restrict_refinement | Obsolete and ignored: refinement never reads code. The column and JSON field stay so old data and clients keep working; the UI no longer offers it. |
| auto_run | Checkbox, default from the settings (off unless changed). If set, the card is run as soon as refinement succeeds, skipping review in Ready. Editable in Todo and Ready. |
| skip_refinement | Checkbox, default from the `default_skip_refinement` setting (off unless changed). If set, the card needs no refinement: see *Skipping refinement*. Can be set when creating the card or later while it is in Todo; in Ready it can no longer change. Asking for refinement (Refine, or a move to Refining) clears it, because the prompt is then a refined one. |
| harness | The coding-agent CLI that runs the card's sessions. Default `claude`; the other adapters are `opencode`, `mistral` and `omp`. Seeded from the `harness` setting; `""` means `claude`. An unknown name is rejected (`unknown harness`). Effort and permission mode are validated by the chosen harness. |
| model | Default `sonnet`. Refinement model separately configurable (default `sonnet`, `haiku` allowed). The UI offers these as a dropdown fed by `GET /api/models`: the CLI has no model-listing command, so the server returns a static list (aliases `sonnet`/`opus`/`haiku` plus full IDs); a card's saved model that is not in the list stays selectable. |
| auto_model | Per card, default from `Settings.auto_model` (off). Replaces this manual model: see *Automatic model selection*. |
| effort | `low` / `medium` / `high` / `xhigh` / `max`. Default `medium`. |
| permission_mode | `auto` (default), `acceptEdits`, `manual`, `dontAsk`, `plan`, `bypassPermissions` |
| max_turns | Default 50. Enforced by the board. |
| max_minutes | Default 45. Enforced by the board. |
| push_default | Optional: `merge` / `local` / `push` / `pr`. If unset, decided at review time. |
| refined_prompt | Output of refinement, editable in Ready |
| repos | Repo list from refinement, editable in Ready; agent may extend it via `add_repo` |
| session_id | Claude session UUID (set by the board with `--session-id`) |
| waiting_on | Card id whose lease on a local folder this card is queued behind; 0 when not waiting |
| status, timestamps, turns used, tokens/cost (informational) | |

## Projects

A project is a named board with a root folder. It only organises cards and seeds the root folder of new ones: refinement, runs, worktrees, local work and leases keep using the card's own `root_folder`.

- Fields: `id`, `name` (required, unique), `root_folder` (absolute, cleaned, must pass the sandbox guard), `created_at`, `updated_at`.
- `GET /api/projects` lists them (id ascending), each with `counts` (cards per status). `POST /api/projects` `{name, root_folder}` creates one (400 for a missing or duplicate name, a relative root or a root outside the sandbox). `PATCH /api/projects/<id>` renames it or changes its root folder; a changed root folder only affects new cards, existing cards keep their own. `DELETE /api/projects/<id>` is refused with 400 while the project has cards. SSE: `project` (created or updated; payload is the project) and `project_deleted` (`{"id"}`).
- `GET /api/cards?project=<id>` lists one project's cards; without `project` all cards are returned. `DELETE /api/cards?status=done` and `POST /api/cards/run?status=ready` and `POST /api/cards/refine?status=todo` take the same optional `&project=<id>` and then touch only that project's cards; without it they act on every project as before.
- Creating a card (`project_id` in the input): an unknown project is a 400. With `project_id` and no `root_folder` the card gets the project's root folder. With neither, the project whose root folder equals the card's root (the `default_root_folder` setting when none is given) is used, and created, named after the folder, when it does not exist; so clients that know nothing of projects keep working. `PATCH` can move a Todo or Ready card to another project (its root folder stays unless one is given).
- Migration on open (idempotent): cards with `project_id` 0 get a project per distinct `root_folder`, named after the folder's base name (` (2)`, ` (3)` … when taken). Nothing else about the cards changes.

## Toolbox

A fixed, harness-neutral catalog of optional agent tools (`internal/model/toolbox.go`), switched on per card with the `tools` field. Each entry has an id, a label, a description and the harnesses that support it (each adapter maps the id to its own tool name):

| id | claude | opencode | mistral | omp |
|---|---|---|---|---|
| `web_search` | `WebSearch` | `websearch` | not supported | not supported |
| `web_fetch` | `WebFetch` | `webfetch` | not supported | not supported |
| `skills` | `Skill` | not supported | not supported | not supported |

- `GET /api/toolbox` returns the catalog: `[{"id", "label", "description", "harnesses"}]`.
- A tool only ever fetches information: no toolbox entry gives a chat or read-only card file-write or bash access.
- Defaults: `Settings.default_tools` maps kind → ids and seeds new cards of that kind (see *Settings*). An empty or missing map means no extras, so old databases behave exactly as before.
- With an empty toolbox every adapter produces exactly the arguments and config it did before the toolbox existed.

## Card kinds

Every kind runs through the same lifecycle, per-card MCP endpoint and UI. The kind decides what the agent may touch and how its work ends.

| kind | Agent | Repo access | How it ends |
|---|---|---|---|
| `code` | What every card did before kinds existed | Worktrees / local work, leases, `add_repo` | `report_done` → Review (diffs) → Approve / Discard |
| `research` | Gathers information about the codebase under the root folder | Read-only: `--tools "Read,Grep,Glob"` (plus the card's toolbox tools). No worktrees, no local work, no leases, no `add_repo`. The session's working directory is the root folder | `report_done(summary)` → Review shows the summary as the **Report**. Approve → Done |
| `plan` | Writes a plan and splits it into tasks | Read-only, as research | `report_plan(plan, tasks[])` → Review shows the plan and a checklist of the tasks. **Approve creates one card per selected task**. Send back with feedback revises the plan |
| `chat` | Pure conversation | No built-in tools (`--tools ""`) apart from the board's MCP tools and the card's toolbox tools (e.g. `--tools "WebSearch"`). Working directory: the card's (empty) work folder | Each reply ends the turn → Needs input showing the reply; my answer resumes the same session. I close it by moving it to Done (or Discard) |

- **Code cards** are unchanged: same commands, transitions and review. The claude adapter's arguments for a code card are identical to before kinds existed.
- **Read-only kinds** never create worktrees, branches or leases and never edit under the root folder: `Run` skips `claimLocal`, `checkoutRepo` and the "plan `.` automatically" rule. The originals guard (`checkOriginals`) still runs after the session. `add_repo` is refused with a tool error. Planned `repos` (from refinement or the request) are informational only.
- **Refinement** uses kind-specific instructions: research sharpens the question and names the areas to look at; plan states the scope and the expected task breakdown. **Chat cards never refine**: they behave as `skip_refinement` (the card text is the first message) and refining one is refused.
- **Plan approval:** `report_plan(plan, tasks)` stores the plan text in `summary` and the tasks in `plan_tasks`, and moves the card to Review. Each task is `{title, description, kind?, repos?}`; `kind` is `code` (default), `research` or `plan` (not `chat`), and `repos` are validated like planned repos, so a bad task is a tool error the agent can fix. Approve takes the indices (from 0) of the tasks to create: absent means all, an empty list none (`POST /api/cards/<id>/approve` `{"tasks": [0, 2]}`, or `tasks` of `approve_card`). The board then creates each card through `CreateCard` with the plan card's `project_id` and `root_folder` and everything else from the settings defaults, except that the new cards never skip refinement or auto-run, so they all land in **Todo**. Every selection is checked before anything is created. Each new card gets the note "created from plan #N" and the plan card gets "plan approved: created #a, #b"; then the plan card → Done. Dragging a plan card to Done, or Discard, creates nothing. Approving a research or chat card just closes it.
- **Chat turns:** when a chat session exits cleanly without an error and without a report, `finish` stores an input of kind `chat` whose payload is `{"text": <the agent's last message>}` and moves the card to Needs input. Answering it (`answer`, or `answer_input`) resumes the session with the answer as the next user message (`--resume`). Moving the card to Done closes it (the open input is answered "moved by …"). Errors, limits and rate limits are handled exactly as for other kinds, so a crash is still Failed.

## Automatic model selection

`Card.auto_model` (a column, default `false`; settable on create/edit, in the card forms and on the MCP tools) turns the manual model off for that card. `Settings.auto_model` (default `false`, `PUT /api/settings`) only seeds it for new cards. Code: `internal/board/automodel.go`. Off: no code path of this feature runs; the refiner prompt and schema are unchanged.

On: `refine` builds the candidates (`modelCandidates`): every model of every *available* harness (claude's `knownModels`, other adapters' listed models) that has metadata in the catalog, the built-in `defaultModelInfo()` overridden/extended by `Settings.model_info` (`"<harness>/<model>"` → `{tier 1-5, cost >= 0}`; cost is a relative unit, 0 = free; validated on PUT). The prompt lists them and the schema adds `complexity` (1-5), `model`, `harness`, `model_reason`. The instructions ask for the smallest, cheapest model that does the task well, weighing capability against cost, preferring a free or cheaper adapter when capable enough.

`resolveModel` validates the answer: a listed (harness, model) is used; an unknown/unavailable one falls back to `pickModel(complexity)` (cheapest candidate with tier >= complexity, else the most capable) when the task was rated; otherwise nothing is chosen and the card keeps its manual model and harness. `useModel` stores the choice on the card (`model`, `harness`; effort and permission mode are reset to values the new adapter accepts when it differs), so the launch path (`startSession`) needs no change. Every outcome is logged as a board event ("automatic model selection: ..." with the reason). Cards that skip refinement, and cards refined before the option was on, use their manual model. The UI disables the card's model select while its option is on and edits `model_info` as JSON in Settings.

## Refinement

One-shot, headless:

```
claude -p --output-format json \
  --model <refine_model> \
  --permission-mode plan \
  --tools "" \                       # refinement never reads code
  --max-turns 4 \
  --json-schema '<schema>' \
  "<refinement instructions + card text>"
```
Working directory: the board's data dir, not the root folder. Refinement only clarifies the card text; it does not explore code (the run agent does that). The prompt lists the root folder and the repos under it (`DiscoverRepos` plus `.`) so the refiner can plan repos by name; planned repos not on that list are dropped with a note. The opencode and mistral adapters have no turn cap.

When the card has `context`, the refinement prompt lists the paths ("Additional context (read-only reference):", each with its type). The default refinement sees the names only and cannot read them. A deep refinement (`deep_refinement`, which runs from the root folder with `Read,Grep,Glob`) also gets the context folders through `RefineSpec.ExtraDirs` (`contextDirs`: each dir and the parent of each file, minus anything already under the root); see *Harness adapters*.

Structured output:
```json
{ "refined_prompt": "string", "repos": ["relative/path/to/repo", "..."], "notes": "assumptions, open questions" }
```

The refined prompt must work for a fresh session with no other context: goal, repos (by name), constraints, and a checkable definition of done. Card goes to Ready; I can edit the prompt and repo list before Run. With `auto_run` set, the board continues straight to Run instead (using the card as saved at that moment, so an edit made while refining counts), and notes "refined; starting automatically (auto-run)" in the card's log.

## Skipping refinement

Refinement costs a model session, and a card made by an MCP client (Claude Desktop, Claude Code) usually carries a prompt that is already detailed. A card with `skip_refinement` does without it:

- When such a card is created, or when the flag is set on a card in Todo, its refined prompt becomes the card text (title, a blank line, then the description; just the title when there is no description; just the description when the title is derived, `title_auto`), or the `refined_prompt` that came with the request. No repos are planned, unless the request names `repos`, so the run uses the board's usual rules: a plain root folder is worked on in place as local work, otherwise the agent adds repos with `add_repo`. The card goes straight to Ready, with a card note "refinement skipped". No refinement session runs.
- With `auto_run` set, it then runs at once, as if Run had been pressed (note "refinement skipped; starting automatically (auto-run)"). If Run refuses it, the refusal is logged and the card stays in Ready.
- A Todo card that has the flag without having been promoted (for example dragged back to Todo) is promoted the same way by a move to Ready or Running, by Run, and by Start all. Such a move never auto-runs: the move is the instruction.
- `refined_prompt` given on create (HTTP or MCP) implies `skip_refinement` unless the request sets it to false. Given `repos` are validated like refinement's: paths relative to the root folder, chosen from the repos under it, `.` for the root folder itself. Unlike refinement, which drops unknown paths with a note, an unknown path refuses the request and no card is created.
- The flag stays on the card, and the board shows "unrefined" in its meta line.

## Workspace and worktrees

Per card: `~/.maestrello/work/<card-id>/`

For each repo in the card's list, and again on each `add_repo`:
1. `git -C <repo> fetch origin`
2. Resolve the default branch (`origin/HEAD`).
3. `git -C <repo> worktree add -b maestrello/<card-id>-<slug> ~/.maestrello/work/<card-id>/<repo-rel-path> origin/<default>`

The agent's working directory is the workspace folder. Original repos under `root_folder` are added read-only by instruction (`--add-dir <root_folder>`, so it can still read code it hasn't checked out). The card's context adds more `--add-dir`s, one per context folder, and for a context file its parent directory (the CLI has no flag that grants a single file outside the working dirs); folders already under `root_folder` and duplicates are skipped. Context is read-only by instruction only (the agent is told to read just the listed paths and never change anything there); the board does not enforce it. Context paths that no longer exist at session start are skipped with a card note.

**Guard:** before Run, the board records `git status --porcelain` for each original repo. After each session ends, it checks again; any new change in an original checkout flags the card with a visible warning.

**Cleanup:** on approve and on discard, `git worktree remove` for every worktree on the card. Discard also deletes the branches. Approve keeps branches (they're the deliverable).

**Local work (folders that are not git repos):** a planned path or `add_repo` path that is not a git repo is local work. There is no worktree, branch, commit, push or PR. The agent edits the folder in place and is told not to run git there. `.` means the root folder itself, and the refiner may plan it. When a card has no repos planned and its root folder is not a git repo and contains none, the board plans `.` automatically. A folder that holds repos is never turned into local work implicitly. Review lists the files modified in place since the card started using the folder (by mtime, skipping hidden dirs, dependency dirs and nested repos). Approve and discard both leave the files as they are, because nothing can be undone without git. The board's own data folder is never local work.

**Local-folder leases:** local work is edited in place, so only one card at a time may work a given local folder. A card holds a *lease* on each local folder while its session is live.
- **Lease file:** one JSON file per leased folder in `<data dir>/leases/` (named by a hash of the folder path, never inside the folder): folder, card id, session id, board process id and pid, acquired-at, expires-at. Written atomically (temp file, then an exclusive link, or a rename to renew).
- **Conflicts:** two folders conflict when one is the other or lies inside it, so a lease on `.` blocks a plain folder under it and the other way round. Git repos use per-card worktrees and take no lease.
- **When:** a card takes its leases before its session starts (Run, and every resume: send back, continue, answer after the session ended) and before `LocalSince` is set, so its review never includes an earlier card's edits. `add_repo` of a local folder takes the lease on the spot.
- **Queue:** a card whose folder is leased by another card does not fail. It stays in Running with `waiting_on` set and the activity "waiting on card #N (title)"; the board shows a "waiting on #N" bubble instead of the spinner. Waiting cards are served first in, first out: a folder an earlier card waits for is reserved for it. When a lease is released, the next card takes it and starts on its own; the queue is also re-checked periodically, so an expired lease frees it too.
- **Cancel:** Stop on a waiting card takes it out of the queue and fails it; Move and Discard take it out of the queue and then act as usual (a card that never had a session rests in Ready, one that had a session in Failed, before the move applies). None of them start a session.
- **add_repo** of a folder leased by another card returns a tool error naming the holder and telling the agent to retry later, or to use `ask_user` / `report_failed`. It never blocks.
- **TTL and heartbeat:** a lease lasts 30 minutes past the session's last sign of life: a stream event, or waiting on me in `ask_user`/permission. A heartbeat (every minute) writes the new expiry. So a lease outlasts any real run but frees within one TTL if the board dies or the agent hangs. A lease that has expired, whose card is no longer running, or whose board process is gone counts as free; the card that takes it over logs a note, and a hung session of this board that loses its lease is stopped and fails.
- **Release:** when the session ends (any way), and on Stop, Move, Discard, Approve and failure. Release is idempotent. Review, Failed and Needs-input cards whose session has ended hold no lease: only a live session edits the folder.
- **Restart:** at startup the board drops stale lease files, and cards that were Running (including waiting ones) fail as before, so nothing stays locked out.

## Running a card

```
claude -p \
  --input-format stream-json --output-format stream-json --verbose \
  --session-id <uuid>            # first run; later runs use --resume <uuid>
  --model <model> --effort <effort> \
  --permission-mode <mode> \
  --permission-prompt-tool mcp__maestrello__permission \
  --mcp-config <generated json pointing at http://127.0.0.1:<port>/mcp/<card-id>> \
  --add-dir <root_folder> \
  --add-dir <context folder or parent of a context file> ... \   # one per context dir, if any
  --append-system-prompt "<board instructions>"
```

Stdin gets the refined prompt as the first user message. Every stream event is stored and shown live on the card.

Research and plan cards add `--tools "Read,Grep,Glob"` and run in the root folder; chat cards add `--tools ""`, omit `--add-dir` and run in their work folder. **Toolbox tools:** each tool of the card is appended to that `--tools` list by its claude name (chat `--tools "WebSearch"`, research/plan `--tools "Read,Grep,Glob,WebSearch"`; code has no `--tools` flag and keeps its full toolset) and to `--allowedTools`, so it runs without a permission prompt. With none, the arguments are byte-identical to the above. `--allowedTools` covers the board tools the kind uses: `report_done`, `report_failed`, `ask_user` for research; `report_plan` instead of `report_done` for plan; `report_failed` and `ask_user` for chat; code keeps the original list with `add_repo`. See *Card kinds*; the adapter reads the kind from `SessionSpec.Card`.

**Board instructions** (appended system prompt; a chat card that has toolbox tools is told what it can do with them ("You can search the web with WebSearch; you cannot read or change any files or run commands") instead of "You have no tools", and read-only kinds get a note naming theirs; for research and plan cards read-only instructions that end with `report_done` or `report_plan`, for chat "answer and end your turn") tell a code agent: work only inside the workspace; originals are read-only; use `add_repo` for repos it needs that aren't checked out; commit its work on the card branch in each repo; finish by calling `report_done` or `report_failed`; ask through `ask_user` instead of guessing.

**Limits** are enforced by the board, which counts assistant turns and wall-clock time from the stream. When a limit is hit, the board stops the process and moves the card to Needs input. *Continue* resumes the session with a "continue" message and a fresh budget.

**Usage limit:** a rate-limit error in the stream (or exit) moves the card to Needs input with *retry* / *discard*, showing the reset time when the CLI reports one.

**Exit without report:** if the process exits without calling `report_done`, `report_failed` or `ask_user`, the card goes to Failed with the tail of the log.

## Harness adapters

Everything specific to one agent CLI sits behind the `Harness` interface (`internal/harness/harness.go`); there are four adapters: Claude Code (`internal/harness/harness_claude.go`), opencode (`internal/harness/harness_opencode.go`), Mistral Vibe (`internal/harness/harness_mistral.go`) and Oh My Pi (`internal/harness/harness_omp.go`). The board keeps the run bookkeeping (turns, limits, cost, `resultErr`/`rateLimit`, card transitions, `finish`); a harness only translates. Observable behaviour of the `claude` adapter is exactly what "Running a card" and "Refinement" describe.

**What an adapter provides**
- `Name()` and `Caps()` (below), registered in the `harnesses` map; `harnessFor(name)` resolves a name (`""` is `claude`, unknown names are an error).
- `SessionCommand(SessionSpec)`: the process for one session: arguments, environment, working directory, the prompt on stdin, and the MCP config file it needs (written at `MCPConfigPath`, so the board can remove it later). `SessionSpec` carries the card (model, effort, permission mode, session id, toolbox tools), prompt, board instructions, MCP URL, working directory, root folder, whether to resume, whether MCP is strict and `Context` (the card's existing context items, `[]ContextItem{Path, Type}`; empty for chat). `RefineSpec` carries model, effort, prompt, working directory, tools, turn cap, schema and `ExtraDirs` (extra read-only folders for a deep refinement with context). The board sets the process group and stderr and starts it. Each adapter has its own binary path (`HarnessConfig.Bin`; the flags `-claude`, `-opencode`, `-mistral` and `-omp`, `App.claudeBin` / `App.opencodeBin` / `App.mistralBin` / `App.ompBin`), read at call time through `App.harness`.
- `ParseEvent(line)`: one stdout line as a `HarnessEvent`: `Kind` (stored as the event's type), the assistant turn (`TurnID`, `Activity`), the final result (`CostUSD`, `IsError`/`ResultError`), `RateLimited` + message and `SessionID` (for a CLI that makes its own session ids: the runner saves it on the card). An error makes the board note `unparsed output: ...` and go on. Usage limits are not part of the event: the runner passes every line to `ParseUsage` as well (below).
- `RefineCommand` and `ParseRefineResult`: the one-shot, read-only refinement (model, effort, tools, JSON schema) and its output (`Structured` JSON, text, cost, is-error).
- `ValidateEffort` and `ValidatePermissionMode`; `Caps.Efforts` / `Caps.PermissionModes` list what is allowed.
- `Account(ctx)`: who the CLI is logged in as, for the header, as `(*Account, error)` with `Account{LoggedIn, Email, OrgName, AuthMethod, APIProvider, SubscriptionType}`. It runs the CLI (using the adapter's binary and environment) and stops when `ctx` ends; the board sets the 10s `accountTimeout` and never calls it per request. A logged-out CLI is an `Account` with `LoggedIn` false, not an error; a broken, missing or unreadable CLI is an error. An adapter without an account lookup returns `ErrNoAccount`.
- `ParseUsage(line)`: reads usage-limit updates out of one stdout line as `[]UsageLimit` (`Type` = the window name, `Status`, `ResetsAt`, `Utilization`, `SurpassedThreshold`, overage fields), `nil` when the line has none. An adapter fills only the fields the line reports; the board merges the update into the latest limit it has for that window and stamps `ObservedAt`. It decides nothing about the run: only `HarnessEvent.RateLimited` stops one.

**Card context in sessions and refinement.** A card's `Context` items (read-only dir/file paths) reach the agent in code, research and plan sessions (never chat) and in refinement. `startSession` keeps only existing paths (a missing one is skipped with a card note) and passes them as `SessionSpec.Context`; `contextNote` adds a READ-ONLY, outside-the-workspace, never-edit paragraph to the instructions. Claude spike finding: `--add-dir <directories...>` is variadic, so the adapter repeats the flag once per directory; there is no non-prompting flag to grant read of a single file outside the working dirs, so a context file's parent dir is added via `--add-dir` and the prompt tells the agent to read only the listed files. Dirs already under the root are skipped and duplicates dropped. opencode sessions need no flag (`external_directory` is already allowed for non-chat kinds). Refinement lists the context paths in its prompt; only deep refinement gets access, via `RefineSpec.ExtraDirs` (claude: `--add-dir`; opencode: `external_directory` allow, edit/bash/webfetch stay denied). Cards created by approving a plan inherit the plan card's context.

**The `harness` setting and card field.** `Settings.harness` (default `claude`) seeds new cards and picks the harness that runs refinement (refinement model and effort are settings, so they are the settings harness's). `Card.harness` picks the harness for the card's sessions. Both are validated against the registry; an empty value means `claude`, so old databases (column `harness`, default `claude`) and the web UI's settings PUT (which leaves it out) keep working. The UI shows it and has selectors for it (see *Web UI*). `GET /api/harnesses` lists the registered harnesses, sorted by name, as `[{"name", "efforts", "permission_modes", "available", "version", "error", "models"}]`: efforts and permission modes come from each `Caps`; `available`/`version`/`error` are the cached availability (below); `models` is claude's `knownModels` and, for opencode, the `provider/model` lines of `opencode models` (empty when opencode is unavailable). `?refresh=1` re-checks every adapter first. The UI fills its harness, model, effort and permission-mode selects from it. Over HTTP an unknown `harness` on `POST`/`PATCH /api/cards` or `PUT /api/settings` is a 400.

**Availability.** A harness is *available* when its binary resolves (`exec.LookPath` for a bare name, an existing file for a path) and `<bin> --version` exits 0 within 5 seconds. `refreshAvailability` (`internal/board/harness_avail.go`) checks every adapter in the background at startup and on `GET /api/harnesses?refresh=1`, keeps the result (version line or error text, plus opencode's model list) in memory and publishes an SSE `harnesses` event; requests only read the cache, never run the CLI. A harness with no cached result for its current binary (the path is read at call time) is checked once on first use. Starting a session or a refinement on an unavailable harness fails the card with `harness <name> is not available: <reason>` and starts no process. Creating or editing cards and settings on an unavailable harness is still allowed (validation is registry-based). The header's account and usage data are per harness through `Account` and `ParseUsage`, not part of availability.

**Capabilities and fallbacks** (`Caps`)
- `Resume` false: a resume starts a fresh session with the refined prompt plus the user's message, as for a card that never got a session.
- `PermissionCallback` false: the harness cannot ask the board's `permission` tool, so the card's permission mode is used as it is and no `permission` inputs appear.
- `StructuredOutput` false: the adapter's `ParseRefineResult` reads the refinement-schema JSON from the final text.
- `PerTurnEvents` false: the turn limit cannot be enforced (the board notes it at the start); only the time limit is. Cost and activity update only at the end.
- `Account` false: the adapter has no account lookup; the board never runs one for it, and the header shows "account unknown" for it (a harness with neither `Account` nor `Usage` is left out of the header). claude, mistral and omp have it; opencode does not (`Account` returns `ErrNoAccount`). mistral has no auth command to run: it reads `MISTRAL_API_KEY` / `<vibe home>/.env` and Vibe's `whoami_cache.json` (plan name), reports logged in when either exists, never returns the key and shows no email. omp runs `omp usage --json` (the accounts it has logins or keys for) and falls back to checking whether a provider key variable is set; only provider and email (if listed) are shown, never a token.
- `Usage` false: the adapter reports no usage limits; the board does not call `ParseUsage` for its runs and the header has no usage chips for it. claude has it; opencode, mistral and omp do not (`ParseUsage` returns nil; omp's limits are only reachable through the separate `omp usage` command, not in its stream), so mistral's and omp's header groups show their account status and no usage chips, and the popover says the harness reports no usage limits.

The board itself implements the `Resume` fallback and the `PerTurnEvents` note and checks `Account` and `Usage`; the other two follow from the adapter (it simply omits the permission flag, or reads the text), since the board only reads what `ParseEvent` and `ParseRefineResult` return. A test adapter can be added to the registry with `harness.Register(name, build)`, which returns the function that removes it again.

**The `opencode` adapter** (`internal/harness/harness_opencode.go`) drives `opencode run --format json` (opencode 1.2.x, as installed here; models are `provider/model`, e.g. `ollama/qwen3-coder:30b`, and the board has no say in which providers exist: they come from the user's own opencode config).
- *Caps:* `Resume` true, `PermissionCallback` false, `StructuredOutput` false, `PerTurnEvents` true. `Efforts` is empty (opencode's `--variant` is provider specific), so only `""` is a valid effort; `PermissionModes` are `auto` (edit, bash and webfetch allowed) and `plan` (read-only). `""` is not a valid mode. Settings accept an empty effort for a harness without efforts.
- *Session ids:* opencode cannot start a session under a preset id (it makes `ses_...` itself), so the adapter reports the id from the stream as `HarnessEvent.SessionID` and the runner overwrites `card.session_id` with it. A resume uses `-s <id>`; an id that is not a `ses_...` id (the board's pre-assigned UUID, when the run died before its first event) cannot be resumed (`SessionChecker`), so the board starts fresh with the refined prompt plus the message.
- *Command:* `opencode run --format json --agent maestrello [-m <model>] [-s <id>]`, the prompt on stdin, `OPENCODE_CONFIG=<MCPConfigPath>`. That file holds the board's MCP server (`type: remote`, `url` = the card's MCP URL, long timeout for `ask_user`), an `agent.maestrello` with the tools and permissions of the card kind, and an `instructions` entry pointing at `<MCPConfigPath>.instructions.md` (the board instructions, with `mcp__maestrello__x` rewritten to opencode's `maestrello_x`; this keeps opencode's own system prompt). It is merged over the user's config, so their providers stay.
- *Kinds:* code gets all built-in tools plus `report_done`, `report_failed`, `ask_user`, `add_repo`; research and plan only `read`/`grep`/`glob`/`list` plus their board tools; chat no built-in tools. Board tools of other kinds and `permission` are switched off, everything else (`"*"`) too. Read-only kinds and `plan` mode also set `edit` and `bash` to `deny`, because a non-interactive run cannot answer a permission question: every permission is `allow` or `deny`, never `ask`.
- *Toolbox:* each tool of the card is switched on in the tool switchboard by its opencode name (`websearch`, `webfetch`) and its permission set to `allow`, also in chat and read-only kinds (they fetch, never change). A tool opencode lacks (`skills`) is skipped, and the board writes the card note `tool skills not supported by opencode; skipped` when the session starts.
- *Strict:* opencode has no "only this MCP server" switch, so MCP servers from the user's own opencode config remain in sandboxed runs; the built-in tools stay restricted and `webfetch` is denied, unless the card explicitly selected `web_fetch` (then it is allowed). The claude adapter's toolbox tools are not affected by strict, which only limits MCP servers.
- *Events:* `text` and `tool_use` are assistant turns (turn id = `part.messageID`, activity from the text or `toolActivity` of the tool input), `step_finish` carries the step cost (the board sums it; Ollama is 0), `error` is an error result (a rate-limit message sets `RateLimited`). There is no final result line: the run ends when the process exits.
- *Refinement:* read-only (`edit`/`bash`/`webfetch` denied, only the read tools the refiner asks for), config passed inline in `OPENCODE_CONFIG_CONTENT` with no MCP server of its own. The schema is added to the prompt, and `ParseRefineResult` joins the text parts and takes the JSON object from the text (plain, in a ```json fence, or the outermost `{...}`).
- *Not verified live:* the event shapes follow opencode's documented JSON events and the error line seen from the installed CLI; no full model run was possible while building this, so `testdata/fake-opencode.sh` is modelled on them. The `"*": false` tool wildcard relies on opencode's tool patterns.

**The `mistral` adapter** (`internal/harness/harness_mistral.go`) drives Mistral Vibe (`vibe`) in programmatic mode. It was written from the public Vibe docs (docs.mistral.ai/vibe) with the CLI not installed, so the session and stream parts were not run against the real binary; `Account` was checked against vibe 3.1.2 (no auth or usage command exists, so it reads the key and `whoami_cache.json` from the Vibe home).
- *Caps:* `Resume` **false**, `PermissionCallback` false, `StructuredOutput` false, `PerTurnEvents` true. `Efforts` is empty (Vibe's `thinking` is per model in its config); `PermissionModes` are `auto` and `plan`. A resume (send back, restart) therefore starts fresh with the refined prompt plus the message. Vibe has `--resume <id>`, but the docs do not say where a run reports its session id, so the adapter reports none (`HarnessEvent.SessionID` stays empty) rather than guess.
- *Command:* `vibe -p --output streaming --agent maestrello --trust [--model <alias>] --enabled-tools <name>...`, the prompt on stdin (`-p` without text), `VIBE_HOME=<MCPConfigPath>.vibe`. That per-card home holds `config.toml` (top-level `default_agent`/`enabled_tools`, the board's MCP server as `[[mcp_servers]]` with `transport = "streamable-http"`, `url` = the card's MCP URL and a long `tool_timeout_sec` for `ask_user`, and `[tools.<name>] permission = "always"` for each enabled tool) and `agents/maestrello.toml` (the board instructions as `instructions`, which Vibe appends after its own system prompt; `mcp__maestrello__x` is rewritten to Vibe's `maestrello_x`). The user's `.env` (API key) is symlinked in. A small JSON file is also written at `MCPConfigPath` so the board's cleanup finds it.
- *MCP:* the session reaches the board's MCP server through `[[mcp_servers]]`; MCP tools are named `<server>_<tool>`. Because the home is separate, the user's own `config.toml` (custom models, providers, MCP servers) is **not** loaded: `--model` takes Vibe's built-in aliases/IDs. Strict mode needs no extra step, as no user MCP servers are loaded.
- *Permissions:* programmatic Vibe denies any tool call that would need approval, so the adapter lists exactly the tools of the card kind (`--enabled-tools`) and sets them to `always`. Code gets `read_file`, `grep`, `write_file`, `edit`, `search_replace`, `bash` plus `report_done`, `report_failed`, `ask_user`, `add_repo`; research gets the read tools plus `report_done`/`report_failed`/`ask_user`; plan the read tools plus `report_plan`/`report_failed`/`ask_user`; chat only `report_failed`/`ask_user`. `plan` mode on a code card drops the write tools. Read-only kinds have no write tool at all.
- *Toolbox:* no entry is supported (the docs name no web or skill tools); each is skipped with the card note `tool <id> not supported by mistral; skipped`.
- *Events:* each streamed line is a message (`role`, `content`, `tool_calls[].function.{name,arguments}`). An `assistant` message is a turn (turn id = `message_id`, else a hash of the line); a tool call sets the activity. There is no result line and no cost, so cost stays 0 and a failed run is seen through the exit code and stderr.
- *Refinement:* `vibe -p --output text` with read-only tools (`read_file`, `grep`) and no MCP server, in a temporary `VIBE_HOME`; the schema is added to the prompt and `ParseRefineResult` takes the JSON object from the final text. No turn cap, and no extra read dirs are passed.
- *Not verified live:* the streamed message shape (the docs only say "one JSON object per message"; the OpenAI-style message above is an assumption), the `agents/*.toml` keys, the `--enabled-tools` pattern `re:^$` used to switch every tool off in a refinement without tools, whether `enabled_tools` in the agent/config accepts the `maestrello_*` names, and the built-in names `search_replace`. `testdata/fake-mistral.sh` is modelled on these assumptions.

**The `omp` adapter** (`internal/harness/harness_omp.go`) drives the Oh My Pi CLI (`omp`, a fork of the pi coding agent) in print/JSON mode. `omp` 18.20.0 **was installed** while building this, so the flags below were checked against `omp --help`, the `models`/`usage` commands and probe runs; but no model credentials were available, so **no run reached a model** and the stream shape is from pi's JSON mode and the strings of the binary, not from a live stream (see *Not verified live*).
- *Caps:* `Resume` true, `PermissionCallback` false, `StructuredOutput` false, `PerTurnEvents` true, `Account` true, `Usage` false. `Efforts` are omp's `--thinking` levels `minimal`, `low`, `medium`, `high`, `xhigh`, `max` (`off` and `auto` are left out; `""` keeps omp's default); `PermissionModes` are `auto` and `plan`. `Usage` is false because omp shows provider limits only through the separate `omp usage` command, never in the stream, so the header shows the account without usage chips.
- *Command:* `omp -p --mode json --approval-mode yolo --config <overlay> --append-system-prompt <instructions file> --tools <list> [--model <m>] [--thinking <effort>] [--resume <id>] [--add-dir <root and context dirs>]`, the prompt on stdin (verified: `-p` reads piped stdin), `PI_CODING_AGENT_DIR=<MCPConfigPath>.omp`, `OMP_MCP_TIMEOUT_MS=86400000`. omp has no flag for an MCP config file, so the per-card agent dir mirrors the user's (every entry symlinked: credentials, models, settings, sessions) except `mcp.json`, which holds only the board server (`{"mcpServers": {"maestrello": {"type": "http", "url": <card MCP URL>, "timeout": 86400000}}}`, the long timeout for `ask_user`). The user's own `mcp.json` is therefore not loaded. The `--config` overlay sets `mcp.startupTimeoutMs: 30000` (the default 250 ms is short for tool discovery) and, in Strict mode, `mcp.enableProjectConfig: false`. The instructions go in `<MCPConfigPath>.instructions.md` with `mcp__maestrello__x` rewritten to omp's `mcp__maestrello_x` (omp names MCP tools `mcp__<server>_<tool>`). A small JSON file is also written at `MCPConfigPath` so the board's cleanup finds it.
- *Kinds and permissions:* as `mvTools`: code gets `read`, `grep`, `glob`, `edit`, `write`, `bash` plus `report_done`, `report_failed`, `ask_user`, `add_repo`; research the read tools plus `report_done`/`report_failed`/`ask_user`; plan the read tools plus `report_plan`/`report_failed`/`ask_user`; chat only `report_failed`/`ask_user`; `plan` mode on a code card drops `edit`, `write` and `bash`. The list is given to `--tools` and tool approval is `yolo`, so nothing prompts.
- *Session ids:* omp makes its own, announced in the `session` header line (`HarnessEvent.SessionID`, saved on the card); a resume is `--resume <id>`. `ResumableSession` accepts any non-empty id without whitespace that is not a flag.
- *Toolbox:* no entry is supported (omp's own `web_search` tool is not offered through the toolbox); each is skipped with the card note `tool <id> not supported by omp; skipped`.
- *Events:* a `message_end` with role `assistant` is a turn (turn id = `responseId`, else a hash of the line; text from the `text` blocks, activity from the first `toolCall`); a `turn_end` carries the turn's cost (`message.usage.cost.total`, summed by the board) and, with `stopReason` `error`, the error (`errorMessage`; a rate-limit message sets `RateLimited`). Other events are logged only.
- *Refinement:* `omp -p --no-session --approval-mode yolo` in plain text mode with `--tools read,grep,glob` (or `--no-tools`), `--add-dir` for the deep-refinement context folders and no board MCP server; the schema is added to the prompt and the JSON object is taken from the final text. No turn cap.
- *Not verified live:* the stream shape above (session header, `message_end`, `turn_end`, `usage.cost.total`), that `--tools` also admits the `mcp__maestrello_*` names (the help says "built-in or extension tool names"), that the `--config` overlay accepts the nested `mcp:` keys (`omp config list` shows them as `mcp.startupTimeoutMs`, `mcp.enableProjectConfig`), that `PI_CODING_AGENT_DIR` with symlinked credentials keeps omp logged in (only the MCP load from that dir was observed), the shape of the `omp usage --json` entries with accounts (only the empty `reports: []` form was seen, so provider and email are read loosely), and `--resume` by id. `testdata/fake-omp.sh` is modelled on these assumptions.

## Board MCP tools (per card)

| Tool | Effect |
|---|---|
| `report_done(summary)` | Card → Review. Summary shown on the review screen. |
| `report_failed(reason)` | Card → Failed. |
| `ask_user(question)` | Card → Needs input. The tool call blocks until I answer, or the session is stopped and later resumed with my answer as a user message. |
| `report_plan(plan, tasks)` | Plan cards only: stores the plan and its proposed tasks (`{title, description, kind?, repos?}`), card → Review. Refused on other kinds. |
| `add_repo(path)` | Code cards only (refused on other kinds). Creates a worktree (fetch → branch → worktree) in the workspace; returns its path. A plain folder becomes local work and is leased; if another card holds it, the call fails at once naming the holder. |
| `permission(tool_name, input)` | Target of `--permission-prompt-tool`. Card → Needs input showing the request; I allow or deny. |

The MCP endpoint is per card (`/mcp/<card-id>`), so tools can't touch other cards. The tool list depends on the card's kind: code has `report_done`, `report_failed`, `ask_user`, `add_repo`, `permission`; research drops `add_repo`; plan has `report_plan` instead of `report_done`; chat has `report_failed`, `ask_user` and `permission`. A tool a kind does not offer is also refused if called (`report_done` on a plan card, `report_plan` anywhere but a plan card, `add_repo` on a non-code card), with a clear tool error.

**Waiting on me:** while a tool call is blocking, the process stays alive. If the server restarts, or the wait is long, the process is stopped and the card resumes later with `--resume <session-id>` and my answer. v1 can keep the process alive and fall back to resume only after a restart.

## Board MCP tools (whole board)

A second MCP endpoint, `/mcp` (no card id, same transport as `/mcp/<card-id>`, `serverInfo.name` `maestrello-board`), lets an outside MCP client manage the board the way I do in the browser. Results are text content holding the JSON of the result; errors are `isError: true` with the same messages the HTTP API gives. All rules stay in the shared code (`CreateCard`, `EditCard`, `Move`/`MoveStopping`, `Approve`, …); the MCP layer only maps arguments and errors, and changes reach open browsers through the usual `card`/`event` SSE events. The per-card tools are not on this endpoint, and these tools are not on `/mcp/<card-id>`.

| Tool | Effect |
|---|---|
| `list_projects()` | Projects in id order with root folder and card counts per status, as `GET /api/projects`. |
| `list_cards(status?, project?)` | Cards in board order (id ascending), optionally one column and/or one project (id); compact view (id, title, status, project_id, activity, root_folder, harness, kind, model, auto_run, waiting_on). Unknown status is an error. |
| `get_card(id)` | As `GET /api/cards/<id>`: card, repos, inputs, live; only the last 20 events. |
| `create_card(…)` | As `POST /api/cards`; the title is optional and derived from the description when left out (one of the two is required); optional description, project_id, root_folder, kind, model, effort, permission_mode, max_turns, max_minutes, push_default, auto_run, restrict_refinement (ignored), skip_refinement, refined_prompt, repos, tools (toolbox ids; an unknown id is a tool error), context (array of absolute folder or file paths, max 20, each must exist and be inside the sandbox; on `update_card` it replaces the list and `[]` clears it). Lands in Todo, or in Ready (or Running with auto_run) when it skips refinement: `skip_refinement` set, or a `refined_prompt` handed over. The tool description tells a client it can hand over a finished prompt to save the refinement tokens. |
| `update_card(id, …, project_id?, refined_prompt?, repos?)` | As `PATCH /api/cards/<id>` (Todo and Ready only); `project_id` moves the card to another project. `skip_refinement` set on a Todo card skips its refinement. |
| `move_card(id or ids, status, stop?)` | The drag, for one card or several (in the order given, each reporting its own result). `stop` = `MoveStopping`; set it only after I confirmed. |
| `answer_input(id, text?, allow?, input_id?)` | Answer the card's open input (`input_id` defaults to it). |
| `approve_card(id, actions?, tasks?)` | As Approve (`actions`: repo id → local/push/pr; `tasks`: for a plan card the indices of the tasks to create as cards, all when left out). |
| `discard_card(id)` | As Discard. |
| `send_back_card(id, text)` | As Send back with feedback. |

Notes the board writes for these changes name the origin ("moved to review by you via MCP") instead of just "you". There are no tools to delete cards or run bulk actions on a whole column.

**Stdio bridge:** `maestrello mcp [-addr 127.0.0.1:7878]` reads newline-delimited JSON-RPC from stdin, POSTs each message to `http://<addr>/mcp` and writes the reply as one line to stdout (nothing else goes to stdout). Notifications produce no output. When no board is running, requests get a JSON-RPC error saying to start it. This is how Claude Desktop, which starts MCP servers as commands, reaches the board.

## Review

Per card:
- The agent's summary, a full transcript view, turns/tokens/cost used.
- **Per repo:** diff against the base branch, commit list, and an action: merge into the base branch / keep local / push branch / push + open PR (`gh pr create`). Defaults to the card's `push_default` when set.
- **Merge (`merge`):** in the original checkout (`root_folder/repo_path`) merges the card branch into the branch the base ref points to (`origin/main` → local `main`). It only happens when that checkout is on that branch and has no uncommitted changes to tracked files; otherwise the branch is kept and the result reads "kept local on <branch>: not merged, <reason>". Fast-forward when possible, else a merge commit `maestrello: merge #<id> <title>`. A conflict is aborted (nothing changes), the branch is kept, and the conflict is reported in the result and a card note. After a merge the card branch is deleted and the result is "merged into main". It never pushes. The review UI lists "Merge into <base>" first; `approve_card` and `push_default` accept `merge`.
- **Research:** the summary is shown as the **Report**, with no changes section. Approve → Done.
- **Plan:** the plan, then a checklist of the proposed tasks (all ticked, with title, description, kind and repos), and the approve action is "Create N cards" for the ticked tasks. Send back with feedback revises the plan.
- **Approve all** applies the chosen actions → Done and removes the worktrees.
- **Discard** → Done, removes the worktrees and branches.
- **Send back with feedback** → Running, resuming the session.
- Repos that were checked out but have no changes are dropped silently.

## Folder picker API

`GET /api/fs/list` (`internal/board/fs.go`) lists one directory for the web UI's folder/file picker. It returns names and types only, never file contents.

- **Request** (query): `path` (absolute; when left out the start is the `default_root_folder` setting, else the sandbox root, else the home directory, else `/`), `files=1` (also list regular files; otherwise only folders), `hidden=1` (include names starting with `.`; hidden by default). A relative `path`, a path that is not a directory or cannot be read, a path outside the sandbox and a path inside the board's data directory are each a 400 `{"error": "…"}`.
- **Response:** `{"path": "<cleaned path>", "parent": "<parent dir>", "entries": [{"name", "type": "dir"|"file", "is_repo": bool}], "truncated": bool}`. Folders come first, then files, each sorted by name. `is_repo` is true for a folder that is a git repo (files are always false). `parent` is left out at the file system root and when the parent is outside the sandbox. At most 2000 entries (`fsListCap`) are returned; `truncated` is true when there were more.
- **Guard:** the requested path goes through the same sandbox guard as every other path (`Guard.Check`, after symlinks), and nothing inside the data directory is ever listed. Entries are filtered one by one: a symlink whose target resolves outside the sandbox, an entry in the data directory and anything that cannot be stat'ed are left out.
- **Host check:** the request is refused with 403 (`forbidden: host must be localhost`) unless its `Host` header names `localhost`, `127.0.0.1` or `::1` (with or without a port). This is the DNS-rebinding protection: a page on another origin that resolves to the board's address cannot read the folder listing.

## Web UI layout

The UI needs no build step: `frontend/` is embedded as is (`frontend/embed.go`, `//go:embed index.html *.css *.svg *.png *.ico js`, exposed as `frontend.FS`) and served by `http.FileServerFS`, so `.js` and `.css` get their content types from the extension. `index.html` holds only the markup, three stylesheet links and `<script type="module" src="js/main.js">`. The markup's inline `on*=` handlers find their functions on `window`, which `js/main.js` fills. Read only the file for the area you change.

CSS (linked in this order): `style.css` (variables, header, board, columns, cards, buttons), `modal.css` (the modal/drawer, forms, detail sections, diffs), `account.css` (account chip, popover, usage gauges).

JavaScript (`frontend/js/`, plain ES modules; modules import each other, cycles are fine because only functions cross them at call time):
- `main.js`: startup (info, models, harnesses, toolbox, first load, SSE) and the `window` exports for the inline handlers.
- `state.js`: `COLUMNS`, `MODES`, `EFFORTS` and the state shared everywhere (`cards`, `openId`, `detail`, with `setCards`/`setOpenId`/`setDetail`: an importer cannot assign to a binding).
- `util.js`: `$`, `esc`, `lsGet`/`lsSet`, `select`.
- `api.js`: `api`, `act`, and the live updates: `connect` (SSE), `loadCards`, `scheduleDetailRefresh`.
- `projects.js`: projects, the selection (`sel`, URL hash, `localStorage`), the project popover and new-project form, swimlane folding.
- `board.js`: columns, swimlanes, card rendering, Clear / Start all / Refine all, drag and drop, Shift+click multi-select.
- `kinds.js`: card kinds (`KINDS`, `kindOf`, `kindMeta`). `avatars.js`: `avatarHTML`.
- `drawer.js`: the modal (`openDrawer`, `closeDrawer`, collapsible sections `sec`).
- `picker.js`: the folder/file picker (`pickPath`, `pathField`, `browsePath`).
- `harness.js`: harness and model selects, harness icons, availability re-check. `forms.js`: card editor fields, tools group, new-card form (`openNew`, `createCard`). `settings.js`: the Settings modal.
- `card.js`: card detail (`openCard`, `loadDetail`, `renderDetail`, input panel, event log lines). `review.js`: diffs, save/delete, answer/reply, send back, approve, discard.
- `account.js`: account and usage indicator, popover, gauges.

## UI (v1)

- **Projects:** a project button in the header (before the account chip) shows "All projects", the project name or "N projects" and opens a popover with an "All projects" checkbox, one checkbox per project (name, root folder, card count) and "New project…" (name and root folder in the modal; the new project is then shown on its own). At least one project stays selected; selecting every project is the same as "All projects", which also picks up projects created later. The selection is kept in `localStorage` (`maestrello.projects`) and in the URL hash (`#p=1,3`, or `#p=all`), which wins on load and on `hashchange`, so a view can be bookmarked. One project selected: the board looks as before and its name shows next to the title. Several: one swimlane per project, stacked vertically in id order; each has a header (collapse toggle, name, root folder, card count) above the usual 8 columns, and the collapsed state is kept in `localStorage` (`maestrello.collapsed`). Clear and Start all in a column header act only on that swimlane's project (`&project=<id>`). Drag and Shift+click group moves work as before; dropping on a column of another project's swimlane changes only the status, never the project. New card preselects the first project shown (and its root folder); the card editor has a project `<select>` whose change refills the root folder from that project; the card detail shows the project. `project` and `project_deleted` events update the switcher and board live. No external assets, inline SVG only; the collapse chevron's turn is not animated under reduced motion.
- **Board:** columns as above; cards can be dragged between columns (manual move); Shift+click selects several cards to drag them together as a group move (same as moving each individually, one confirm for live agents, one error summary); cards show title, repos, model, elapsed time and a live status line (latest tool call).
- **Avatars:** every board card shows a small decorative inline-SVG avatar (aria-hidden, not draggable) beside its title. Its look (hue, head ornament, eye size) is derived deterministically from the card id, so it never changes and nothing is stored; no external assets. Status drives pose and animation: bobbing and blinking while running/refining (not when waiting on another card), calm breathing in Ready or when waiting, sleepy in To do, a hop with a "!" bubble in Needs input, a droopy sad face in Failed, a static smile in Review/Done. Animation phase is derived from the clock so live re-renders don't restart it; under reduced motion the avatars are static. The open card's avatar (same look, pose and live updates) is also shown larger in the modal header next to the title, above an agent-info summary (status, id, model, effort, permission mode, turn and minute limits, cost, shortened session id, activity or waiting-on, auto, root folder).
- **Clearing Done:** every card, in any column, has a small × that deletes it after a confirm, without opening the drawer (`DELETE /api/cards/<id>`). A card outside Done is first moved to Done (worktrees and leases removed, branches kept); a card with a live agent (running, or waiting for input) gets a confirm that says the agent will be stopped, and the request carries `?stop=1`, without which the server refuses with 400. A card being refined cannot be deleted (400) until refinement ends. The Done header has a **Clear** button (shown only when Done has cards) that deletes every Done card after a confirm (`DELETE /api/cards?status=done` → `{"deleted": n}`; any other or missing `status` is refused with 400). Deleting, here and via **Delete card** in the drawer, removes only the board's data (the card, its repos, inputs and events); branches kept on approve or move-to-Done stay in their repos. Open boards update live through the `deleted` event.
- **Starting Ready:** the Ready header has a **Start all** button (shown only when Ready has cards) that runs every Ready card, and every Todo card that skips refinement, after a confirm, as if Run had been pressed on each: `POST /api/cards/run?status=ready` → `{"started": n, "errors": {"<id>": "message"}}`. Cards start in ascending id order (the board's order), so the local-folder queue stays first in, first out. A card that left Ready (or Todo) since the listing is skipped silently and not counted; other refusals are listed in `errors` and shown to me. Any other or missing `status` is refused with 400 and starts nothing. There is no cap: cards needing a leased local folder queue as usual.
- **Refining Todo:** the Todo header has a **Refine all** button (shown only when Todo has a card that needs refinement) that, after a confirm, refines every Todo card as if Refine had been pressed on each: `POST /api/cards/refine?status=todo` → `{"refined": n, "errors": {"<id>": "message"}}`. Cards that skip refinement and chat cards are left alone and not counted; a card that left Todo since the listing is skipped silently; other refusals are listed in `errors` (one failing card never stops the rest) and shown to me. Any other or missing `status` is refused with 400 and refines nothing.
- **Harness:** each board card's meta line starts `#12 · claude · sonnet · medium · auto` (id, harness, model, effort, permission mode), and the card detail's session row shows the harness too, so it is visible what runs the card even with a single harness. The harness name is preceded by a small inline-SVG icon per adapter (claude in the accent orange, opencode a terminal-window glyph in teal), with a generic fallback icon for unknown names and no external assets. The card editor (new and edit) and Settings have a harness `<select>` filled from `GET /api/harnesses` (new cards default to the settings harness); a harness that is not available is shown as `opencode (not available)` with the reason as tooltip (and a note under the selects) but stays selectable; running its cards fails with the reason. Changing it refills the model (from the harness's `models`), effort and permission-mode selects from that harness, keeping the current choice if the new harness accepts it.
- **Kind:** the new-card form and the card editor have a Kind select (Code / Research / Plan / Chat); Chat hides the refinement options. The board card's meta line shows the kind when it is not `code` (`#12 · research · claude · sonnet …`), the card detail shows it as a badge. The Needs input panel shows a `chat` input as the agent's reply with a reply box (Ctrl/Cmd+Enter sends) and a **Close chat** action that moves the card to Done.
- **New card:** one "Prompt" textarea (focused; Ctrl/Cmd+Enter creates and refines, or just creates when skip is ticked) and a collapsed "Options" section with the project, root folder, checkboxes and run settings below. The form sends the prompt as `description` and no title, so the server derives it; refinement later replaces the derived title. An empty prompt is refused in the browser.
- **Tools:** the card editor (new card, Todo and Ready) has a "Tools" group with one checkbox per toolbox entry (`GET /api/toolbox`). An entry the selected harness does not support is disabled (and unticked) with a tooltip saying so; changing the harness refreshes this. Changing the kind re-applies the settings defaults for that kind, only on a new card. The card detail's agent-info summary lists the card's tools.
- **Folder/file picker:** an in-page modal (`pickPath({mode, start, title})` in `picker.js`, markup in `#picker`, stacked above the other modals so it opens from inside the card drawer) backed by `GET /api/fs/list`. It has an "↑ Up" button, an editable path box (Enter goes there), the list of entries (folders with a "repo" badge for git repos), a "Show hidden" checkbox and Cancel. In `dir` mode it lists folders only and "Select this folder" returns the current folder; clicking a folder opens it. In `file` mode it lists folders and files, clicking a folder opens it and clicking a file returns it. A "first entries only" notice shows when the listing is `truncated`. It starts at the given path, or at the server's default when that cannot be listed. Escape, the backdrop or Cancel dismisses it (the value stays as it was); the rest of the page is inert while it is open, focus is kept inside, and arrow keys, Home/End and Backspace (up) move through the list. No animation with reduced motion.
- **Browse buttons:** the root-folder inputs (new/edit card form, new-project form and the default root folder in Settings) are a text input plus a **Browse…** button (`pathField`) that opens the picker in `dir` mode starting at the input's value. Typing a path still works.
- **Additional context:** the card editor (new card, Todo and Ready) has an "Additional context" group: the list of paths (folder or file icon, path, × to remove), **Add folder…** and **Add file…** buttons that open the picker (starting at the root folder field), and a text box with **Add** (Enter) for a typed path, whose type the server decides. Duplicates are not added. The form always sends the whole list as `context`, even when empty, so removals persist. In other columns the card detail shows the list read-only as an "Additional context (N)" section.
- **Card editor** (Todo/Ready detail): title, description, root folder, harness and the other run settings, auto-run checkbox ("Run automatically after refinement"), push default, and (for new and Todo cards) a "Skip refinement" checkbox, which turns off "Create and refine" in the new-card form. Cards with `auto_run` show "auto" in their meta line until they run; cards that skipped refinement show "unrefined".
- **Modal:** card detail, New card and Settings open in a centred modal dialog (blurred backdrop, animated accent border; Escape, the backdrop or Close dismisses it; full-screen on narrow screens; no animation with reduced motion).
- **Card detail:** refined prompt (editable in Ready), live log, Needs input panel (question or permission request + answer box / allow-deny), review panel. Every titled section is collapsible (Refined prompt, Card and run settings and per-repo diffs start collapsed); open/closed state is remembered per card in the browser and survives live updates.
- **Account and usage indicator:** the header shows, between the spacer and the Settings gear button, one group per harness that reports an account or usage limits (`Caps.Account` or `Caps.Usage`; sorted by name): the harness's small icon (as on the cards), who its CLI is logged in as (email, or "not logged in" / "account unknown") and a compact chip per usage-limit window: a small radial (ring) gauge of its utilization plus text, e.g. ring + "5h 42% · resets 14:30" (ring and text `--warn` for `allowed_warning`, `--bad` for `rejected`; otherwise `--ok`; the ring is clamped to 0–100% while the text shows the real value, and a window with no utilization gets an empty dashed ring instead of a 0% gauge). A harness that cannot report an account but reports usage shows "account unknown" and its chips. A tooltip and a click-to-open popover have one section per harness: the organisation, auth method, plan, and every window with a horizontal progress bar of its utilization (same colours and clamping, with a tick at the warning threshold when known), its status, utilization, absolute reset time and when it was last observed, or "no usage data yet; updates when an agent runs" (or "This harness reports no usage limits"). Each section has its own ↻ Refresh button (shown when the harness has an account lookup) that calls `GET /api/account?harness=<name>&refresh=1`; its tooltip says what it runs ("Re-run claude auth status"). On narrow screens the email and reset times are hidden first. With only `claude` registered the header looks as it always did, plus the icon. Data sources, CLI output only (no credential files, no direct API calls):
  - *Account:* each adapter's `Account(ctx)` (claude: `claude auth status --json` as a one-shot subprocess with `claudeEnv`, parsed leniently: unknown fields are ignored and a missing or mistyped field stays empty). `refreshAccount(name)` runs it through `App.harness(name)` with a 10s timeout and caches the result per harness, together with the error and the check time. At startup every registered harness with `Caps.Account` is refreshed in the background (in parallel, never blocking startup); after that only on demand, never per request. A failing, missing, unknown or logged-out CLI becomes an `account_error` or `logged_in: false` and replaces the earlier account, never a broken board. A harness without `Caps.Account` is never run: its entry has no account, no error and no check time. The email appears only in the header, never in card events or notes.
  - *Usage limits:* every stdout line of a run is passed to the card's harness's `ParseUsage` (when `Caps.Usage`). The updates are merged into the latest limit per window of that harness (the card's `harness`, `""` meaning `claude`): status, reset time, `utilization` (a fraction), threshold and overage fields, and the time it was observed. This changes no card; only `RateLimited` stops a run, as before. Claude's `ParseUsage` reads each `rate_limit_event`'s `rate_limit_info` whatever its status: the per-window `unifiedWindows` when the CLI reports them, then the top-level info for the window named by `rateLimitType` (`resetsAt` in milliseconds is converted to seconds). Windows are ordered `five_hour`, `seven_day`, `seven_day_opus`, `seven_day_sonnet`, `seven_day_overage_included`, `overage`, then the rest by name. The limits are kept in memory per harness, saved under the `usage_limits` key of the `settings` table (outside `Settings`) as an object by harness name (`{"claude": [...]}`) so they survive a restart, and pushed to open boards as the `usage` SSE event. On load, an old value that is a plain JSON array is read as `claude`'s limits and rewritten in the new shape by the next update.
  - `GET /api/account` returns one entry per registered harness, sorted by name like `GET /api/harnesses`: `{"harnesses": [{"harness": "claude", "account_supported": true, "usage_supported": true, "account": {…} | null, "account_error": "…", "checked_at": "…" | null, "limits": [{"type", "status", "resets_at", "utilization", "observed_at", …}]}]}` (`account_error` is left out when empty; `limits` is `[]` when there are none). `?refresh=1` re-runs the account lookup of every harness first; `?harness=<name>&refresh=1` only of that harness (an unknown name is a 400, with or without `refresh`). The `usage` SSE event carries the same whole snapshot, so the UI replaces its copy. The UI reloads it on every SSE (re)connect.
- **Settings:** opened from a gear-icon button in the header (inline SVG, `aria-label`/tooltip "Settings"; header order: account indicator, gear, New card). Fields: default model, effort, permission mode, limits, push default, default auto-run (`default_auto_run`, off by default), default skip refinement (`default_skip_refinement`, off by default), refinement model and effort, workspace root, and the default `harness` (default `claude`, a select). Under the fields Settings lists every harness with its availability (version, or why it is not available) and a ↻ re-check button (`GET /api/harnesses?refresh=1`); the refinement model and effort selects follow the chosen harness. A grid of kinds × toolbox checkboxes sets `default_tools` (kind → ids); new cards of a kind start with that entry. New cards are seeded from these; `PUT /api/settings` rejects unknown tool ids and unknown kinds in `default_tools` (duplicates are dropped), and rejects invalid values (empty model, unknown harness/effort/permission mode/push default, negative limits).

## Data model (SQLite)

- `projects` — id, name (unique), root_folder, created_at, updated_at.
- `cards` — the fields above, including `kind`, `plan_tasks` and `tools` (comma-separated toolbox ids; added with `ALTER TABLE`), `context` (JSON `[{path,type}]`, `TEXT NOT NULL DEFAULT ''`; added with `ALTER TABLE`) and `project_id` (added with `ALTER TABLE`; 0 only until the migration on open gives it a project).
- `card_repos` — card_id, repo_path, worktree_path, branch, base_ref, review_action, pr_url.
- `events` — card_id, seq, ts, kind, raw JSON (stream events, tool calls, status changes).
- `inputs` — card_id, kind (question / permission / limit / usage_limit / chat), payload, answer, answered_at.
- `settings` — key/value (`settings` holds the Settings, including `default_tools`; `usage_limits` the last usage-limit snapshot per harness).
- Lease files (not in SQLite): `<data dir>/leases/*.json`, one per leased local folder.

## Testing rules (hard requirement)

All development testing stays inside this project folder. Touching real repos outside it is not acceptable.

- **Fake repos only:** `testdata/` holds a setup script that creates throwaway git repos plus a local bare repo for each as its `origin`. Fetch and push only ever hit those bare repos.
- **Workspace root override:** tests and dev runs set the workspace root to `testdata/work/`, never `~/.maestrello`.
- **Sandbox:** the sandbox is the path guard (`internal/sandbox`) plus the `testdata/` fixtures (`internal/testenv` locates them for tests).
- **Path guard in code:** a dev/test flag (`--sandbox-root <dir>`) makes the board refuse any `root_folder`, repo path, worktree path or `--add-dir` that doesn't resolve (after symlinks) inside that directory. It's on for every test run.
- **No real remotes or PRs:** `gh pr create` and pushes to non-local remotes are disabled in sandbox mode. They're stubbed and logged instead.
- **Agent runs in tests** use `root_folder` = a `testdata/` fixture, so `--add-dir` and the working directory both stay inside the project.
- **The folder picker stays in the sandbox too:** `GET /api/fs/list` goes through the same path guard, so with `--sandbox-root` it can only list inside the sandbox, and its tests (`internal/board/fs_test.go`, `internal/board/context_test.go`) run against the `testdata/` fixtures and scratch directories under the project folder, never real folders (context paths in tests are created inside the sandbox too).

## Decision record

- **Folder choice: an in-page picker instead of `zenity` / `kdialog`.** Browse buttons and the context editor use a modal in the web UI backed by `GET /api/fs/list`, not a native dialog launched by the board. A native dialog would open on the display of the process that runs the board, not in the browser I am using, and needs `zenity` or `kdialog` installed, which varies by desktop; the board is a single Go binary with an embedded UI and no such dependency. The picker works in whatever browser shows the board. It also goes through the sandbox guard, so it can only show what the board would accept (a native dialog would show any path and leave the checking to afterwards), and it can show git repos, hidden files and truncation the way the board wants. The cost is one read-only endpoint, guarded by the Host check described in *Folder picker API*.

## Open items to settle while building

- Exact stream-json shapes for rate-limit errors and result messages: confirm against the installed CLI.
- How to interrupt cleanly at a turn limit (stream-json control message vs. killing the process). Killing is acceptable because the session persists and is resumed.
- Repo discovery under `root_folder`: walk for `.git` directories (depth limit, skip `node_modules` etc.), cached per folder.
