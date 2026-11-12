#!/usr/bin/env bash
# Stand-in for the claude CLI, for exercising the board without a model or git.
# `auth status ...`: prints a fixed fake account as JSON and exits (no stdin read).
#   FAKE_CLAUDE_AUTH=out  - prints {"loggedIn":false} and exits 1 instead
# Refinement (--json-schema): prints a result with structured_output.
# Agent run: reads the prompt from stdin and acts on a keyword in it:
#   ASK  - asks the user via ask_user (blocks until answered), then reports done
#   LOOP - emits assistant turns until the board stops it
#   HANG - goes silent (no output) until the board stops it
#   FAIL - exits without reporting
#   PLAN - reports a plan with two tasks via report_plan (for plan cards)
#   CHAT - prints an assistant reply and exits cleanly, with no report (for chat cards;
#          the reply says whether the session was started or resumed). A session
#          started with --tools "" (a chat card) acts as if the prompt said CHAT.
#   anything else - reports done
# Modifiers, applied before reporting done:
#   ADD:<repo>  - checks out <repo> via add_repo
#   CWD         - emits an assistant turn "cwd: <working directory>"
#   WRITE       - writes CHANGED.txt into every worktree in the workspace
#   TOUCH       - writes STRAY.txt into every original checkout (to trip the guard)
#   LOCAL       - writes LOCAL.txt into the working directory (local work runs there)
#   RATEOK      - emits rate_limit_events that allow the request: five_hour allowed
#                 (overage rejected, 42% used) and seven_day allowed_warning (81% used),
#                 both with resetsAt and utilization
#   RATEAFTER   - after reporting done, emits a rate_limit_event that rejects
set -euo pipefail

if [ "${1:-}" = --version ]; then
  echo "0.0.0 (Fake Claude)"; exit 0
fi
if [ "${1:-}" = auth ]; then
  if [ "${2:-}" = status ]; then
    if [ "${FAKE_CLAUDE_AUTH:-}" = out ]; then
      echo '{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}'
      exit 1
    fi
    echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"test@maestrello.invalid","orgName":"Fake Org","subscriptionType":"max"}'
    exit 0
  fi
  echo "fake claude: unknown auth command" >&2; exit 1
fi

mcp_config="" schema="" session="" add_dir="" resumed="" chat=""
while [ $# -gt 0 ]; do
  case "$1" in
    --add-dir) add_dir="$2"; shift 2 ;;
    --mcp-config) mcp_config="$2"; shift 2 ;;
    --json-schema) schema="$2"; shift 2 ;;
    --tools) case "$2" in *Read*) ;; *) chat=1 ;; esac; shift 2 ;;
    --session-id) session="$2"; shift 2 ;;
    --resume) session="$2"; resumed=1; shift 2 ;;
    *) shift ;;
  esac
done
prompt="$(cat)"

if [ -n "$schema" ]; then
  jq -cn --arg p "Refined: ${prompt: -200}" '{type:"result",subtype:"success",is_error:false,result:"",
    structured_output:{title:"Fake refined title",refined_prompt:$p,repos:[],notes:"fake refinement"},total_cost_usd:0.001}'
  exit 0
fi

url="$(jq -r '.mcpServers.maestrello.url' "$mcp_config")"
call() { # call <tool> <args-json>
  curl -s -X POST "$url" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -d "$(jq -cn --arg n "$1" --argjson a "$2" '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:$n,arguments:$a}}')" |
    jq -r '.result.content[0].text'
}
turn() { # turn <n> <text>
  jq -cn --arg id "msg_$1_$RANDOM" --arg t "$2" '{type:"assistant",message:{id:$id,content:[{type:"text",text:$t}]}}'
}

jq -cn --arg s "$session" '{type:"system",subtype:"init",session_id:$s,model:"fake"}'
turn 1 "Working on: ${prompt:0:80}"

if [[ "$prompt" =~ ADD:([^[:space:]]+) ]]; then
  call add_repo "$(jq -cn --arg p "${BASH_REMATCH[1]}" '{path:$p}')" >/dev/null
fi
if [[ "$prompt" == *CWD* ]]; then
  turn cwd "cwd: $PWD"
fi
if [[ "$prompt" == *WRITE* ]]; then
  find . -mindepth 2 -maxdepth 4 -name .git -type f | while read -r g; do echo "changed by fake" >"$(dirname "$g")/CHANGED.txt"; done
fi
if [[ "$prompt" == *LOCAL* ]]; then
  echo "changed in place by fake" >LOCAL.txt
fi
if [[ "$prompt" == *RATEOK* ]]; then
  now=$(date +%s)
  jq -cn --argjson r $((now + 3600)) '{type:"rate_limit_event",rate_limit_info:{status:"allowed",rateLimitType:"five_hour",overageStatus:"rejected",resetsAt:$r,utilization:0.42}}'
  jq -cn --argjson r $((now + 4 * 86400)) '{type:"rate_limit_event",rate_limit_info:{status:"allowed_warning",rateLimitType:"seven_day",resetsAt:$r,utilization:0.81,surpassedThreshold:0.75}}'
fi
if [[ "$prompt" == *TOUCH* && -n "$add_dir" ]]; then
  find "$add_dir" -mindepth 2 -maxdepth 4 -name .git -type d | while read -r g; do echo "stray" >"$(dirname "$g")/STRAY.txt"; done
fi

said=""
if [ -n "$chat" ] && [[ "$prompt" != *LOOP* && "$prompt" != *HANG* && "$prompt" != *FAIL* ]]; then
  said="$prompt"
  prompt="CHAT $prompt" # a chat session (--tools "") only ever answers
fi
case "$prompt" in
  *LOOP*)
    for i in $(seq 2 1000); do turn "$i" "looping $i"; sleep 0.05; done ;;
  *HANG*)
    sleep 60 ;;
  *FAIL*)
    echo "fake failure" >&2; exit 3 ;;
  *PLAN*)
    call report_plan '{"plan":"Fake plan: do it in two steps.","tasks":[
      {"title":"First fake task","description":"Do the first part."},
      {"title":"Second fake task","description":"Do the second part.","kind":"research"}]}' >/dev/null ;;
  *CHAT*)
    turn 2 "Fake chat reply ${resumed:+(resumed) }to: ${said:-${prompt:0:60}}" ;;
  *ASK*)
    answer="$(call ask_user '{"question":"Which colour?"}')"
    turn 2 "User said: $answer"
    call report_done "$(jq -cn --arg a "$answer" '{summary:("done with answer " + $a)}')" >/dev/null ;;
  *)
    call report_done '{"summary":"fake work done"}' >/dev/null
    if [[ "$prompt" == *RATEAFTER* ]]; then
      echo '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour"}}'
    fi ;;
esac
jq -cn '{type:"result",subtype:"success",is_error:false,result:"ok",num_turns:2,total_cost_usd:0.002}'
