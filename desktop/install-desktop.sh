#!/bin/sh
# Install a menu shortcut pointing to this extracted app directory.
set -eu
application=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
menu_dir="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
mkdir -p "$menu_dir"
escape_exec() { printf '%s' "$1" | sed 's/[\\"`$]/\\&/g; s/%/%%/g' | sed 's/\\/\\\\/g'; }
icon_path=$(printf '%s' "$application/icon.png" | sed 's/\\/\\\\/g')
cat > "$menu_dir/local.ytdownloader.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=YT Downloader
Comment=Download videos to MP4
Exec="$(escape_exec "$application/YT Downloader")"
Icon=$icon_path
Terminal=false
Categories=AudioVideo;Network;
DESKTOP
printf '%s\n' 'YT Downloader 已加入应用菜单。'
