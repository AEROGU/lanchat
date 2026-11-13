@echo off
rem Abre el desinstalador de LanChat (pregunta antes de borrar nada) con el
rem ejecutable que funciona en esta PC: LanChat_x86.exe en Windows de 32 bits.
set "exe=LanChat.exe"
if /i "%PROCESSOR_ARCHITECTURE%"=="x86" if not defined PROCESSOR_ARCHITEW6432 set "exe=LanChat_x86.exe"
if not exist "%~dp0%exe%" set "exe=LanChat_x86.exe"
start "" "%~dp0%exe%" -uninstall
