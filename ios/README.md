# Context Drop

Native Today, Habits, Weights, Food, and History tabs. Today and detail/history screens scroll; Habits and Weights fit the available height and allow scrolling when larger text or smaller screens need it. The app keeps `com.avyay.faceguard` to preserve Keychain setup and recordings across upgrades.

## Native styling

Every screen uses `healthNavigationTitle` for a system-owned inline navigation bar, shared rounded title font and adaptive paper background. Root titles and their actions stay in the bar rather than moving with scroll content; lists, forms and custom scroll views keep the system safe area. No fixed navigation height or manual status-bar inset is used. Active workout actions remain available while scrolling; Rename Workout is in Workout Options. History remains a separate tab. Goal save/cancel, back navigation and keyboard/rest controls retain their native placements.

Navigation source-contract regression: `xcrun swiftc Tests/Navigation/main.swift -o /tmp/contextdrop-navigation-tests && /tmp/contextdrop-navigation-tests Sources`. `Tests/Style/main.swift` also verifies the installed title font. Review root tabs, goal/health sheets and active workouts at the top and while scrolling in both appearances; system sheets have their own safe area, so compare title/action alignment within each presentation rather than forcing a sheet to the full-screen status-bar offset.

Home combines recorded sleep and the saved `target-sleep` record (`kind: target`, `unit: hours`) in Recovery, with the latest recorded night's date, progress and the difference from the goal. Missing sleep is not treated as zero; missing goals do not produce invented defaults or progress. The target remains read-only in Goals and managed in Context Drop; saving food/weight goals does not change it. `Tests/Sleep/main.swift` covers recovery comparisons, missing/invalid data, app-response decoding, cache loading, target filtering and preservation when other goals are saved; it accepts an optional read-only app-response fixture path.

`Sources/HealthStyle.swift` owns the shared adaptive forest/paper palette, opaque secondary text, Dynamic Type typography, page/card spacing, card shape, primary button style, and native navigation appearance. Screens follow the system light/dark setting, including sheets. Native lists, forms, destructive actions, and tab navigation retain system behavior; custom cards and primary actions use the shared components. Keep new styling here rather than adding per-screen color constants or fixed text sizes.

`Tests/Style/main.swift` checks text contrast (at least 4.5:1) in light/dark, normal/increased contrast, and base/elevated surfaces, plus shared spacing and minimum primary-control height. Run it on an isolated simulator; no health records or credentials are needed:

```sh
export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer
SDK=$(xcrun --sdk iphonesimulator --show-sdk-path)
xcrun --sdk iphonesimulator swiftc -sdk "$SDK" -target arm64-apple-ios17.0-simulator \
  Sources/HealthStyle.swift Tests/Style/main.swift -o /tmp/contextdrop-style-tests
xcrun simctl spawn YOUR_SIMULATOR_UDID /tmp/contextdrop-style-tests
```

Use `x86_64` instead of `arm64` on an Intel Mac. Also visually review the five tabs and goal/meal/weight sheets in both appearances and with larger text. Never copy personal records or setup credentials just to run visual checks.

Today reads the existing health.avyayv.com dashboard feed, caches it offline, and refreshes on launch, foregrounding, manually, and every minute while active. The Mac's Oura ingestion runs every five minutes; Oura still must upload ring data through its own app. This release does not implement direct ring history sync or guarantee iOS background HTTP refresh.

Food shows meal cards, individual foods and portions, daily nutrition totals, meal details, and an editable recent-meal picker. Existing imported JSON notes are converted into structured foods; stored nutrition estimates are marked and missing values remain blank. Changing foods or portions clears unchanged nutrition totals to avoid carrying stale values forward.

Food, workout sets, body weight, and custom daily habits save locally before upload. A private authenticated API syncs edits and imports existing local health history. Place private `{ "token": "..." }` in Documents/health-setup.json via device copy; the app imports it into device-only Keychain and removes the file. Never bundle it. Personal logs remain separate from the public dashboard. Raw motion and individual labels stay on the phone; only confirmed daily touch totals are synced.

When tracking was enabled before an app restart, RingMonitor starts a new recording and reconnects automatically. Prior recordings remain marked interrupted rather than claiming uninterrupted coverage. Force-quitting still prevents background execution until iOS permits relaunch or the user opens the app. Bounded connection/lifecycle diagnostics persist in Documents/connection-events.json.

Build output is ContextDrop.app; the installed display name is Context Drop.

## Live labeled recordings

In Habits, tap **Start** to save raw accelerometer frames in a new local session. The existing sensitive classifier flags candidates with its current threshold and ten-second episode cooldown; it is not retrained automatically. Each automatic candidate starts **Unreviewed**. Mark it **Touch** or **Not a Touch**, or undo a label. **Missed Touch — Last 3 Seconds** adds a manually labeled positive example. Notifications also have Touch / Not a Touch actions.

Sessions save full motion streams plus candidate time ranges (three seconds before, up to two seconds after), original classifier probability, model snapshot and threshold. Human labels remain separate from classifier output. Unflagged or unreviewed data is not assumed negative. End a session to save and stop the ring stream; review saved sessions from the Sessions list. A force-quit can lose approximately the last second of buffered motion, and the prior session is marked Interrupted on the next launch. Start a new recording to continue.

Data stays in the app's protected Documents/Recordings directory: one directory per session, containing motion.jsonl and session.json. Frames include arrival wall time, monotonic uptime, sequence, sample-rate byte, and signed axes in g. Raw frame time uses Unix seconds; dates in session.json use Foundation seconds since 2001-01-01 (add 978307200 to compare with raw frame timestamps). Timestamps and sequences allow later gap checks; candidate post-context is limited by actual received data or session end. No ring key is included. Session metadata and label edits are saved atomically; raw motion flushes once per second and on flags/end. The original recovered key remains in Keychain.

Standalone persistence tests are in Tests/RecordingStore/main.swift; compile alongside Sources/RecordingStore.swift, Sources/TouchModel.swift, and Sources/MotionFrame.swift on macOS. They cover raw frame preservation, labels/undo, unreviewed defaults, manual positives, reload, and interrupted sessions.

Personal iPhone prototype that streams the ring accelerometer over Bluetooth and nudges for hair or face touching. Uses the ring key already recovered from the owner's Oura app backup. Hair/face detection itself runs locally. The other health tabs use the dashboard and private log API.

## Build and install

Requires Xcode with iOS support, XcodeGen, a signed-in Apple Account, and a paired iPhone with Developer Mode enabled. The configured team is Avyay's free Personal Team; choose your own team for another account. Free profiles expire after seven days, requiring a rebuild/install.

```sh
xcodegen generate
DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer xcodebuild \
  -project ContextDrop.xcodeproj -scheme ContextDrop -configuration Debug \
  -destination 'id=YOUR_DEVICE_UDID' -derivedDataPath build \
  -allowProvisioningUpdates -allowProvisioningDeviceRegistration build
```

Install `build/Build/Products/Debug-iphoneos/ContextDrop.app` with `xcrun devicectl device install app`. Use `devicectl device copy to` with `--domain-type appDataContainer --domain-identifier com.avyay.faceguard` to transfer the private setup JSON to `Documents/faceguard-setup.json`. On launch the app validates it, stores it in the device-only Keychain, and deletes the import file. Never put the setup in source, app resources, logs, or a public file server.

## Calibration

`scripts/prepare_setup.py PRIVATE_DIRECTORY` fits an eight-feature standardized logistic model from the owner's five 15-second recordings (rest, hair, typing, cheek, hair). It expects the named calibration capture, `oura-app.sqlite`, and `ring-auth-key.hex` in that directory. Run with `uv run --with numpy --with scikit-learn python ...`.

Both hair and cheek are positives. One-second windows, a roughly half-second stride, two high windows before an alert, three low windows to rearm, and a ten-second cooldown reduce repeated nudges. The small calibration is not independent accuracy validation. Eating, drinking, changes in ring orientation, and other everyday motions need additional calibration.

## Behavior

Allow Bluetooth and notifications, turn Bluetooth on, and tap Start Session. The ring may need a brief charger wakeup. The Oura app can compete for the same connection; pause Context Drop to return the connection. Pause explicitly requests the ring stream to stop before disconnecting.

Background Bluetooth and state restoration are enabled. Actual locked-screen continuity needs verification on the physical device; force quitting stops normal restoration. Notification sound follows Silent Mode and Focus. Raw accelerometer streaming consumes additional ring battery. Live streaming requests are bounded to two minutes and renewed while monitoring; do not rely on firmware timeout as a substitute for Pause.

Only a local aggregate `Documents/status.json` is written for USB diagnostics; it contains status, sample count, alert count, and model probability, with no key or raw motion.

## Checks

The standalone Swift test checks Python/Swift model parity, AES authentication, and episode/cooldown behavior. Generate the private model fixture first, then compile `Sources/TouchModel.swift`, `Sources/MotionFrame.swift`, `Sources/RingSetup.swift`, and `Tests/main.swift` with `xcrun swiftc` on the Mac; run with the private `model-parity.json` path.

## Updating from human labels

`scripts/train_labeled_session.py PRIVATE_DIRECTORY SESSION_DIRECTORY --threshold 0.8` creates a candidate model, a Python/Swift parity fixture, and an exploratory evaluation report in the private directory. It merges overlapping same-label clips, rejects conflicting overlaps, ignores unreviewed data, and keeps an entire merged movement out at a time during evaluation. A positive clip labels a touch somewhere in the interval, so its two strongest original-detector windows are used as weak positive examples; negative clips contribute every complete window. The original calibration is retained, with per-event and class-balanced weights.

The first labeled update used session DB49DB66-7C3A-409B-BB5E-93E11CF77428: 15 labels merged into 7 positive and 5 negative movements. At a threshold of 0.8, exploratory leave-one-movement-out checks detected 7/7 positives and flagged 1/5 negatives (original detector: 7/7 and 5/5). The threshold was selected using these same checks, so this is not an independent accuracy estimate. Validate using a new recording session.

The private RingSetup can include an optional `threshold` override. A recording freezes both model and threshold when it starts; importing a newer model affects subsequent recordings, preserving the provenance of existing sessions.

The ring has reported both 49 and 50 in the motion-frame rate byte. Both are accepted with identical axis decoding, and the reported rate is retained in recordings. The classifier uses 49-sample windows (about 1 second at 49 Hz or 0.98 seconds at 50 Hz). Other rates remain unsupported until calibrated.

## Wearable Sleep Refresh

Recovery uses the latest dated night across the Oura-backed dashboard's `latestNight`, `nights`, and `rolling30` records, sorted by day and period. The label shows Oura's wake date, not the phone's current date or the request time. Missing duration remains unknown. Refresh revalidates the dashboard with `Cache-Control: no-cache`; an older generation cannot overwrite a newer saved snapshot. "Last checked" describes the request, not when sleep occurred. Launch, foreground, active-minute polling and pull-to-refresh use this same path. The app cannot retrieve Oura records that the dashboard publisher has not supplied yet; reinstalling does not repair publisher lag.

If a newer publication drops the latest sleep day, Recovery retains that actual saved Oura night and shows a feed warning while accepting other updated dashboard fields. It never invents a new date or duration. A same-day correction is accepted; the current feed has no explicit sleep-deletion signal.

Run `bash Tests/Snapshot/run.sh` on macOS to test dated selection, stale saved snapshots, newer publications missing sleep, refresh/revalidation, network failures, persistence, and date labels with an isolated mocked transport. No live health writes occur.

## Workout sessions (0.3.0)

Weights now has a session workflow, a short animated start (respects Reduce Motion), repeat templates from actual history, searchable/custom exercise entry, individually checked sets, weight/reps, warm-ups, exercise order and notes, lb/kg conversion, assistance/bodyweight modes, timed rest with +30s/skip, and a finished-session summary/history. Today offers Resume Workout when a session is active.

Every draft edit is atomically saved to protected Documents/active-workout.json. The rest timer stores a deadline rather than relying on background timer ticks. Local rest notifications use their own identifier; face alerts only clear other face alerts. Sound depends on existing iOS notification permissions/Focus settings.

Finishing writes workout metadata and checked sets to HealthStore in one atomic batch before clearing the draft. Set IDs remain stable across retries, preventing duplicates after interruption. Incomplete sets are excluded. Completed workouts sync to the same private endpoint; active drafts stay on the phone. Previous records are preserved, and imported assistance amounts remain marked as assistance. Unit/load-type changes are disabled after an exercise has completed sets; undo the sets first to change those settings.

Tests/Workout/main.swift covers draft restart and rest deadlines, valid set completion, atomic finish and failures, retry deduplication, repeat-template values, assisted/bodyweight modes, and corrupt draft preservation. Tests use temporary directories and never create workouts in the live account.

Food > Goals edits daily calories, daily protein and body weight targets. Weights > Body Weight shows the latest dated measurement, goal and weight-history chart. Targets use stable shared-API records of kind `target`, remain separate from actual logs, and are excluded from history rows. Missing nutrition stays unknown; goal progress reflects only logged nutrition. Blank targets remove them. No calorie/protein targets are prescribed automatically.
