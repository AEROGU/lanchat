@echo off
setlocal
rem Quita las reglas del Firewall de Windows que creo "Abrir puertos LanChat
rem (solo si tienes problemas).bat" (las del grupo "LanChat (puertos)"). No
rem toca la regla que crea LanChat.exe desde Ajustes ni las que agrego Windows
rem al permitirlo.
rem Pide ser administrador.
set "LANCHAT_GRUPO=LanChat (puertos)"

net session >nul 2>&1
if errorlevel 1 (
  echo Pidiendo permiso de administrador...
  set "LANCHAT_BAT=%~f0"
  powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -Verb RunAs -FilePath $env:LANCHAT_BAT"
  exit /b
)

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$r = @(Get-NetFirewallRule -Group $env:LANCHAT_GRUPO -ErrorAction SilentlyContinue);" ^
  "if ($r.Count -eq 0) { Write-Host 'No habia reglas de LanChat (puertos).'; exit 0 }" ^
  "try { $r | Remove-NetFirewallRule -ErrorAction Stop; Write-Host ('Listo: se quitaron ' + $r.Count + ' reglas.') }" ^
  "catch { Write-Host ('No se pudieron quitar las reglas: ' + $_); exit 1 }"
echo.
pause
