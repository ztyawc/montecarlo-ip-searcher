#!/usr/bin/env bash
set -euo pipefail

repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repository_root"
flutter_root=${FLUTTER_ROOT:-$(dirname "$(dirname "$(readlink -f "$(command -v flutter)")")")}
android_root=${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}
android_jar="$android_root/platforms/${ANDROID_PLATFORM:-android-36}/android.jar"
flutter_jar="$flutter_root/bin/cache/artifacts/engine/android-arm64-release/flutter.jar"
if [[ ! -f "$android_jar" || ! -f "$flutter_jar" ]]; then
  echo 'Android platform and Flutter Android release artifacts must be installed before running native tests.' >&2
  exit 1
fi

test_output=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/mcis-native-tests.XXXXXX")
trap 'rm -rf "$test_output"' EXIT
classpath="$android_jar:$flutter_jar"
javac --release 17 -cp "$classpath" -d "$test_output" \
  android-app/app/src/main/java/com/ztyawc/mcis/RunSession.java \
  android-app/app/src/main/java/com/ztyawc/mcis/ScanResults.java \
  flutter-app/android/app/src/main/java/com/ztyawc/mcis/ScanDiagnostics.java \
  flutter-app/android/app/src/main/java/com/ztyawc/mcis/NativeScanner.java \
  android-app/tests/com/ztyawc/mcis/RunSessionTest.java \
  android-app/tests/com/ztyawc/mcis/ScanResultsTest.java \
  flutter-app/android/tests/com/ztyawc/mcis/NativeConfigTest.java \
  flutter-app/android/tests/com/ztyawc/mcis/ScanDiagnosticsTest.java
for test_class in RunSessionTest ScanResultsTest NativeConfigTest ScanDiagnosticsTest; do
  java -cp "$test_output:$classpath" "com.ztyawc.mcis.$test_class"
done
