@echo off
setlocal
rem SOLO SI TIENES PROBLEMAS. La mayoria de las PC NO necesitan este script:
rem basta con permitir LanChat cuando Windows lo pregunta la primera vez (o en
rem Ajustes > Firewall de Windows). Usalo solo en la PC a la que NO le llegan
rem los mensajes aunque se vea en la lista: suele pasar si su red esta como
rem "Publica" o si tiene varias redes (Ethernet y Wi-Fi).
rem
rem Que hace: abre en el Firewall de Windows SOLO los dos puertos de LanChat,
rem de entrada y en todas las redes (privada, publica y dominio):
rem UDP 50000 (descubrimiento) y TCP 50001 (mensajes, archivos y fotos de
rem perfil). Ningun otro programa los usa; las conexiones de LanChat van
rem cifradas. Las reglas no dependen de la carpeta de LanChat.exe y quedan en
rem el grupo "LanChat (puertos)": antes de crearlas se borran las de ese
rem grupo, asi que no se duplican.
rem Se revierte por completo con "Quitar puertos LanChat.bat". Pide ser
rem administrador. Si cambiaste los puertos en config.json, cambialos tambien
rem aqui.
set "LANCHAT_GRUPO=LanChat (puertos)"
set "LANCHAT_UDP=50000"
set "LANCHAT_TCP=50001"

rem Al volver a abrirse como administrador ("elevado") ya se mostro el aviso.
if /i "%~1"=="elevado" goto :admin
echo SOLO SI TIENES PROBLEMAS con LanChat en esta PC.
echo.
echo La mayoria de las PC no necesitan esto. Usalo solo si a esta PC no le
echo llegan los mensajes aunque aparezca en la lista de los demas.
echo.
echo Abre en el Firewall de Windows solo los puertos de LanChat:
echo   UDP %LANCHAT_UDP% (descubrimiento) y TCP %LANCHAT_TCP% (mensajes y archivos).
echo Se revierte con "Quitar puertos LanChat.bat".
echo.
echo Si no quieres hacerlo, cierra esta ventana. Para continuar,
pause

:admin
net session >nul 2>&1
if errorlevel 1 (
  echo Pidiendo permiso de administrador...
  set "LANCHAT_BAT=%~f0"
  powershell -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -Verb RunAs -FilePath $env:LANCHAT_BAT -ArgumentList 'elevado'"
  exit /b
)

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$ErrorActionPreference = 'Stop'; $g = $env:LANCHAT_GRUPO;" ^
  "try {" ^
  "  Get-NetFirewallRule -Group $g -ErrorAction SilentlyContinue | Remove-NetFirewallRule;" ^
  "  New-NetFirewallRule -Group $g -DisplayName ('LanChat - descubrimiento (UDP ' + $env:LANCHAT_UDP + ')') -Direction Inbound -Action Allow -Protocol UDP -LocalPort $env:LANCHAT_UDP -Profile Any | Out-Null;" ^
  "  New-NetFirewallRule -Group $g -DisplayName ('LanChat - mensajes y archivos (TCP ' + $env:LANCHAT_TCP + ')') -Direction Inbound -Action Allow -Protocol TCP -LocalPort $env:LANCHAT_TCP -Profile Any | Out-Null;" ^
  "  Write-Host ('Listo: abiertos UDP ' + $env:LANCHAT_UDP + ' y TCP ' + $env:LANCHAT_TCP + ' en todas las redes.');" ^
  "  $b = Get-NetFirewallApplicationFilter | Where-Object { $_.Program -like '*LanChat*' } | Get-NetFirewallRule | Where-Object { $_.Action -eq 'Block' -and $_.Direction -eq 'Inbound' -and $_.Enabled -eq 'True' };" ^
  "  if ($b) { Write-Host ''; Write-Host 'ATENCION: estas reglas BLOQUEAN a LanChat y ganan a las de arriba.'; Write-Host 'Borralas en Firewall de Windows > Configuracion avanzada > Reglas de entrada:'; $b | ForEach-Object { Write-Host ('  - ' + $_.DisplayName) } }" ^
  "} catch { Write-Host ('No se pudieron crear las reglas: ' + $_); exit 1 }"
echo.
pause
