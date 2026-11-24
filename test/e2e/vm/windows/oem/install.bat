@echo off
REM Runs once at the end of the unattended Windows installation.
REM Enables OpenSSH so the host can drive the real test suite over SSH.
echo [fleet-e2e] bootstrapping OpenSSH...
REM Test-VM only: lets the local administrator get an elevated token over the
REM network (SSH). Never enable this on a real machine.
reg add "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System" /v LocalAccountTokenFilterPolicy /t REG_DWORD /d 1 /f
powershell -NoProfile -ExecutionPolicy Bypass -File C:\OEM\bootstrap.ps1
echo [fleet-e2e] bootstrap finished with code %ERRORLEVEL%
