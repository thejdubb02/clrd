; clrd starts with Windows, in the tray with no window, as it does with the
; desktop session on Linux. The user turns that off in Settings > Apps >
; Startup, which Windows keeps apart from this value, so writing it again
; never turns it back on.
;
; Not written on an update, so an entry someone removed stays removed. Not
; written at all when a file named no-autostart is in the app's settings
; folder: that is how a setup script says no before the first install.
;
; The uninstaller already removes the value (Tauri's own step, by name).
!macro NSIS_HOOK_POSTINSTALL
  ${If} $UpdateMode <> 1
  ${AndIfNot} ${FileExists} "$APPDATA\${BUNDLEID}\no-autostart"
    WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${PRODUCTNAME}" '"$INSTDIR\${MAINBINARYNAME}.exe" --hidden'
  ${EndIf}

  ; clrd:// addresses open clrd: "Open in the clrd app" on the server's link
  ; page. The app takes the address as one argument and acts on nothing in it
  ; until the user presses Link this computer. Written on every run, updates
  ; too, so a build from before this gets it.
  WriteRegStr HKCU "Software\Classes\clrd" "" "URL:clrd"
  WriteRegStr HKCU "Software\Classes\clrd" "URL Protocol" ""
  WriteRegStr HKCU "Software\Classes\clrd\shell\open\command" "" '"$INSTDIR\${MAINBINARYNAME}.exe" "%1"'
!macroend

!macro NSIS_HOOK_POSTUNINSTALL
  ${If} $UpdateMode <> 1
    DeleteRegKey HKCU "Software\Classes\clrd"
  ${EndIf}
!macroend
