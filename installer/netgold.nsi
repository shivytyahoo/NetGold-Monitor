; NetGold Monitor — Windows installer script (NSIS 3.x)
;
; Builds dist/NetGold-Monitor-Setup-v3.0.3.exe:
;   makensis installer/netgold.nsi        (run from the repository root)
;
; The installer is deliberately plain and transparent: no packing,
; no bundled third-party tools, no background downloads. It carries
; the program, its documentation, and Microsoft's SqlLocalDB.msi
; (the same pinned file NetGold's engine setup verifies) so that
; backup (.bak) imports also work on machines with no internet.
;
; Running-copy rule (v3.0.3): the check for a running copy of the
; program, and the stop that follows it, identify the program ONLY
; by its exact image name, the exact install path being replaced,
; and a process ID other than this setup program's own. No prefix
; or wildcard process matching is used anywhere in this script.

Unicode true
SetCompressor lzma

!define APPNAME "NetGold Monitor"
!define APPVERSION "3.7.1"
!define PUBLISHER "Shiv"
!define EXENAME "NetGoldMonitor.exe"
!define ARPKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\NetGoldMonitor"

Name "${APPNAME}"
Caption "${APPNAME} ${APPVERSION} Setup"
OutFile "..\dist\NetGold-Monitor-Setup-v${APPVERSION}.exe"
InstallDir "$PROGRAMFILES64\NetGold Monitor"
InstallDirRegKey HKLM "Software\NetGold Monitor" "InstallLocation"
RequestExecutionLevel admin
BrandingText "NetGold Monitor — Developed by ${PUBLISHER}"

VIProductVersion "3.7.1.0"
VIAddVersionKey "ProductName" "NetGold Monitor"
VIAddVersionKey "ProductVersion" "3.7.1"
VIAddVersionKey "FileVersion" "3.7.1"
VIAddVersionKey "CompanyName" "Shiv"
VIAddVersionKey "Publisher" "Shiv"
VIAddVersionKey "FileDescription" "NetGold Monitor Setup"
VIAddVersionKey "LegalCopyright" "Copyright (c) 2026 Shiv"
VIAddVersionKey "OriginalFilename" "NetGold-Monitor-Setup-v3.7.1.exe"

!include "MUI2.nsh"

!define MUI_ICON "..\branding\netgold.ico"
!define MUI_UNICON "..\branding\netgold.ico"
!define MUI_WELCOMEPAGE_TITLE "Welcome to NetGold Monitor Setup"
!define MUI_WELCOMEPAGE_TEXT "This will install NetGold Monitor 3.7.1 on your computer.$\r$\n$\r$\nNetGold Monitor is a free, open-source network monitoring program (ping, SNMP, bandwidth graphs, NetFlow, syslog and legacy database import), developed by Shiv.$\r$\n$\r$\nThe setup also carries Microsoft's SQL Server LocalDB installer, so importing the legacy system backup file (.bak) works even without an internet connection.$\r$\n$\r$\nClick Next to continue."
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXENAME}"
!define MUI_FINISHPAGE_RUN_TEXT "Run NetGold Monitor"
!define MUI_FINISHPAGE_TEXT "NetGold Monitor has been installed.$\r$\n$\r$\nThe program runs quietly in the system tray — no console window. It opens in your browser when it starts, and you can reopen it or exit it from its tray icon at any time."

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; This setup/uninstall process's own ID, captured before any
; running-copy check so the check can always exclude it.
Var NetGoldSelfPid

; ----------------------------------------------------------------
; Main section: program + bundled SQL engine installer + shortcuts
; ----------------------------------------------------------------
Section "NetGold Monitor program" SecMain
  SectionIn RO

  ; Offer to stop a running copy first, so its files can be replaced.
  ; A process counts as "the running program" only when ALL of these
  ; hold: its image name is exactly ${EXENAME} (an equality test,
  ; never a pattern), it is running from the exact path being
  ; replaced ($INSTDIR\${EXENAME}), and its process ID is not this
  ; setup program's own. Setup's own file name merely starts with
  ; the same product name, which is not a match under these rules,
  ; so setup can never detect or stop itself.
  System::Call 'kernel32::GetCurrentProcessId() i .r0'
  StrCpy $NetGoldSelfPid $0
  nsExec::ExecToStack 'powershell -NoProfile -Command "if (Get-CimInstance Win32_Process | Where-Object Name -eq $\"${EXENAME}$\" | Where-Object ProcessId -ne $NetGoldSelfPid | Where-Object ExecutablePath -eq $\"$INSTDIR\${EXENAME}$\") { exit 0 } else { exit 1 }"'
  Pop $0 ; exit code: "0" = the installed program is running
  ${If} $0 == "0"
    MessageBox MB_YESNO|MB_ICONQUESTION "NetGold Monitor appears to be running. Setup should stop it before replacing its files.$\r$\n$\r$\nStop the running NetGold Monitor now?" IDNO skipStop
      nsExec::Exec 'net stop NetGoldMonitor'
      nsExec::ExecToStack 'powershell -NoProfile -Command "Stop-Process -Id (Get-CimInstance Win32_Process | Where-Object Name -eq $\"${EXENAME}$\" | Where-Object ProcessId -ne $NetGoldSelfPid | Where-Object ExecutablePath -eq $\"$INSTDIR\${EXENAME}$\" | Select-Object -ExpandProperty ProcessId) -Force -ErrorAction SilentlyContinue; Start-Sleep -Seconds 3; if (Get-CimInstance Win32_Process | Where-Object Name -eq $\"${EXENAME}$\" | Where-Object ProcessId -ne $NetGoldSelfPid | Where-Object ExecutablePath -eq $\"$INSTDIR\${EXENAME}$\") { exit 2 } else { exit 0 }"'
      Pop $0 ; exit code: "0" = stopped, "2" = still running
      ${If} $0 == "2"
        MessageBox MB_OK|MB_ICONEXCLAMATION "NetGold Monitor is still running, so Setup cannot replace its files.$\r$\n$\r$\nPlease exit NetGold Monitor from its tray icon (right-click the NetGold icon near the clock and choose Exit), then run Setup again."
        Abort
      ${EndIf}
    skipStop:
  ${EndIf}

  SetOutPath "$INSTDIR"
  File "/oname=${EXENAME}" "..\dist\NetGold-Monitor-v${APPVERSION}.exe"

  ; The bundled SQL engine installer (Microsoft SqlLocalDB.msi) —
  ; used by NetGold's engine setup when the download is unavailable.
  SetOutPath "$INSTDIR\engine"
  File "..\build-cache\SqlLocalDB.msi"

  SetOutPath "$INSTDIR"
  File "..\README.md"
  File "..\LICENSE"

  WriteUninstaller "$INSTDIR\Uninstall.exe"

  ; Start Menu shortcuts.
  CreateDirectory "$SMPROGRAMS\NetGold Monitor"
  CreateShortcut "$SMPROGRAMS\NetGold Monitor\NetGold Monitor.lnk" "$INSTDIR\${EXENAME}" "" "$INSTDIR\${EXENAME}" 0
  CreateShortcut "$SMPROGRAMS\NetGold Monitor\Uninstall NetGold Monitor.lnk" "$INSTDIR\Uninstall.exe" "" "$INSTDIR\Uninstall.exe" 0

  ; Add/Remove Programs entry.
  WriteRegStr HKLM "${ARPKEY}" "DisplayName" "NetGold Monitor"
  WriteRegStr HKLM "${ARPKEY}" "DisplayVersion" "3.7.1"
  WriteRegStr HKLM "${ARPKEY}" "Publisher" "Shiv"
  WriteRegStr HKLM "${ARPKEY}" "DisplayIcon" "$INSTDIR\${EXENAME}"
  WriteRegStr HKLM "${ARPKEY}" "UninstallString" "$INSTDIR\Uninstall.exe"
  WriteRegStr HKLM "${ARPKEY}" "QuietUninstallString" "$INSTDIR\Uninstall.exe /S"
  WriteRegStr HKLM "${ARPKEY}" "InstallLocation" "$INSTDIR"
  WriteRegDWORD HKLM "${ARPKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${ARPKEY}" "NoRepair" 1
  WriteRegDWORD HKLM "${ARPKEY}" "EstimatedSize" 91000

  WriteRegStr HKLM "Software\NetGold Monitor" "InstallLocation" "$INSTDIR"
SectionEnd

; ----------------------------------------------------------------
; Optional desktop shortcut (its own component checkbox)
; ----------------------------------------------------------------
Section "Desktop shortcut" SecDesktop
  CreateShortcut "$DESKTOP\NetGold Monitor.lnk" "$INSTDIR\${EXENAME}" "" "$INSTDIR\${EXENAME}" 0
SectionEnd

LangString DESC_SecMain ${LANG_ENGLISH} "The NetGold Monitor program, its documentation, and the bundled Microsoft SQL engine installer used for the legacy system backup imports."
LangString DESC_SecDesktop ${LANG_ENGLISH} "Add a NetGold Monitor shortcut to the desktop."
!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecMain} $(DESC_SecMain)
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} $(DESC_SecDesktop)
!insertmacro MUI_FUNCTION_DESCRIPTION_END

; ----------------------------------------------------------------
; Uninstaller
; ----------------------------------------------------------------
Section "Uninstall"
  ; Stop a running copy (tray program and/or service) before
  ; removing files, identified exactly as in the installer: exact
  ; image name, exact install path, and never this uninstaller's
  ; own process. Your monitoring data in your user profile
  ; (%LOCALAPPDATA%\NetGold) is deliberately kept.
  System::Call 'kernel32::GetCurrentProcessId() i .r0'
  StrCpy $NetGoldSelfPid $0
  nsExec::Exec 'net stop NetGoldMonitor'
  nsExec::ExecToStack 'powershell -NoProfile -Command "Stop-Process -Id (Get-CimInstance Win32_Process | Where-Object Name -eq $\"${EXENAME}$\" | Where-Object ProcessId -ne $NetGoldSelfPid | Where-Object ExecutablePath -eq $\"$INSTDIR\${EXENAME}$\" | Select-Object -ExpandProperty ProcessId) -Force -ErrorAction SilentlyContinue; Start-Sleep -Seconds 3; if (Get-CimInstance Win32_Process | Where-Object Name -eq $\"${EXENAME}$\" | Where-Object ProcessId -ne $NetGoldSelfPid | Where-Object ExecutablePath -eq $\"$INSTDIR\${EXENAME}$\") { exit 2 } else { exit 0 }"'
  Pop $0 ; result ignored: the uninstall continues either way

  Delete "$INSTDIR\${EXENAME}"
  Delete "$INSTDIR\engine\SqlLocalDB.msi"
  RMDir "$INSTDIR\engine"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\NetGold Monitor\NetGold Monitor.lnk"
  Delete "$SMPROGRAMS\NetGold Monitor\Uninstall NetGold Monitor.lnk"
  RMDir "$SMPROGRAMS\NetGold Monitor"
  Delete "$DESKTOP\NetGold Monitor.lnk"

  DeleteRegKey HKLM "${ARPKEY}"
  DeleteRegKey HKLM "Software\NetGold Monitor"
SectionEnd
