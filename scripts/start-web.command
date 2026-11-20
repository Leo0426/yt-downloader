#!/bin/sh
set -eu
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
if [ -x "$script_dir/yt-downloader" ]; then
  exec "$script_dir/yt-downloader" -web "$@"
fi
cd "$script_dir/.."
exec go run . -web "$@"
