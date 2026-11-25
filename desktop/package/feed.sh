#!/bin/sh
# Usage: feed.sh INSTALLER PACKAGE_URL OUT_DIR
# Signs a Windows installer for the app's updater (#719) and writes the update
# feed for it: OUT_DIR gets a copy of the installer, its .sig and latest.json.
# PACKAGE_URL is the address the installer will be downloaded from; the feed
# and the installer may live at different addresses.
#
# The private key comes from TAURI_SIGNING_PRIVATE_KEY and
# TAURI_SIGNING_PRIVATE_KEY_PASSWORD, as `cargo tauri signer` reads them. It
# is never written anywhere by this script. Needs tauri-cli and jq.
set -eu

installer=$1
url=$2
out=$3
name=$(basename "$installer")
# The version is in the name the bundler gives the installer.
version=${name#clrd_}
version=${version%_x64-setup.exe}
case $version in
  *[!0-9.]* | "" ) echo "feed.sh: no version in $name" >&2; exit 1 ;;
esac

mkdir -p "$out"
cp "$installer" "$out/$name"
# The version goes into the signed comment: the app refuses a signature made
# for any other version than the one the feed names.
cargo tauri signer sign --app-version "$version" "$out/$name" >&2
jq -n --arg v "$version" --arg s "$(cat "$out/$name.sig")" --arg u "$url" \
  '{version: $v, platforms: {"windows-x86_64": {signature: $s, url: $u}}}' > "$out/latest.json"
