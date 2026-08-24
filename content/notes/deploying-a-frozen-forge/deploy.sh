#!/usr/bin/env bash
# Ship dist/ to a server with an atomic swap: rsync into a fresh timestamped directory,
# then repoint the "current" symlink. A visitor mid-request never sees a half-uploaded site,
# and rolling back is one `ln -sfn` away.
#
#   ./deploy.sh forge@example.com:/srv/forge
set -euo pipefail

TARGET="${1:?usage: deploy.sh user@host:/srv/path}"
HOST="${TARGET%%:*}"
ROOT="${TARGET#*:}"
RELEASE="$(date -u +%Y%m%dT%H%M%SZ)"
KEEP="${KEEP:-5}"

[ -f dist/index.html ] || { echo "dist/ not built — run: npm run build" >&2; exit 1; }

echo "==> uploading release $RELEASE"
rsync -az --delete \
  --chmod=D755,F644 \
  dist/ "$HOST:$ROOT/releases/$RELEASE/"

echo "==> activating"
ssh "$HOST" bash -euo pipefail -s -- "$ROOT" "$RELEASE" "$KEEP" <<'REMOTE'
root=$1; release=$2; keep=$3
ln -sfn "$root/releases/$release" "$root/current.new"
mv -Tf "$root/current.new" "$root/current"   # rename(2): atomic on the same filesystem
cd "$root/releases"
ls -1 | sort -r | tail -n "+$((keep + 1))" | xargs -r rm -rf
REMOTE

echo "==> live: $RELEASE"
