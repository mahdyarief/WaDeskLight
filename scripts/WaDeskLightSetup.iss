; WaDeskLight installer script
; Build: ISCC.exe /O"dist" /F"WaDeskLightSetup" scripts\WaDeskLightSetup.iss

[Setup]
AppId={{8F6A9E2C-4B1E-4F3A-9C6D-WaDeskLight}
AppName=WaDeskLight
AppVersion=1.2.0
AppPublisher=WaDeskLight
AppContact=https://github.com/rayss868/WaDeskLight
DefaultDirName={localappdata}\Programs\WaDeskLight
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
OutputDir=dist
OutputBaseFilename=WaDeskLightSetup
Compression=lzma
SolidCompression=yes
CloseApplications=yes
Uninstallable=yes
UninstallDisplayIcon={app}\icon.ico
SetupIconFile=..\assets\icon.ico

[Files]
Source: "..\dist\WhatsApp.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\assets\icon.ico"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\WhatsApp"; Filename: "{app}\WhatsApp.exe"; IconFilename: "{app}\icon.ico"; WorkingDir: "{app}"
Name: "{autodesktop}\WhatsApp"; Filename: "{app}\WhatsApp.exe"; IconFilename: "{app}\icon.ico"; WorkingDir: "{app}"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"

[Run]
Filename: "{app}\WhatsApp.exe"; Description: "Launch WhatsApp"; Flags: nowait postinstall skipifsilent