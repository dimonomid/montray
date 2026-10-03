[Desktop Entry]
Type=Application
Name=Montray (Legacy)
Comment=Show Montray status in the desktop tray
Icon=montray-ui-legacy
Exec={{ desktopExecArgument .Executable }} --config {{ desktopExecArgument .ConfigFilename }}
Terminal=false
