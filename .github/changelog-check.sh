#!/bin/sh
# App changes need a new fragment unless the workflow grants no-changelog.
# Usage: changelog-check.sh <base ref>
set -eu
base=$1
files=$(git diff --no-renames --name-only "$base"...HEAD)
echo "$files" | grep -qE '^(server|web|android|sdk)/' || exit 0

if echo "$files" | grep -qxF CHANGELOG.md; then
  # The migration itself runs against a base that still uses inline entries.
  if ! git cat-file -e "$base:changelog.d/README.md" 2>/dev/null; then
    exit 0
  fi
  echo "Move this pull request's Unreleased entry from CHANGELOG.md into its own file under changelog.d/."
  echo "Keep the base version of CHANGELOG.md and see changelog.d/README.md for the file format."
  exit 1
fi

added=$(git diff --no-renames --diff-filter=A --name-only "$base"...HEAD -- changelog.d)
fragments=$(echo "$added" | grep -E '^changelog\.d/(added|changed|fixed|removed|security)/[a-zA-Z0-9][a-zA-Z0-9_-]*\.md$' || :)
if [ -z "$fragments" ]; then
  echo "This pull request changes server/, web/, android/ or sdk/ without a new changelog fragment."
  echo "Add one file under changelog.d/ (see its README), or apply no-changelog if the change is internal."
  exit 1
fi

printf '%s\n' "$fragments" | while IFS= read -r file; do
  if [ -L "$file" ] || ! awk '
    NR == 1 && /^- .*[^[:space:]]/ { bullet = 1; next }
    NR > 1 && /^  .*[^[:space:]]/ { next }
    { exit 1 }
    END { if (!bullet) exit 1 }
  ' "$file"; then
    echo "$file must contain one Markdown bullet and optional lines indented by two spaces."
    exit 1
  fi
done
