#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/../.."
export DEVELOPER_DIR="${DEVELOPER_DIR:-/Applications/Xcode.app/Contents/Developer}"
output=$(mktemp -d /tmp/contextdrop-snapshot-tests.XXXXXX)
trap 'rm -rf "$output"' EXIT
xcrun swiftc -parse-as-library \
  Sources/HealthStore.swift Sources/HealthData.swift Sources/FoodData.swift \
  Sources/WorkoutNaming.swift Sources/WorkoutModels.swift Sources/WorkoutStore.swift \
  Sources/RecordingStore.swift Sources/TouchModel.swift Sources/MotionFrame.swift \
  Sources/HabitSummary.swift Tests/Snapshot/main.swift -o "$output/tests"
"$output/tests"
