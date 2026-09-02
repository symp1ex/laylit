@echo off
setlocal

rem SERVICE_NAME must match serviceName in internal\entry\main.go.
set "SERVICE_NAME=Laylit"
set "DISPLAY_NAME=Laylit"
set "DESCRIPTION=Shows the active keyboard layout through keyboard backlight color."
set "EXE_PATH=%~dp0Laylit.exe"
set "START_TYPE=auto"

set "ACTION=%~1"
if not defined ACTION set "ACTION=install"

if /I "%ACTION%"=="install" goto install
if /I "%ACTION%"=="uninstall" goto uninstall
if /I "%ACTION%"=="start" goto start
if /I "%ACTION%"=="stop" goto stop

echo Usage: %~nx0 [install^|uninstall^|start^|stop]
exit /b 2

:install
if not exist "%EXE_PATH%" (
    echo ERROR: executable not found: "%EXE_PATH%"
    exit /b 1
)
sc.exe query "%SERVICE_NAME%" >nul 2>&1
if not errorlevel 1 (
    echo ERROR: service "%SERVICE_NAME%" already exists.
    exit /b 1
)
sc.exe create "%SERVICE_NAME%" binPath= "\"%EXE_PATH%\" -service" start= "%START_TYPE%" DisplayName= "%DISPLAY_NAME%"
if errorlevel 1 (
    echo ERROR: failed to create service "%SERVICE_NAME%".
    exit /b 1
)
sc.exe description "%SERVICE_NAME%" "%DESCRIPTION%"
if errorlevel 1 (
    echo ERROR: service was created, but its description could not be set.
    exit /b 1
)
echo Service "%SERVICE_NAME%" installed successfully.
exit /b 0

:uninstall
sc.exe query "%SERVICE_NAME%" >nul 2>&1
if errorlevel 1 (
    echo ERROR: service "%SERVICE_NAME%" does not exist.
    exit /b 1
)
sc.exe delete "%SERVICE_NAME%"
if errorlevel 1 (
    echo ERROR: failed to delete service "%SERVICE_NAME%".
    exit /b 1
)
echo Service "%SERVICE_NAME%" deleted successfully.
exit /b 0

:start
sc.exe start "%SERVICE_NAME%"
if errorlevel 1 (
    echo ERROR: failed to start service "%SERVICE_NAME%".
    exit /b 1
)
exit /b 0

:stop
sc.exe stop "%SERVICE_NAME%"
if errorlevel 1 (
    echo ERROR: failed to stop service "%SERVICE_NAME%".
    exit /b 1
)
exit /b 0
