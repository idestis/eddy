#!/usr/bin/env bash
# Local-mode developer workflow behind `task dev`, `task dev:hub`, `task dev:agent` and
# `task dev:web`. DEV ONLY: nothing here is used by the charts or the release images.
#
#   hack/dev.sh config   write .dev/hub.yaml (and .dev/agent-token when missing)
#   hack/dev.sh hub      the hub with hot reload (air, -tags dev)
#   hack/dev.sh agent    the agent with hot reload; local mode unless LOCAL=0
#   hack/dev.sh web      Vite on http://localhost:5173
#   hack/dev.sh all      config, then hub, agent and web together; Ctrl+C stops all
#
# Settings, highest precedence first: task variables (CONTEXTS=, LOCAL=, ALLOW_WRITES=,
# ALLOW_WRITES_PROTECTED=, PROTECT=, SQLITE=), then the environment or .env (EDDY_AGENT_*),
# then the defaults. See docs/development.md.
set -euo pipefail

cd "$(dirname "$0")/.."

AIR_VERSION=v1.67.4
HUB_AGENT_URL=ws://127.0.0.1:8443/agent/v1/connect
TOKEN_FILE=.dev/agent-token
HUB_CONFIG=.dev/hub.yaml

contexts="${CONTEXTS:-${EDDY_AGENT_CONTEXTS:-}}"
local_mode="${LOCAL:-${EDDY_AGENT_LOCAL:-1}}"
allow_writes="${ALLOW_WRITES:-${EDDY_AGENT_ALLOW_WRITES:-0}}"
allow_writes_protected="${ALLOW_WRITES_PROTECTED:-0}"
protect="${PROTECT:-${EDDY_AGENT_PROTECT:-(?i)prod}}"
sqlite="${SQLITE:-0}"

die() {
  echo "hack/dev.sh: $*" >&2
  exit 1
}

is_true() {
  case "${1:-}" in
  1 | true | TRUE | True | yes) return 0 ;;
  *) return 1 ;;
  esac
}

air_cmd() {
  if command -v air >/dev/null 2>&1; then
    AIR=(air)
  else
    AIR=(go run "github.com/air-verse/air@${AIR_VERSION}")
  fi
}

load_token() {
  [[ -s $TOKEN_FILE ]] || die "$TOKEN_FILE is missing; run: task dev:config"
  EDDY_DEV_AGENT_TOKEN="$(tr -d '[:space:]' <"$TOKEN_FILE")"
  export EDDY_DEV_AGENT_TOKEN
}

cmd_config() {
  if ! is_true "$local_mode" && [[ -n $contexts ]]; then
    echo "hack/dev.sh: LOCAL=0 serves only the current context; ignoring CONTEXTS=$contexts" >&2
    contexts=""
  fi
  local args=(--contexts "$contexts" --protect "$protect" --out "$HUB_CONFIG" --token-file "$TOKEN_FILE")
  if is_true "$sqlite"; then
    args+=(--sqlite)
  fi
  go run -tags dev ./cmd/hub dev-config "${args[@]}"
}

cmd_hub() {
  [[ -f $HUB_CONFIG ]] || die "$HUB_CONFIG is missing; run: task dev:config"
  load_token
  export EDDY_DEV_MODE=1
  air_cmd
  exec "${AIR[@]}" -c .air.hub.toml
}

cmd_agent() {
  load_token
  export EDDY_DEV_MODE=1 EDDY_HUB_URL="$HUB_AGENT_URL" EDDY_AGENT_TOKEN="$EDDY_DEV_AGENT_TOKEN"
  local extra=()
  air_cmd
  if is_true "$local_mode"; then
    if is_true "$allow_writes_protected"; then
      if [[ -f .env ]] && grep -qE '^[[:space:]]*(export[[:space:]]+)?ALLOW_WRITES_PROTECTED=' .env; then
        die "ALLOW_WRITES_PROTECTED must be typed on the command line, not kept in .env"
      fi
      extra=(-- --allow-writes-protected)
    fi
    export EDDY_AGENT_LOCAL=1 EDDY_AGENT_CONTEXTS="$contexts" EDDY_AGENT_PROTECT="$protect"
    if is_true "$allow_writes"; then
      export EDDY_AGENT_ALLOW_WRITES=1
    else
      export EDDY_AGENT_ALLOW_WRITES=0
    fi
    # ${extra[@]+...}: macOS bash 3.2 treats an empty array as unset under set -u.
    exec "${AIR[@]}" -c .air.agent.toml ${extra[@]+"${extra[@]}"}
  fi

  # LOCAL=0: the real, impersonating agent against the current context (meant for kind).
  [[ -f $HUB_CONFIG ]] || die "$HUB_CONFIG is missing; run: task dev:config LOCAL=0"
  local names
  names="$(sed -n 's/^  - name: "\(.*\)"$/\1/p' "$HUB_CONFIG")"
  [[ $(printf '%s\n' "$names" | grep -c .) -eq 1 ]] ||
    die "LOCAL=0 needs a hub config with one cluster; run: task dev:config LOCAL=0"
  if grep -q '^    protected: true' "$HUB_CONFIG"; then
    die "LOCAL=0 would run the impersonating agent against a context matching PROTECT ($protect); switch to a kind context"
  fi
  export EDDY_AGENT_LOCAL=0 EDDY_CLUSTER="$names" EDDY_ALLOW_INSECURE=1 EDDY_HEALTH_ADDR="${EDDY_HEALTH_ADDR:-127.0.0.1:18081}"
  exec "${AIR[@]}" -c .air.agent.toml
}

cmd_web() {
  cd web
  [[ -d node_modules ]] || pnpm install --frozen-lockfile
  exec pnpm run dev
}

# prefix tags every line of its input. It ignores Ctrl+C and ends when its
# input closes, so a process still logging its shutdown never gets SIGPIPE.
prefix() {
  trap '' INT TERM
  local tag=$1 color=$2 line
  if [[ -t 1 ]]; then
    tag=$'\033['"${color}m${tag}"$'\033[0m'
  fi
  while IFS= read -r line || [[ -n $line ]]; do
    printf '%s %s\n' "$tag" "$line"
  done
}

cmd_all() {
  cmd_config
  load_token
  local names=(hub agent web) colors=(36 35 32) i
  pgids=()
  # Job control gives every child its own process group, so Ctrl+C reaches only this
  # script, which then stops each group in order (air forwards SIGINT to its binary).
  set -m
  for i in 0 1 2; do
    ("$0" "${names[$i]}" 2>&1 | prefix "$(printf '%-5s|' "${names[$i]}")" "${colors[$i]}") &
    pgids+=($!)
  done
  set +m

  stopping=0
  stop_all() {
    [[ $stopping == 1 ]] && return
    stopping=1
    echo "hack/dev.sh: stopping hub, agent and web" >&2
    local g alive
    for g in "${pgids[@]}"; do
      kill -INT -- "-$g" 2>/dev/null || true
    done
    for _ in $(seq 1 20); do
      alive=0
      for g in "${pgids[@]}"; do
        kill -0 -- "-$g" 2>/dev/null && alive=1
      done
      [[ $alive == 0 ]] && break
      sleep 0.5
    done
    for g in "${pgids[@]}"; do
      kill -KILL -- "-$g" 2>/dev/null || true
    done
    wait 2>/dev/null || true
  }
  trap 'stop_all; exit 130' INT TERM
  trap 'stop_all' EXIT

  echo "hack/dev.sh: UI on http://localhost:5173 (log in as dev / eddy-dev-password, or /auth/dev/login)" >&2
  while :; do
    for i in 0 1 2; do
      if ! kill -0 "${pgids[$i]}" 2>/dev/null; then
        echo "hack/dev.sh: ${names[$i]} exited; stopping the others" >&2
        exit 1
      fi
    done
    sleep 1
  done
}

case "${1:-}" in
config) cmd_config ;;
hub) cmd_hub ;;
agent) cmd_agent ;;
web) cmd_web ;;
all) cmd_all ;;
*)
  echo "usage: hack/dev.sh config|hub|agent|web|all" >&2
  exit 2
  ;;
esac
