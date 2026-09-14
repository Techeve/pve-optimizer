#!/bin/sh
# preremove - haelt den Dienst an, bevor Dateien entfernt werden.
set -e

if [ -d /run/systemd/system ]; then
	systemctl stop pve-optimizer.service >/dev/null 2>&1 || true
	# Beim endgueltigen Entfernen (nicht bei Upgrade) auch deaktivieren.
	if [ "$1" = "remove" ] || [ "$1" = "0" ]; then
		systemctl disable pve-optimizer.service >/dev/null 2>&1 || true
	fi
fi

exit 0
