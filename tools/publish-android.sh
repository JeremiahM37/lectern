#!/usr/bin/env bash
# Builds the signed Android app for a release and attaches it, with the
# manifest the app checks for updates (lectern-android.json), to that GitHub
# release. Run it for every release, after release.yml has published it, so
# installed apps offer the update (docs/android.md, "Updates").
#
#   LECTERN_ANDROID_SIGNING=/path/signing.properties tools/publish-android.sh v2.8.0
#
# The tag must be checked out: the app is built from the same commit as the
# release's binaries and carries its version.
set -euo pipefail
tag=${1:?usage: $0 vX.Y.Z}
version=${tag#v}
repo=JeremiahM37/lectern
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
: "${LECTERN_ANDROID_SIGNING:?set LECTERN_ANDROID_SIGNING to the signing.properties file}"
[[ "$(git rev-parse HEAD)" == "$(git rev-parse "$tag^{commit}")" ]] || { echo "check out $tag first" >&2; exit 1; }
grep -q "Version = \"$version\"" internal/version/version.go || { echo "internal/version/version.go is not $version" >&2; exit 1; }
gh release view "$tag" -R "$repo" >/dev/null
unset LECTERN_UPDATE_MANIFEST LECTERN_UPDATE_APK_PREFIX LECTERN_ANDROID_VERSION LECTERN_APP_LINK_HOSTS

(cd frontend && npm ci --silent && npm run build && python3 scripts/stage.py)
mobile/build-android.sh test
mobile/build-android.sh release

out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
apk="$out/lectern-android-$version.apk"
cp mobile/android/app/build/outputs/apk/release/app-release.apk "$apk"
apksigner=$(ls -d "${ANDROID_HOME:-$HOME/android-sdk}"/build-tools/*/apksigner | sort -V | tail -1)
"$apksigner" verify --print-certs "$apk" | grep -qi "cc438ac9a8c58b582e69b320fbd85038082bad56c37a41f9b6db04d0f2aeba56" \
  || { echo "the APK is not signed with Lectern's release key" >&2; exit 1; }
sum=$(sha256sum "$apk" | cut -d' ' -f1)
IFS=. read -r a b c <<<"$version"
python3 - "$out/lectern-android.json" <<PY
import json, os, sys
json.dump({
    "version": "$version",
    "versionCode": $a * 10000 + $b * 100 + $c,
    "apk": "https://github.com/$repo/releases/download/$tag/lectern-android-$version.apk",
    "sha256": "$sum",
    "size": os.path.getsize("$apk"),
}, open(sys.argv[1], "w"), indent=2)
PY
echo "$sum  lectern-android-$version.apk" > "$apk.sha256"
gh release upload "$tag" -R "$repo" --clobber "$apk" "$apk.sha256" "$out/lectern-android.json"

# What an installed app will read: the latest release's manifest, and the APK it names.
if [[ "$(gh release view -R "$repo" --json tagName -q .tagName)" == "$tag" ]]; then
  got=$(curl -fsSL "https://github.com/$repo/releases/latest/download/lectern-android.json")
  echo "$got"
  url=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["apk"])' "$got")
  [[ "$(curl -fsSL "$url" | sha256sum | cut -d' ' -f1)" == "$sum" ]] && echo "published: $url ($sum)"
else
  echo "note: $tag is not the latest release, so installed apps will not be offered it" >&2
fi
