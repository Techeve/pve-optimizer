#!/bin/bash
# Rollt die gebauten pve-optimizer-.deb-Pakete (amd64 + arm64 aus bin/) auf den
# TechEve-Repository-Server (aptly) aus: hochladen → in das Repo übernehmen →
# die Suite neu publizieren und signieren. Danach ist pve-optimizer per
# `apt install pve-optimizer` aus dem eigenen Repo installier- und aktualisierbar.
#
# Benötigte Umgebungs-/CI-Variablen (geerbte techeve-Gruppenvariablen):
#   REPO_URL   Basis-URL der aptly-HTTP-API, z.B. https://repo.techeve.de
#   REPO_USER  Basic-Auth-Benutzer
#   REPO_PASS  Basic-Auth-Passwort (masked)
# Optional:
#   REPO_NAME  aptly-Repo-Name        (Default: techeve)
#   DISTRO     zu publizierende Suite (Default: stable)
#   GPG_KEY    Signaturschlüssel      (Default: repo@techeve.de)
#
# Die Variablen sind PROTECTED — der Job läuft deshalb nur auf einem
# geschützten Branch (main). Sonst sind sie leer und das Skript bricht
# mit einer klaren Meldung ab.
set -euo pipefail

: "${REPO_URL:?REPO_URL erforderlich (protected CI-Variable — Job muss auf einem geschützten Branch laufen)}"
: "${REPO_USER:?REPO_USER erforderlich}"
: "${REPO_PASS:?REPO_PASS erforderlich}"
REPO_NAME="${REPO_NAME:-techeve}"
DISTRO="${DISTRO:-stable}"
GPG_KEY="${GPG_KEY:-repo@techeve.de}"

shopt -s nullglob
DEBS=(bin/*.deb)
[ "${#DEBS[@]}" -gt 0 ] || { echo "Keine .deb-Dateien in bin/ gefunden"; exit 1; }

# Zugangsdaten NICHT über -u in der Kommandozeile übergeben: argv ist auf
# einem Shared-Runner für jeden parallelen Job in /proc lesbar. Stattdessen
# über eine temporäre netrc-Datei (nur für den eigenen User lesbar), die am
# Ende sicher wieder entfernt wird.
NETRC="$(mktemp)"
chmod 600 "$NETRC"
cleanup() { rm -f "$NETRC"; }
trap cleanup EXIT
# Host ohne Schema/Port für den netrc-machine-Eintrag.
REPO_HOST="$(printf '%s' "$REPO_URL" | sed -E 's#^https?://##; s#[:/].*$##')"
printf 'machine %s login %s password %s\n' "$REPO_HOST" "$REPO_USER" "$REPO_PASS" > "$NETRC"

CURL="curl -fsS --retry 3 --retry-delay 2 --netrc-file ${NETRC}"
# Eindeutiges Staging-Verzeichnis je Pipeline-Lauf.
UPDIR="pve-optimizer-${CI_PROJECT_ID:-x}-${CI_PIPELINE_IID:-0}"

echo ">> 1/3 Pakete nach Staging '${UPDIR}' hochladen"
for DEB in "${DEBS[@]}"; do
  echo "   - ${DEB}"
  ${CURL} -X POST -F "file=@${DEB}" "${REPO_URL}/api/files/${UPDIR}" >/dev/null
done

echo ">> 2/3 In Repository '${REPO_NAME}' übernehmen"
${CURL} -X POST "${REPO_URL}/api/repos/${REPO_NAME}/file/${UPDIR}" >/dev/null

echo ">> 3/3 Suite '${DISTRO}' neu publizieren und signieren"
${CURL} -X PUT -H "Content-Type: application/json" \
  --data "{\"Signing\":{\"Batch\":true,\"GpgKey\":\"${GPG_KEY}\"}}" \
  "${REPO_URL}/api/publish/:./${DISTRO}" >/dev/null

echo ">> Fertig — pve-optimizer-Pakete sind im Repository ${REPO_URL} verfügbar."
echo "   Installation auf dem Zielserver:  apt update && apt install pve-optimizer"
