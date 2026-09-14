#!/bin/sh
# postremove - raeumt nach dem Entfernen auf.
#
# Bei "purge" fallen Konfiguration und der gemerkte Stand weg. An den VMs
# aendert das nichts: Was der Dienst dort ergaenzt hat, steht in deren
# Konfiguration und bleibt, wo es ist.
set -e

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi

if [ "$1" = "purge" ]; then
	rm -rf /etc/pve-optimizer /var/lib/pve-optimizer
fi

exit 0
