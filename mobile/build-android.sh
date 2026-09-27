#!/usr/bin/env bash
# Builds the Lectern Android app (docs/android.md).
#
#   mobile/build-android.sh            debug APK
#   mobile/build-android.sh release    release APK, signed when
#                                      LECTERN_ANDROID_SIGNING points at a
#                                      signing.properties file
#   mobile/build-android.sh test       JVM unit tests
#
# The APK bundles web/index.html and web/static exactly as staged for the Go
# binary, so build and stage the frontend first when it changed:
#   (cd frontend && npm run build && python3 scripts/stage.py)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
: "${ANDROID_HOME:=${ANDROID_SDK_ROOT:-$HOME/android-sdk}}"
export ANDROID_HOME
# Gradle's caches are large; keep them wherever the caller says (this host
# uses /mnt/bulk), defaulting to Gradle's own ~/.gradle.
[[ -n "${LECTERN_GRADLE_HOME:-}" ]] && export GRADLE_USER_HOME=$LECTERN_GRADLE_HOME
cd "$here/android"
[[ -f local.properties ]] || echo "sdk.dir=$ANDROID_HOME" > local.properties

case "${1:-debug}" in
  debug) ./gradlew --console=plain assembleDebug; out=app/build/outputs/apk/debug ;;
  release)
    if [[ -z "${LECTERN_ANDROID_SIGNING:-}" ]]; then
      echo "LECTERN_ANDROID_SIGNING is not set: the release APK will be unsigned" >&2
    fi
    ./gradlew --console=plain assembleRelease; out=app/build/outputs/apk/release ;;
  test) exec ./gradlew --console=plain testDebugUnitTest ;;
  *) echo "usage: $0 [debug|release|test]" >&2; exit 2 ;;
esac
ls -l "$out"/*.apk
