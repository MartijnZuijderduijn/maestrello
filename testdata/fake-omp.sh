#!/usr/bin/env bash
# Stand-in for the Oh My Pi CLI (`omp`), for exercising the board without a model.
#   --version        prints `omp/<version>` like the real one
#   usage --json     prints the JSON of `omp usage --json`: one account (provider, no keys),
#                    or none when FAKE_OMP_LOGGED_OUT is set
#   models --json    prints a small model catalog (exit 1 when FAKE_OMP_NO_MODELS is set)
#   -p ...           reads the prompt from stdin and acts on a keyword in it, like fake-mistral.sh:
#     ASK (asks the user, then reports done), LOOP, FAIL, ERR (exits 2), RATE (rate-limit error), PLAN,
#     CHAT, anything else reports done. A refinement (no --mode json) prints the schema's JSON in a
#     ```json fence.
#   `--mode json` prints pi's JSON events: a `session` header, then message_end / turn_end per turn.
#   The board's MCP URL is read from $PI_CODING_AGENT_DIR/mcp.json, as the real CLI would load it.
set -euo pipefail

if [ "${1:-}" = --version ]; then echo "omp/18.20.0-fake"; exit 0; fi
if [ "${1:-}" = usage ]; then
  if [ -n "${FAKE_OMP_LOGGED_OUT:-}" ]; then
    echo '{"generatedAt":1,"reports":[],"accountsWithoutUsage":[],"disabledCredentials":[],"capacity":{}}'
  else
    echo '{"generatedAt":1,"reports":[{"provider":"anthropic","email":"fake@example.com","limits":[]}],"accountsWithoutUsage":[],"disabledCredentials":[],"capacity":{}}'
  fi
  exit 0
fi
if [ "${1:-}" = models ]; then
  # `omp models --json`: the catalog, one chat model per provider (FAKE_OMP_NO_MODELS: fails)
  [ -z "${FAKE_OMP_NO_MODELS:-}" ] || { echo "no catalog" >&2; exit 1; }
  echo '{"models":[{"provider":"anthropic","kind":"chat","id":"claude-opus-4-5","selector":"anthropic/claude-opus-4-5"},{"provider":"ollama","kind":"chat","id":"devstral:24b","selector":"ollama/devstral:24b"},{"provider":"ollama","kind":"embedding","id":"nomic","selector":"ollama/nomic"}]}'
  exit 0
fi
[ "${1:-}" = -p ] || { echo "fake omp: unknown command $*" >&2; exit 1; }
shift

mode=text model="" resume="" tools=""
while [ $# -gt 0 ]; do
  case "$1" in
    --mode) mode="$2"; shift 2 ;;
    --model) model="$2"; shift 2 ;;
    --resume) resume="$2"; shift 2 ;;
    --tools) tools="$2"; shift 2 ;;
    --config|--append-system-prompt|--approval-mode|--thinking|--add-dir) shift 2 ;;
    *) shift ;;
  esac
done
prompt="$(cat)"

if [ "$mode" != json ]; then
  card="${prompt#*--- CARD ---}"
  body="$(jq -cn --arg p "Refined: ${card:0:200}" '{title:"Fake refined title",refined_prompt:$p,repos:[],notes:"fake refinement"}')"
  printf 'Here is the result:\n```json\n%s\n```\n' "$body"
  exit 0
fi

n=0
sid="${resume:-fake-omp-session-$$}"
jq -cn --arg id "$sid" --arg cwd "$PWD" '{type:"session",id:$id,cwd:$cwd}'
turn() { # turn <content-json-array> [stopReason] [errorMessage]
  n=$((n+1))
  local msg
  msg="$(jq -cn --argjson c "$1" --arg r "fake-$$-$n" --arg s "${2:-stop}" --arg e "${3:-}" \
    '{role:"assistant",content:$c,responseId:$r,stopReason:$s,usage:{cost:{total:0.01}}} + (if $e != "" then {errorMessage:$e} else {} end)')"
  jq -cn --argjson m "$msg" '{type:"message_end",message:$m}'
  jq -cn --argjson m "$msg" '{type:"turn_end",message:$m,toolResults:[]}'
}
say() { turn "$(jq -cn --arg t "$1" '[{type:"text",text:$t}]')"; }
tool() { # tool <name> <args-json>
  turn "$(jq -cn --arg n "$1" --argjson a "$2" '[{type:"toolCall",id:"c1",name:$n,arguments:$a}]')"
}

url="$(jq -r '.mcpServers.maestrello.url' "${PI_CODING_AGENT_DIR:?}/mcp.json")"
call() { # call <tool> <args-json>
  curl -s -X POST "$url" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -d "$(jq -cn --arg n "$1" --argjson a "$2" '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:$n,arguments:$a}}')" |
    jq -r '.result.content[0].text'
}

say "Working on: ${prompt:0:80} (model ${model:-none}, tools ${tools:0:40})"
tool read '{"path":"/tmp/example.txt"}'

case "$prompt" in
  *LOOP*) for i in $(seq 3 1000); do say "looping $i"; sleep 0.05; done ;;
  *FAIL*) echo "fake failure" >&2; exit 3 ;;
  *ERR*) echo "fake omp: API error" >&2; exit 2 ;;
  *RATE*) turn '[]' error "429 usage limit reached" ;;
  *PLAN*)
    call report_plan '{"plan":"Fake plan.","tasks":[{"title":"First fake task","description":"Do the first part."}]}' >/dev/null ;;
  *CHAT*) say "Fake chat reply to: ${prompt:0:60}" ;;
  *ASK*)
    answer="$(call ask_user '{"question":"Which colour?"}')"
    say "User said: $answer"
    call report_done "$(jq -cn --arg a "$answer" '{summary:("done with answer " + $a)}')" >/dev/null ;;
  *)
    if [[ "$prompt" == *WRITE* ]]; then
      echo "changed by fake" >CHANGED.txt
    fi
    call report_done '{"summary":"fake work done"}' >/dev/null ;;
esac
jq -cn '{type:"agent_end",messages:[]}'
