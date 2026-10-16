@echo off
setlocal

:: ============================================================
::  Reconcile GUI launcher  /  "Align local" GUI
::
::  This file is intentionally ASCII-only: cmd.exe parses .cmd
::  files byte-by-byte using the console code page, so non-ASCII
::  text here gets split into fragments that cmd then tries to
::  execute as commands. All Chinese text is printed by the Go
::  program instead, which handles UTF-8 correctly.
::
::  It does two things:
::    1. point the config dir at .pcsconfig (the saved login)
::    2. start "reconcile gui", which opens the browser
::
::  Close this window to stop the interface.
::  Nothing on the cloud is changed by starting or closing it.
:: ============================================================

set "ROOT=%~dp0"
set "EXE=%ROOT%.tools\BaiduPCS-GoPlus.exe"
if exist "%ROOT%BaiduPCS-GoPlus.exe" set "EXE=%ROOT%BaiduPCS-GoPlus.exe"
set "CFG=%ROOT%.pcsconfig"

if not exist "%EXE%" goto missing

set "BAIDUPCS_GO_CONFIG_DIR=%CFG%"

echo.
echo ============================================================
echo   Reconcile GUI  -  BaiduPCS-GoPlus
echo ============================================================
echo   exe    : %EXE%
echo   config : %CFG%
echo.
echo   The browser should open automatically. If it does not,
echo   open the http address printed below.
echo.
echo   Close this window, or use the page's own close button,
echo   to stop the interface.
echo ============================================================
echo.

"%EXE%" reconcile gui
set "RC=%ERRORLEVEL%"

echo.
if not "%RC%"=="0" echo The interface exited with code %RC%.
pause
exit /b %RC%

:missing
echo.
echo [ERROR] Program not found:
echo         %EXE%
echo.
echo Build it first:
echo         powershell -NoProfile -ExecutionPolicy Bypass -File tools\build.ps1
echo.
pause
exit /b 1
