@echo off
chcp 65001 >nul
setlocal
pwsh -NoProfile -File "%~dp0scripts\build-windows.ps1" -Version "%~1"
exit /b %errorlevel%