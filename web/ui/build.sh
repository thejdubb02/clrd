#!/bin/sh
# Build web/static/app.css from web/ui/app.css with the pinned Tailwind
# standalone CLI. No Node. CI runs this and fails if the output changed.
#   web/ui/build.sh          build (twice, to catch a bad run)
#   web/ui/build.sh --watch  rebuild on save
set -eu
VERSION=4.3.5
cd "$(dirname "$0")"
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)  ASSET=tailwindcss-linux-x64;    SHA=e47695cee3f0d90623a91f3939b08bdee610eb5ad99340adff9d4145b284d6cc ;;
  *) echo "build.sh: add the sha256 for $(uname -s)-$(uname -m) from the v$VERSION release" >&2; exit 1 ;;
esac
BIN="${XDG_CACHE_HOME:-$HOME/.cache}/clrd/tailwindcss-$VERSION"
if [ ! -x "$BIN" ]; then
  mkdir -p "$(dirname "$BIN")"
  curl -fsSL -o "$BIN.tmp" "https://github.com/tailwindlabs/tailwindcss/releases/download/v$VERSION/$ASSET"
  echo "$SHA  $BIN.tmp" | sha256sum -c - >/dev/null
  chmod +x "$BIN.tmp" && mv "$BIN.tmp" "$BIN"
fi
[ $# -eq 0 ] || exec "$BIN" -i app.css -o ../static/app.css --minify "$@"
# Tailwind now and then drops an @apply declaration on a cold or loaded run
# (#665), so keep a result only when two builds in a row agree.
t=$(mktemp -d); trap 'rm -rf "$t"' EXIT
"$BIN" -i app.css -o "$t/a" --minify
for _ in 1 2 3 4; do
  "$BIN" -i app.css -o "$t/b" --minify
  cmp -s "$t/a" "$t/b" && { mv "$t/b" ../static/app.css; exit 0; }
  mv "$t/b" "$t/a"
done
echo "build.sh: Tailwind built a different app.css every time; run it again (#665)" >&2
exit 1
