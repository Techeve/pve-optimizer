---
title: Installation
description: pve-optimizer aus dem Techeve-Repository oder von Hand installieren.
sidebar:
  order: 2
---

## Aus dem Techeve-Repository

Der empfohlene Weg — auf jedem Node, der den Dienst bekommen soll. Proxmox ist
Debian, das Paket kommt also aus `apt` wie alles andere auch:

```bash
apt update && apt install pve-optimizer
```

Ist die Paketquelle auf dem Node noch nicht eingebunden, einmalig — der
Repository-Server bringt dafür ein Einrichtungsskript mit, das den
Signaturschlüssel hinterlegt und die Quelle einträgt:

```bash
curl -fsSL https://repo.techeve.de/setup.sh | sh
```

**Der Dienst startet nach der Installation bewusst nicht von selbst.** Er
schreibt in die Konfiguration laufender VMs, und womit, hängt an der Hardware
des Clusters — was ohne Zutun losliefe, wäre geraten. Das Paket bringt deshalb
nur die Vorlage mit:

```bash
cp /etc/pve-optimizer/config.example.yaml /etc/pve-optimizer/config.yaml
$EDITOR /etc/pve-optimizer/config.yaml       # dry_run: true stehen lassen
systemctl enable --now pve-optimizer
journalctl -u pve-optimizer -f               # was er tun würde
```

Sieht das Protokoll gut aus, `dry_run: false` setzen und neu starten. Schneller
zu einer passenden Datei führt der [Wizard](../konfiguration/#der-wizard).

Ein `apt upgrade` tauscht später das Binary und startet den Dienst neu, sofern
er eingerichtet ist. Die eigene `config.yaml` bleibt unangetastet; die Vorlage
daneben wird auf den neuen Stand gebracht.

## Umstieg von einer Handinstallation

Wer den Dienst bisher von Hand eingerichtet hat, hat ihn unter
`/usr/local/bin/pve-optimizer` liegen und seine Unit unter
`/etc/systemd/system/`. **Ein `apt install` allein reicht dort nicht** — im
Gegenteil, es sieht danach nur so aus, als wäre der Dienst aktuell:

Das Paket legt das Binary nach `/usr/bin` und die Unit nach
`/lib/systemd/system`. Systemd bevorzugt aber, was unter `/etc` steht. Die alte
Unit bleibt also maßgeblich und zeigt weiter auf `/usr/local/bin` — das
Paket-Binary wird nie gestartet. Das Installationsskript startet den Dienst neu,
weil er eingeschaltet ist, und bringt damit **die alte Version wieder hoch**.
Ein `pve-optimizer -version` am eingeschalteten Dienst zeigt weiterhin den
alten Stand.

Der Umstieg braucht deshalb einmalig je Node:

```bash
systemctl stop pve-optimizer
rm /etc/systemd/system/pve-optimizer.service
rm /usr/local/bin/pve-optimizer
apt install pve-optimizer
systemctl daemon-reload
systemctl enable --now pve-optimizer
systemctl status pve-optimizer      # Version im Protokoll gegenprüfen
```

Die eigene `/etc/pve-optimizer/config.yaml` bleibt dabei liegen und gilt weiter.
Zwei Dinge sind daran zu prüfen:

- Steht dort noch ein Pool in der alten Schreibweise `<node>:<pool>`, bricht
  der Dienst beim Start mit einem Hinweis ab — der Eintrag gehört unter
  [`nodes:`](../konfiguration/#abweichungen-je-node).
- Ohne einen Abschnitt `rules:` bleibt es beim bisherigen Verhalten: Die
  IO-Begrenzung ist scharf, alle neuen Regeln sind aus.

## Von Hand

Ohne Repository — die Binaries hängen an jedem
[Release](https://gitlab.techeve.de/techeve/pve-optimizer/-/releases?mtm_campaign=linking&mtm_kwd=doc):

```bash
install -m 0755 pve-optimizer-linux-amd64 /usr/bin/pve-optimizer
install -d -m 0750 /etc/pve-optimizer
install -m 0644 config.example.yaml /etc/pve-optimizer/config.yaml
install -m 0644 deploy/pve-optimizer.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now pve-optimizer
```

Erst mit `dry_run: true` laufen lassen und ins Journal schauen — dort steht
dann, was der Dienst setzen *würde*:

```bash
journalctl -u pve-optimizer -f
```

## Bestehende Gäste nachziehen

Die laufende Beobachtung greift nur bei neuen Aufgaben — an bereits vorhandenen
Gästen bliebe also alles, wie es ist. Für den Rollout gibt es deshalb einen
einmaligen Durchlauf über alle VMs und Container des Clusters:

```bash
pve-optimizer -config /etc/pve-optimizer/config.yaml -sweep
```

Erst mit `dry_run: true` laufen lassen und das Protokoll prüfen, dann scharf.
Der Durchlauf beendet sich nach getaner Arbeit; ein Gast, der sich nicht
anpassen lässt, bricht ihn nicht ab, sondern wird am Ende gemeldet.

## Entwicklung

```bash
make            # Übersicht aller Ziele
make check      # fmt, vet, lint, test, govulncheck — das, was auch die CI prüft
make build      # Binary für den eigenen Rechner
make deb        # Debian-Pakete für amd64 und arm64
```

Vor jedem Push `make check` — die Pipeline prüft dasselbe.
