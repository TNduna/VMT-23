@echo off
rem Switch the console to UTF-8 so text is displayed correctly.
chcp 65001 >nul 2>&1
rem VMT-23 supervisor startup script (Windows)
rem
rem Usage:
rem   start.bat                  Run in foreground (Ctrl-C to stop)
rem   start.bat -addr :9000      Pass additional arguments through to vmt-23 unchanged
rem
rem Its only job is to start vmt-23.exe and decide whether to restart it based on its exit code.
rem
rem   0      Normal user stop      -> exit loop
rem   75     Program requests restart -> restart immediately (one-click update or rollback in the UI)
rem   Other  Crash                  -> restart after backoff (1->2->4…up to 60 seconds)
rem
rem Downloads, SHA256 checks, and replacements are handled by vmt-23 itself at startup
rem (selfupdate package). Keep this script simple; see the explanation at the top of start.sh.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=vmt-23.exe"
if not exist "%BIN%" if exist "vmt-23.exe" set "BIN=vmt-23.exe"
if not exist "%BIN%" (
	echo [vmt-23] Executable not found: %BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [vmt-23] Exited normally
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem Update/rollback is ready: vmt-23 applies it during startup after restart.
	echo [vmt-23] Restart requested (apply the new version)…
	set /a delay=1
	goto loop
)

echo [vmt-23] Unexpected exit ^(code=!code!^); restarting in !delay!s 1>&2
rem timeout fails in a redirected console, so use ping as a fallback (N seconds requires N+1 pings).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop