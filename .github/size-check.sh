#!/bin/sh
# Usage: size-check.sh NAME FILE
# Fails if FILE is bigger than NAME's budget in .github/size-budget (#298),
# and prints the size to the job summary on every run.
set -eu
name=$1 file=$2
budget=$(awk -v n="$name" '$1 == n { print $2 }' "$(dirname "$0")/size-budget")
[ -n "$budget" ] || { echo "no budget for $name in .github/size-budget" >&2; exit 1; }
size=$(wc -c < "$file")
mb() { awk -v b="$1" 'BEGIN { printf "%.2f MB", b / 1000000 }'; }
line="$name: $(mb "$size") ($size bytes), budget $(mb "$budget")"
echo "$line"
echo "$line" >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
if [ "$size" -gt "$budget" ]; then
  echo "$name is over its size budget. Make it smaller, or raise the budget in .github/size-budget in a reviewed change." >&2
  exit 1
fi
