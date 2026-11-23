#!/bin/sh
# CI: web/static/app.css must be what build.sh makes from the committed
# sources (#599). Run from anywhere in a git checkout.
#
# Two things made the old `build.sh && git diff` check flaky:
# - The Tailwind binary leaves the step's stdout non-blocking, so a big
#   `git diff` after it could die with EAGAIN and lose the log. The build here
#   writes to a file and the check compares hashes, not diff output.
# - The pinned Tailwind sometimes emits a different, equally valid file for the
#   same sources. On a mismatch the check builds again from a clean
#   `git archive HEAD`; only a second mismatch fails it.
# Files that differ land in $OUT (default $RUNNER_TEMP/app-css) for the
# workflow to upload.
set -eu
cd "$(git rev-parse --show-toplevel)"
OUT="${OUT:-${RUNNER_TEMP:-/tmp}/app-css}"
want=$(git rev-parse HEAD:web/static/app.css)

build() { # build the tree in $1 and print the hash of its app.css
  "$1/web/ui/build.sh" </dev/null >"$OUT/build.log" 2>&1 || { cat "$OUT/build.log" >&2; exit 1; }
  git hash-object "$1/web/static/app.css"
}

mkdir -p "$OUT"
got=$(build .)
if [ "$got" = "$want" ]; then
  rm -rf "$OUT"
  echo "app.css matches its source ($want)"
  exit 0
fi
cp web/static/app.css "$OUT/app.css.in-tree"

clean=$(mktemp -d)
trap 'rm -rf "$clean"' EXIT
git archive HEAD | tar -x -C "$clean"
again=$(build "$clean")
cp "$clean/web/static/app.css" "$OUT/app.css.clean"
echo "committed $want, in-tree build $got, clean build $again"

if [ "$again" = "$want" ]; then
  git checkout -- web/static/app.css # the binary embeds the committed file
  echo "::warning::app.css: the in-tree build differed but a clean build matches the committed file (#599)"
  exit 0
fi
echo "web/static/app.css is stale against this commit. On a pull request that commit is the merge with main: merge main, run web/ui/build.sh and commit app.css."
exit 1
