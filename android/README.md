# clrd for Android

The Android client for clrd. It keeps one connection open to your clrd server, shows each open notification in the system tray, and clears it from the tray the moment it is dismissed or resolved anywhere else.

Package: `me.itsjustin.clrd`. Kotlin, Jetpack Compose, minSdk 26 (Android 8.0), targetSdk 35.

## Build

Needs JDK 17 and an Android SDK with platform 35 and build-tools 35.

```bash
export ANDROID_HOME=/path/to/android-sdk
./gradlew assembleDebug testDebugUnitTest   # debug APK + unit tests
./gradlew verifyRoborazziDebug              # unit tests + JVM screenshot checks
./gradlew recordRoborazziDebug --tests "me.itsjustin.clrd.ScreensTest" # refresh android/screenshots/
./gradlew assembleRelease                   # unsigned release APK
```

APKs land in `app/build/outputs/apk/`.

## Install from the clrd F-Droid repo

Every change to the app on `main` is built, signed and published to an F-Droid repo. To get it and its updates:

1. In the F-Droid app, open Settings, then Repositories, and tap +.
2. Enter `https://raw.githubusercontent.com/thejdubb02/clrd/fdroid/fdroid/repo`.
3. Search for clrd and install it.

These builds are signed with the project's own key, not a debug key and not F-Droid's key. Android will not update an app across signing keys, so uninstall any debug build (or a build from elsewhere) before installing from this repo. Published builds carry the short git commit in their version name (for example `0.1.0-85ba447`) and use the commit count as their version code; a local `./gradlew assembleRelease` with no extra flags does neither.

## How it works

- `SyncService` is a foreground service holding a WebSocket to `/api/v1/ws?since=<last seq>` (docs/SPEC.md section 3). It reconnects with jittered exponential backoff (1 s up to 60 s) and right away when the network comes back.
- Events replayed after a reconnect are merged per notification until the server sends `ready`, so a phone that was offline does not buzz for alerts that were already cleared elsewhere.
- Rows live in a Room database with the last applied seq, written in the same transaction.
- Every tap (Ack, Snooze, Clear, swipe) is saved to a local queue first, applied on screen at once, then sent. Anything that could not be sent goes out before the next WebSocket connect, so an action taken offline is not lost.
- Each open notification is one system notification with `tag` = notification id. Swiping it away only hides it on this phone (#265). A `dismissed` or `resolved` event from the server cancels it.
- Channels by priority: p1 min, p2 low, p3 default, p4 and p5 high.
- Sound (Settings, docs/SPEC.md section 2.2): Android fixes a channel's sound when it is created, so each sound has its own set of channels (`p3-chime`, `p5-alert`). `default` keeps the plain `p1` to `p5`, so phones that never pick a sound see no change. Picking a sound creates its channels, deletes the others and reposts open notifications quietly. The six sound files are copied in from `assets/sounds` at build time. If the phone's own notification settings changed the sound or turned a channel down, Settings says so and links there.

## Choices worth knowing

- **Foreground service type `specialUse`.** `dataSync` is capped at 6 hours a day on Android 15 and cannot start at boot, and clrd needs to be connected all the time. The F-Droid build is not subject to Play policy; a Play build would need to justify this type.
- **Plain http is allowed** so home servers on a LAN work. Settings warns when the URL is `http://`, since the token is then sent unencrypted.
- **No backup of app data.** The server token stays on the phone: `allowBackup` is off and the data extraction rules exclude everything.
- **Changing server or token starts clean.** Cached rows, the replay position and any actions not yet sent are dropped, so nothing from one server reaches another.
- **No tracking.** No analytics, no crash reporting, no Google Play Services, no Firebase. The only network peer is the server you configure.

## Reproducible builds

- Gradle wrapper 8.11.1 with a pinned `distributionSha256Sum`. The wrapper jar matches the official checksum for that version.
- All plugin and library versions are fixed in the build files (no `+` or dynamic versions).
- `dependenciesInfo` is turned off, so the APK carries no Google-encrypted dependency blob.
- Release builds are not minified, which removes R8 as a source of drift.
- No build timestamps or git hashes are written into the APK.
- Fonts (Geist, SIL Open Font License, see `licenses/Geist-OFL.txt`) are committed, not downloaded at build time.

Clean builds (`./gradlew clean assembleRelease`) on two separate Linux machines (JDK 17, SDK 35) produced byte-identical unsigned release APKs. An incremental build on top of an older one can differ, so always build release APKs from clean. F-Droid's own reproducibility check has not been run yet.

## Not done yet

- Never run on a real device or emulator. All behaviour above is covered by JVM unit tests against a fake server, not by on-device testing.
- Sender action buttons are stored but not shown in the tray yet.
- The tray shows at most the 40 newest open notifications after a full refresh.
- QR pairing, UnifiedPush and translations are later work.
