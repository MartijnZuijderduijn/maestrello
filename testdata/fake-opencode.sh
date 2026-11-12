#!/usr/bin/env bash
# Stand-in for the opencode CLI, for exercising the board without a model.
#   --version        prints a fake version
#   models           prints two fake provider/model lines
#   run ...          reads the prompt from stdin and acts on a keyword in it, like fake-claude.sh:
#     ASK (asks the user, then reports done), LOOP, FAIL, ERR (emits an error event), PLAN, CHAT,
#     anything else reports done. A refinement (config passed inline in OPENCODE_CONFIG_CONTENT)
#     answers with the schema's JSON in a ```json fence.
#   Output is `--format json` style: one event per line, each with a sessionID. A new session
#   gets the id ses_fake...; with -s <id> that id is kept and the text says "(resumed)".
set -euo pipefail

if [ "${1:-}" = --version ]; then echo "1.2.10-fake"; exit 0; fi
if [ "${1:-}" = models ]; then printf 'ollama/fake-model:1b\nopencode/fake-free\n'; exit 0; fi
[ "${1:-}" = run ] || { echo "fake opencode: unknown command $*" >&2; exit 1; }
shift

session="" model="" agent=""
while [ $# -gt 0 ]; do
  case "$1" in
    -s|--session) session="$2"; shift 2 ;;
    -m|--model) model="$2"; shift 2 ;;
    --agent) agent="$2"; shift 2 ;;
    *) shift ;;
  esac
done
resumed=""
if [ -n "$session" ]; then resumed=1; else session="ses_fake$RANDOM$RANDOM"; fi
prompt="$(cat)"
now() { echo $((1700000000000 + RANDOM)); }
ev() { # ev <type> <part-json>
  jq -cn --arg t "$1" --arg s "$session" --argjson ts "$(now)" --argjson p "$2" '{type:$t,timestamp:$ts,sessionID:$s,part:$p}'
}
turn() { # turn <msg-id> <text>
  ev step_start '{"type":"step-start"}'
  ev text "$(jq -cn --arg m "msg_$1" --arg t "$2" '{type:"text",messageID:$m,text:$t}')"
  ev step_finish "$(jq -cn --arg m "msg_$1" '{type:"step-finish",messageID:$m,reason:"stop",cost:0}')"
}

if [ -n "${OPENCODE_CONFIG_CONTENT:-}" ]; then
  card="${prompt#*--- CARD ---}"
  body="$(jq -cn --arg p "Refined: ${card:0:200}" '{title:"Fake refined title",refined_prompt:$p,repos:[],notes:"fake refinement"}')"
  turn refine "$(printf 'Here is the result:\n```json\n%s\n```' "$body")"
  exit 0
fi

cfg="${OPENCODE_CONFIG:?}"
url="$(jq -r '.mcp.maestrello.url' "$cfg")"
call() { # call <tool> <args-json>
  curl -s -X POST "$url" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    -d "$(jq -cn --arg n "$1" --argjson a "$2" '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:$n,arguments:$a}}')" |
    jq -r '.result.content[0].text'
}
tool() { # tool <msg-id> <name> <input-json>
  ev step_start '{"type":"step-start"}'
  ev tool_use "$(jq -cn --arg m "msg_$1" --arg n "$2" --argjson i "$3" '{type:"tool",messageID:$m,tool:$n,state:{status:"completed",input:$i,output:"ok"}}')"
  ev step_finish "$(jq -cn --arg m "msg_$1" '{type:"step-finish",messageID:$m,reason:"tool-calls",cost:0}')"
}

turn 1 "Working on: ${prompt:0:80} (model ${model:-none}, agent $agent)"
tool 2 read '{"filePath":"/tmp/example.txt"}'

case "$prompt" in
  *LOOP*) for i in $(seq 3 1000); do turn "$i" "looping $i"; sleep 0.05; done ;;
  *FAIL*) echo "fake failure" >&2; exit 3 ;;
  *ERR*)
    jq -cn --arg s "$session" '{type:"error",timestamp:1,sessionID:$s,error:{name:"UnknownError",data:{message:"Error: boom"}}}'
    exit 1 ;;
  *PLAN*)
    call report_plan '{"plan":"Fake plan.","tasks":[{"title":"First fake task","description":"Do the first part."}]}' >/dev/null ;;
  *CHAT*) turn 3 "Fake chat reply ${resumed:+(resumed) }to: ${prompt:0:60}" ;;
  *ASK*)
    answer="$(call ask_user '{"question":"Which colour?"}')"
    turn 3 "User said: $answer"
    call report_done "$(jq -cn --arg a "$answer" '{summary:("done with answer " + $a)}')" >/dev/null ;;
  *)
    if [[ "$prompt" == *WRITE* ]]; then
      echo "changed by fake" >CHANGED.txt
    fi
    call report_done '{"summary":"fake work done"}' >/dev/null ;;
esac
