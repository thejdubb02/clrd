# clrd for Android

The Android client for clrd. It keeps one connection open to your clrd server, shows each open notification in the system tray, and clears it from the tray the moment it is dismissed or resolved anywhere else.

Package: `me.itsjustin.clrd`. Kotlin, Jetpack Compose, minSdk 26 (Android 8.0), targetSdk 35.

## Build

Needs JDK 17 and an Android SDK with platform 35 and build-tools 35.

```bash
export ANDROID_HOME=/path/to/android-sdk
./gradlew assembleDebug testDebugUnitTest   # debug APK + unit tests
./gradlew assembleRelease                   # unsigned release APK
```

APKs land in `app/build/outputs/apk/`.

## How it works

- `SyncService` is a foreground service holding a WebSocket to `/api/v1/ws?since=<last seq>` (docs/SPEC.md section 3). It reconnects with jittered exponential backoff (1 s up to 60 s) and right away when the network comes back.
- Events replayed after a reconnect are merged per notification until the server sends `ready`, so a phone that was offline does not buzz for alerts that were already cleared elsewhere.
- Rows live in a Room database with the last applied seq, written in the same transaction.
- Every tap (Ack, Snooze 1h, Clear, swipe) is saved to a local queue first, applied on screen at once, then sent. Anything that could not be sent goes out before the next WebSocket connect, so an action taken offline is not lost.
- Each open notification is one system notification with `tag` = notification id. Swiping it away calls the server's `dismiss`. A `dismissed` or `resolved` event from the server cancels it.
- Channels by priority: p1 min, p2 low, p3 default, p4 and p5 high.

## Choices worth knowing

- **Foreground service type `specialUse`.** `dataSync` is capped at 6 hours a day on Android 15 and cannot start at boot, and clrd needs to be connected all the time. The F-Droid build is not subject to Play policy; a Play build would need to justify this type.
- **Plain http is allowed** so home servers on a LAN work. Settings warns when the URL is `http://`, since the token is then sent unencrypted.
- **No backup of app data.** The server token stays on the phone: `allowBackup` is off and the data extraction rules exclude everything.
- **No tracking.** No analytics, no crash reporting, no Google Play Services, no Firebase. The only network peer is the server you configure.

## Reproducible builds

- Gradle wrapper 8.11.1 with a pinned `distributionSha256Sum`. The wrapper jar matches the official checksum for that version.
- All plugin and library versions are fixed in the build files (no `+` or dynamic versions).
- `dependenciesInfo` is turned off, so the APK carries no Google-encrypted dependency blob.
- Release builds are not minified, which removes R8 as a source of drift.
- No build timestamps or git hashes are written into the APK.
- Fonts (Geist, SIL Open Font License, see `licenses/Geist-OFL.txt`) are committed, not downloaded at build time.

F-Droid's own reproducibility check has not been run yet.

## Not done yet

- Never run on a real device or emulator. All behaviour above is covered by JVM unit tests against a fake server, not by on-device testing.
- Sender action buttons are stored but not shown in the tray yet.
- The tray shows at most the 40 newest open notifications after a full refresh.
- QR pairing, UnifiedPush and translations are later work.
