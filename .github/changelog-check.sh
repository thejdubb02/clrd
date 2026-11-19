#!/bin/sh
# Fails when a pull request changes server/, web/, android/ or sdk/ but not
# CHANGELOG.md. Usage: changelog-check.sh <base ref>
set -eu
files=$(git diff --no-renames --name-only "$1"...HEAD)
echo "$files" | grep -qE '^(server|web|android|sdk)/' || exit 0
echo "$files" | grep -qxF CHANGELOG.md && exit 0
echo "This pull request changes server/, web/, android/ or sdk/ but not CHANGELOG.md."
echo "Add a line under Unreleased, or add the no-changelog label if no user would notice the change."
exit 1
