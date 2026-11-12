#!/usr/bin/env bash
# Creates throwaway fixture repos for development and tests.
# Everything lives under testdata/: bare "origins" and working clones in project/.
# Nothing here ever talks to a real remote.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
origins="$here/origins"
project="$here/project"

rm -rf "$origins" "$project" "$here/work"
mkdir -p "$origins" "$project" "$here/work"

g() { git -c user.name=maestrello-test -c user.email=test@maestrello.invalid -c init.defaultBranch=main "$@"; }

make_repo() {
  local name="$1"; shift
  local seed="$here/.seed-$name"
  rm -rf "$seed"
  mkdir -p "$seed"
  g -C "$seed" init -q
  "$@" "$seed"
  g -C "$seed" add -A
  g -C "$seed" commit -qm "initial commit"
  g clone -q --bare "$seed" "$origins/$name.git"
  rm -rf "$seed"
  mkdir -p "$(dirname "$project/$name")"
  g clone -q "$origins/$name.git" "$project/$name"
}

svc_greeter() {
  cat >"$1/main.go" <<'EOF'
package main

import "fmt"

func main() {
	fmt.Println(greet("world"))
}

func greet(name string) string {
	return "Hello, " + name
}
EOF
  printf 'module example.invalid/greeter\n\ngo 1.22\n' >"$1/go.mod"
  printf '# greeter\n\nPrints a greeting.\n' >"$1/README.md"
}

svc_billing() {
  cat >"$1/invoice.py" <<'EOF'
def total(lines):
    return sum(qty * price for qty, price in lines)
EOF
  printf '# billing\n\nInvoice helpers.\n' >"$1/README.md"
}

lib_common() {
  cat >"$1/strings.js" <<'EOF'
export function shout(s) {
  return s.toUpperCase();
}
EOF
  printf '# common\n\nShared helpers.\n' >"$1/README.md"
}

make_repo services/greeter svc_greeter
make_repo services/billing svc_billing
make_repo libs/common lib_common

echo "fixtures ready in $project"
