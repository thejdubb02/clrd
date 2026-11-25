#!/bin/sh
# Usage: size-check.sh NAME FILE [EXEMPT]
# Fails if FILE is bigger than NAME's budget in .github/size-budget (#298),
# and prints the size to the job summary on every run. EXEMPT is a file
# FILE embeds whose growth never counts against the budget, like the
# changelog (#683): its size is taken off. It has its own cap.
set -eu
name=$1 file=$2
budget=$(awk -v n="$name" '$1 == n { print $2 }' "$(dirname "$0")/size-budget")
case $budget in '' | *[!0-9]*) echo "no single whole-byte budget for $name in .github/size-budget" >&2; exit 1 ;; esac
size=$(wc -c < "$file")
note=
if [ $# -gt 2 ]; then size=$((size - $(wc -c < "$3"))) note=", not counting $3"; fi
mb() { awk -v b="$1" 'BEGIN { printf "%.2f MB", b / 1000000 }'; }
line="$name: $(mb "$size") ($size bytes$note), budget $(mb "$budget")"
echo "$line"
echo "$line" >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
if [ "$size" -gt "$budget" ]; then
  echo "$name is over its size budget. Make it smaller, or raise the budget in .github/size-budget in a reviewed change." >&2
  exit 1
fi
