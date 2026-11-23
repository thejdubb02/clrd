#!/bin/sh
# Usage: deb.sh BINARY OUT_DIR
# Packs a release build of the Linux app into OUT_DIR as a .deb, then checks
# the package it made. Needs dpkg-dev and desktop-file-utils.
#
# The package exists mostly for its Depends line: the tray library is loaded
# at run time, so nothing but a package can make sure it is installed (#625).
set -eu
umask 022

bin=$1
out=$2
here=$(cd "$(dirname "$0")" && pwd)
version=$(awk -F'"' '/^version = / { print $2; exit }' "$here/../Cargo.toml")
arch=$(dpkg --print-architecture)
deb=$out/clrd-desktop_${version}_$arch.deb

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
root=$work/root

# Its hints go to stderr: stdout is only the path of the finished package.
desktop-file-validate "$here/clrd.desktop" >&2
install -D -m 0755 "$bin" "$root/usr/bin/clrd-desktop"
install -D -m 0644 "$here/clrd.desktop" "$root/usr/share/applications/clrd.desktop"
install -D -m 0644 "$here/../app/icons/icon.png" "$root/usr/share/icons/hicolor/64x64/apps/clrd.png"
# Starts with the desktop session, as a tray icon with no window. A file in
# /etc, so turning it off (or removing it) survives an upgrade.
mkdir -p "$root/etc/xdg/autostart"
sed 's/^Exec=.*/Exec=clrd-desktop --hidden/' "$here/clrd.desktop" > "$root/etc/xdg/autostart/clrd.desktop"

# The libraries the binary links to, with the versions this build needs,
# worked out by dpkg from the binary itself. It wants a debian/control to
# sit next to, even an empty one.
mkdir "$work/debian"
: > "$work/debian/control"
linked=$(cd "$work" && dpkg-shlibdeps -O "$root/usr/bin/clrd-desktop" | sed -n 's/^shlibs:Depends=//p')
[ -n "$linked" ]

mkdir "$root/DEBIAN"
echo /etc/xdg/autostart/clrd.desktop > "$root/DEBIAN/conffiles"
cat > "$root/DEBIAN/control" <<EOF
Package: clrd-desktop
Version: $version
Architecture: $arch
Maintainer: clrd contributors <thejdubb02@users.noreply.github.com>
Installed-Size: $(du -sk --exclude=DEBIAN "$root" | cut -f1)
Depends: $linked, libayatana-appindicator3-1 | libappindicator3-1
Recommends: gnome-keyring | kwallet6 | keepassxc
Section: net
Priority: optional
Homepage: https://github.com/thejdubb02/clrd
Description: clrd desktop app
 A tray icon and an inbox window for a clrd notification server. Clearing a
 notification here clears it on every device, and the other way round.
EOF

mkdir -p "$out"
# xz, not the newer default: older Debian cannot open a zstd package.
dpkg-deb --root-owner-group -Zxz --build "$root" "$deb" > /dev/null

# What a user's machine will see, read back from the finished file.
fail() {
  echo "deb.sh: $1" >&2
  exit 1
}
depends=$(dpkg-deb -f "$deb" Depends)
for lib in libwebkit2gtk-4.1-0 libgtk-3-0 libayatana-appindicator3-1 libc6; do
  case $depends in *"$lib"*) ;; *) fail "Depends does not name $lib: $depends" ;; esac
done
# Linking needs a keychain (Secret Service). Only recommended: a desktop may
# bring its own under a package name not listed here.
case $(dpkg-deb -f "$deb" Recommends) in gnome-keyring*) ;; *) fail "Recommends does not name a keychain" ;; esac
files=$(dpkg-deb -c "$deb")
has() {
  echo "$files" | grep -Eq "^$1 root/root .* \./$2\$" || fail "missing, or not $1 root/root: $2"
}
has -rwxr-xr-x usr/bin/clrd-desktop
has -rw-r--r-- usr/share/applications/clrd.desktop
has -rw-r--r-- usr/share/icons/hicolor/64x64/apps/clrd.png
has -rw-r--r-- etc/xdg/autostart/clrd.desktop
[ "$(dpkg-deb --ctrl-tarfile "$deb" | tar -xO ./conffiles)" = /etc/xdg/autostart/clrd.desktop ] ||
  fail "the autostart entry is not a conffile"
dpkg-deb --fsys-tarfile "$deb" | tar -xO ./etc/xdg/autostart/clrd.desktop |
  grep -qx 'Exec=clrd-desktop --hidden' || fail "autostart does not start hidden"
dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/share/applications/clrd.desktop |
  grep -qx 'Exec=clrd-desktop' || fail "the menu entry does not open the window"

echo "$deb"
