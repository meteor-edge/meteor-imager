; Inno Setup script: iscc /DVersion=1.2.3 /DSource=dist\windows_amd64 packaging\windows\meteor-imager.iss
#ifndef Version
  #define Version "0.0.0"
#endif
#ifndef Source
  #define Source "..\..\dist\windows_amd64"
#endif

[Setup]
AppId={{6B7E1C2A-4D0F-4C3B-9A51-2E8D7F0B1C64}
AppName=Meteor Imager
AppVersion={#Version}
AppPublisher=Meteor Edge
AppPublisherURL=https://github.com/meteor-edge/meteor-imager
DefaultDirName={autopf}\Meteor Imager
DefaultGroupName=Meteor Imager
DisableProgramGroupPage=yes
OutputDir=..\..\dist
OutputBaseFilename=MeteorImager-{#Version}-setup
SetupIconFile=..\icon\icon.ico
UninstallDisplayIcon={app}\meteor-imager.ico
LicenseFile=..\..\LICENSE
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
Compression=lzma2
SolidCompression=yes
WizardStyle=modern

[Files]
Source: "{#Source}\meteor-imager.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\icon\icon.ico"; DestDir: "{app}"; DestName: "meteor-imager.ico"
Source: "..\..\LICENSE"; DestDir: "{app}"
Source: "{#Source}\THIRD_PARTY_LICENSES.txt"; DestDir: "{app}"

[Icons]
Name: "{group}\Meteor Imager"; Filename: "{app}\meteor-imager.exe"; IconFilename: "{app}\meteor-imager.ico"
Name: "{autodesktop}\Meteor Imager"; Filename: "{app}\meteor-imager.exe"; IconFilename: "{app}\meteor-imager.ico"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; Flags: unchecked

[Run]
Filename: "{app}\meteor-imager.exe"; Description: "Start Meteor Imager"; Flags: nowait postinstall skipifsilent shellexec
