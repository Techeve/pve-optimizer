#!/bin/sh
# postinstall - registriert den Dienst und sagt, was noch zu tun ist.
#
# Der Dienst wird bei einer Erstinstallation bewusst NICHT gestartet: Er
# schreibt in die Konfiguration laufender VMs, und die Werte dafuer haengen
# an der Hardware des Clusters. Was ohne Zutun losliefe, waere geraten.
# Beim Update dagegen wird ein bereits eingerichteter Dienst neu gestartet.
set -e

# Sprache der Ausgaben: Standard Englisch, Deutsch nur bei deutscher
# Systemsprache. Reihenfolge nach POSIX (LC_ALL sticht LC_MESSAGES sticht
# LANG). dpkg ruft Maintainer-Skripte oft ohne Locale auf - dann Englisch.
#
# KEINE UMLAUTE in den Texten: Die Ausgabe landet auf Konsolen, die
# keineswegs immer UTF-8 sprechen (serielle Konsolen, Rettungssysteme,
# ISO-8859-1-Terminals). Deshalb durchgaengig ue, ae, oe und ss.
is_german() {
	for v in "${LC_ALL:-}" "${LC_MESSAGES:-}" "${LANG:-}"; do
		[ -n "$v" ] || continue
		case "$v" in
			de* | De* | DE*) return 0 ;;
			*) return 1 ;;
		esac
	done
	return 1
}

CONF_DIR=/etc/pve-optimizer
CONF_FILE="$CONF_DIR/config.yaml"

# Das Verzeichnis traegt neben der Konfiguration auch secret.env mit dem
# API-Token. Der Dienst laeuft als root, sonst kaeme er nicht an /etc/pve;
# ausser root muss hier niemand hineinschauen.
mkdir -p "$CONF_DIR"
chown root:root "$CONF_DIR"
chmod 0750 "$CONF_DIR"

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
	# Nur weiterlaufen lassen, was bereits lief. Ein Update soll den Dienst
	# auf den neuen Stand bringen, ihn aber nicht erstmals scharf schalten.
	if systemctl is-enabled pve-optimizer.service >/dev/null 2>&1; then
		systemctl restart pve-optimizer.service >/dev/null 2>&1 || true
	fi
fi

[ -e "$CONF_FILE" ] && exit 0

if is_german; then
	cat <<'EOF'

============================================================
  pve-optimizer ist installiert - und noch nicht gestartet.

  Der Dienst schreibt in die Konfiguration laufender VMs.
  Womit, steht in einer Konfigurationsdatei, die es noch
  nicht gibt:

    1. Vorlage kopieren
       cp /etc/pve-optimizer/config.example.yaml \
          /etc/pve-optimizer/config.yaml

    2. Anpassen - vor allem die Begrenzungen unter
       "defaults" und "pools" auf die eigenen Platten, und
       "dry_run: true" zum Ausprobieren stehen lassen.

    3. Starten und zuschauen, was er taete:
       systemctl enable --now pve-optimizer
       journalctl -u pve-optimizer -f

    4. Sieht das Protokoll gut aus, "dry_run: false" setzen
       und neu starten. Fuer die bereits vorhandenen VMs
       einmalig:
       pve-optimizer -config /etc/pve-optimizer/config.yaml -sweep

  Ohne Zutun sind nur die IO-Begrenzungen scharf; die
  uebrigen Regeln stehen in der Vorlage auf "report" und
  schreiben nichts, bis jemand sie einschaltet.

  Doku: /usr/share/doc/pve-optimizer/README.md
============================================================

EOF
else
	cat <<'EOF'

============================================================
  pve-optimizer is installed - and not started yet.

  The service writes to the configuration of running VMs.
  What it writes comes from a configuration file that does
  not exist yet:

    1. Copy the template
       cp /etc/pve-optimizer/config.example.yaml \
          /etc/pve-optimizer/config.yaml

    2. Adjust it - above all the limits under "defaults"
       and "pools" to match your own disks, and leave
       "dry_run: true" in place for a trial run.

    3. Start it and watch what it would do:
       systemctl enable --now pve-optimizer
       journalctl -u pve-optimizer -f

    4. If the log looks right, set "dry_run: false" and
       restart. For VMs that already exist, once:
       pve-optimizer -config /etc/pve-optimizer/config.yaml -sweep

  Out of the box only the IO limits are enforced; the other
  rules ship as "report" in the template and write nothing
  until someone turns them on.

  Docs: /usr/share/doc/pve-optimizer/README.md
============================================================

EOF
fi

exit 0
