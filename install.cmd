@echo off
rem Starts the QS-PodScript installer (install.ps1). Options are passed on,
rem e.g.  install.cmd -Gpu vulkan
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1" %*
echo.
pause
