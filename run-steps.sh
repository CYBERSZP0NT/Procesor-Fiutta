#!/usr/bin/env bash
# run-steps.sh — execute AGENTS.md steps one after another, each in a FRESH
# pi/ollama context. Never runs two agents/models concurrently (flock +
# OLLAMA_KEEP_ALIVE=0 so the model fully unloads before the next step).
#
# Usage:
#   ./run-steps.sh              # run steps 1..8
#   START=3 END=4 ./run-steps.sh   # only steps 3..4 (e.g. a re-run)
#   DRY_RUN=1 ./run-steps.sh    # print the prompt + command, don't run
set -uo pipefail

REPO_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$REPO_DIR"

# ---------------------------------------------------------------- tunables
: "${MODEL:=srchmnmichael/Qwen3.8-Uncensored:q4_K_M}"   # qwen3.8-uncensored:q4_k_m
: "${CTX:=131072}"                                       # ~130k runner context
: "${STEP_TIMEOUT:=5400}"                                # 90 min per step
: "${START:=1}"
: "${END:=8}"
LOCKFILE="$REPO_DIR/runs/agent.lock"
RUNS="$REPO_DIR/runs"
mkdir -p "$RUNS"

# Model fully unloads between steps -> only one local model ever active.
export OLLAMA_CONTEXT_LENGTH="$CTX"
export OLLAMA_KEEP_ALIVE="0m"
export OLLAMA_NUM_PARALLEL=1

green(){ printf '\033[32m%s\033[0m\n' "$*"; }
red(){   printf '\033[31m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }

prompt_for(){ local n="$1" nx="$((n+1))"; cat <<EOF
Read AGENTS.md top to bottom first. Then read docs/PROGRESS.md if it exists (create a handover from any prior state if present).
You are executing EXACTLY STEP $n of the plan (see the section "STEP $n —").

Rules:
- Do only STEP $n's scope. If an earlier step's contract seems wrong, fix it
  minimally and note the deviation in docs/PROGRESS.md.
- Work in $REPO_DIR.
- Leave "go build ./...", "go vet ./..." and "go test ./..." GREEN before you finish
  (if STEP $n hasn't introduced Go yet, the exit criteria in AGENTS.md rule).
- Before finishing, append a new dated "### STEP $n <date> <ok|blocked>" section to
  docs/PROGRESS.md summarizing: what was built, files touched, test results,
  exit-criteria status (met / not met + why), and a 3-line handover note for
  STEP $nx.
- If something blocks you and you cannot finish within your session, still write
  that PROGRESS.md entry honestly and say "BLOCKED" in your final message.
- Final message: one short paragraph: status + what the next step should verify first.
EOF
}

for n in $(seq "$START" "$END"); do
  SESSION="step-$n-$RANDOM"
  LOG="$RUNS/step-$n.out"
  PROMPT="$(prompt_for "$n")"

  echo "============================================================"
  green "STEP $n  (fresh context, model: $MODEL, ctx: $CTX, thinking: high)"

  if [ "${DRY_RUN:-0}" = "1" ]; then
    echo "$PROMPT"
    echo "CMD: pi --provider ollama --model $MODEL --thinking high -p \"...\""
    continue
  fi

  CMD=(flock "$LOCKFILE"
       env timeout "$STEP_TIMEOUT"
       pi --provider ollama --model "$MODEL" --thinking high
          --approve
          --session-id "$SESSION"
          -p "$PROMPT")

  "${CMD[@]}" > "$LOG" 2>&1
  rc=$?

  echo
  if [ $rc -eq 124 ]; then
    red "STEP $n TIMED OUT after ${STEP_TIMEOUT}s (exit 124)"
  elif [ $rc -ne 0 ]; then
    red "STEP $n failed (exit $rc)"
  else
    green "STEP $n finished (exit 0)"
  fi
  tail -25 "$LOG" | sed 's/^/    | /'

  # Post-step verification: Go must build (steps 1+) — stop the pipeline if broken.
  if [ -f go.mod ]; then
    if go build ./... >/dev/null 2>&1; then
      green "  verify: go build ./... OK"
    else
      red "  verify: go build BROKEN — stopping pipeline. Inspect $LOG and $REPO_DIR/docs/PROGRESS.md"
      break
    fi
    if go test ./... >/dev/null 2>&1; then
      green "  verify: go test ./... OK"
    else
      yellow "  verify: go test has failures (may be acceptable mid-step; read PROGRESS.md)"
    fi
  fi

  # Fully unload the model before the next agent can start.
  ollama stop "$MODEL" >/dev/null 2>&1 || true
  echo "  model unloaded; next step starts clean."
done

echo "============================================================"
green "pipeline finished (or stopped). Logs: $RUNS/*.out  ·  state: docs/PROGRESS.md"
