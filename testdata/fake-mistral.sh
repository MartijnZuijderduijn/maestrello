#!/usr/bin/env bash
# Stand-in for the Mistral Vibe CLI (`vibe`), for exercising the board without a model.
#   --version        prints a fake version
#   -p ...           reads the prompt from stdin and acts on a keyword in it, like fake-opencode.sh:
#     ASK (asks the user, then reports done), LOOP, FAIL, ERR (exits 2), PLAN, CHAT,
#     anything else reports done. A refinement (`--output text`, no MCP server in the config) prints
#     the schema's JSON in a ```json fence.
#   `--output streaming` prints one JSON message per line (role, content, tool_calls), like Vibe's
#   documented "one JSON object per message". The home (VIBE_HOME) holds config.toml with the board's MCP URL.
set -euo pipefail

if [ "${1:-}" = --version ]; then echo "2.4.2-fake"; exit 0; fi
[ "${1:-}" = -p ] || { echo "fake vibe: unknown command $*" >&2; exit 1; }
shift

output="" agent="" model=""
while [ $# -gt 0 ]; do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    --agent) agent="$2"; shift 2 ;;
    --model) model="$2"; shift 2 ;;
    --enabled-tools) shift 2 ;;
    *) shift ;;
  esac
done
prompt="$(cat)"
n=0
say() { n=$((n+1)); jq -cn --arg t "$1" --arg m "fake-$$-$n" '{role:"assistant",content:$t,message_id:$m}'; }
tool() { # tool <name> <args-json>
  n=$((n+1)); jq -cn --arg m "fake-$$-$n" --arg n "$1" --arg a "$2" '{role:"assistant",content:"",message_id:$m,tool_calls:[{id:"c1",type:"function",function:{name:$n,arguments:$a}}]}'
  jq -cn '{role:"tool",content:"ok",name:"x"}'
}

if [ "$output" = text ]; then
  card="${prompt#*--- CARD ---}"
  body="$(jq -cn --arg p "Refined: ${card:0:200}" '{title:"Fake refined title",refined_prompt:$p,repos:[],notes:"fake refinement"}')"
  printf 'Here is the result:\n```json\n%s\n```\n' "$body"
  exit 0
fi

cfg="${VIBE_HOME:?}/config.toml"
url="$(sed -n 's/^url = "\(.*\)"$/\1/p' "$cfg")"
call() { # call <tool> <args-json>
  curl -s -X POST "$url" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -d "$(jq -cn --arg n "$1" --argjson a "$2" '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:$n,arguments:$a}}')" |
    jq -r '.result.content[0].text'
}

say "Working on: ${prompt:0:80} (model ${model:-none}, agent $agent)"
tool read_file '{"path":"/tmp/example.txt"}'

case "$prompt" in
  *LOOP*) for i in $(seq 3 1000); do say "looping $i"; sleep 0.05; done ;;
  *FAIL*) echo "fake failure" >&2; exit 3 ;;
  *ERR*) echo "fake vibe: API error" >&2; exit 2 ;;
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
