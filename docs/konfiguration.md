---
title: Konfiguration
description: Wizard, Drosselprofile je Speicherpool, Abweichungen je Node und API-Token.
sidebar:
  order: 3
---

## Der Wizard

Der kürzeste Weg zu einer laufenden Einrichtung:

```bash
pve-optimizer -wizard
```

Er sieht sich erst den Cluster an und schlägt vor, was dort tatsächlich steht —
die vorhandenen Speicher, die Gäste, die kein Sicherungsauftrag erfasst. Jede
Frage hat eine Vorgabe, die **Enter** übernimmt:

```
Gefunden auf pve01:
  Speicher mit Gastplatten: local-pool, local-zfs
  Gäste im Cluster:         29
  davon ohne Sicherung:     4

── Drosselung der Platten ──
Dauerrate lesen (MB/s) [200]:
```

Am Ende zeigt er die erzeugte Datei, **lädt sie einmal probeweise** und
schreibt erst dann — eine Konfiguration, die der Dienst nicht versteht, wäre
ein schlechteres Ergebnis als gar keine. Eine vorhandene Datei wird vorher nach
`config.yaml.vor-wizard` gesichert.

Zum Schluss läuft `-check` und zeigt, was auf dem Cluster auffällt. Die Gäste
ohne Sicherung listet er dabei schon während der Einrichtung auf und fragt,
welche davon bewusst keine brauchen — die landen dann in der Ignore-Liste und
tauchen im [Bericht](../empfehlungen/#regelmäßiger-bericht) nicht mehr auf.

Der Wizard spricht über `pvesh` mit Proxmox und gehört damit **auf einen
Node**. Für die zentrale Installation über die Cluster-API ist die Vorlage der
Weg.

## Grundaufbau

Vorlage zum Nachbessern oder für den Anfang von Hand: [`config.example.yaml`](https://gitlab.techeve.de/techeve/pve-optimizer/-/blob/main/config.example.yaml?mtm_campaign=linking&mtm_kwd=doc).

```yaml
mode: local
poll_interval: 30s
dry_run: false

rules:             # was der Dienst prüft und wie weit er dabei geht
  io_limits: enforce
  discard: report

defaults:          # gilt für jeden Pool ohne eigenes Profil
  mbps_rd: 200
  mbps_wr: 150
  iops_rd: 5000
  iops_wr: 3000

pools:
  local-pool:
    mbps_rd: 500            # Dauerrate
    mbps_rd_max: 800        # Spitze ...
    bps_rd_max_length: 10   # ... für höchstens 10 Sekunden
    iops_rd: 20000
```

`defaults` ist Pflicht — ohne Standardprofil bliebe eine Platte auf einem
unbekannten Pool ungedrosselt, und genau das soll nicht passieren. Ein
Tippfehler in einem Schlüssel lässt den Dienst beim Start abbrechen, statt die
Begrenzung stillschweigend zu verschlucken.

Was unter `rules:` steht, beschreibt die Seite [Regeln](../regeln/).

## Die Schlüssel der Drosselprofile

| Zweck | Durchsatz (MB/s) | IOPS |
|---|---|---|
| Dauerrate | `mbps_rd`, `mbps_wr` | `iops_rd`, `iops_wr` |
| Spitze | `mbps_rd_max`, `mbps_wr_max` | `iops_rd_max`, `iops_wr_max` |
| Dauer der Spitze (s) | `bps_rd_max_length`, `bps_wr_max_length` | `iops_rd_max_length`, `iops_wr_max_length` |
| beide Richtungen gemeinsam | `mbps`, `mbps_max`, `bps_max_length` | `iops`, `iops_max`, `iops_max_length` |

Zwei Fallstricke, die Proxmox hier mitbringt:

**Die Dauer der Spitze heißt beim Durchsatz `bps_..._max_length`, nicht
`mbps_...`.** Und sie gehört zwingend dazu: Ein `mbps_rd_max` ohne Längenangabe
lässt QEMU nur eine Sekunde bursten, die Spitze verpufft also.

**Gemeinsames und getrenntes Limit schließen sich aus.** `mbps` neben `mbps_rd`
weist QEMU zurück. Der Dienst prüft das beim Start für jedes Profil und lässt an
einer Platte, die bereits die andere Variante nutzt, die jeweilige Familie
unangetastet — sonst würde eine einzige unpassende Platte die ganze Änderung
scheitern lassen.

Ein weggelassener oder auf `0` gesetzter Schlüssel wird nicht geschrieben.

### Die Burst-Dauer ist in der Weboberfläche unsichtbar

Proxmox bietet unter *Disk → Bandwidth* nur die Dauer- und die Spitzenrate an —
Felder für `_max_length` gibt es dort nicht. Die Werte, die dieser Dienst setzt,
sind in der Oberfläche also **nicht zu sehen**.

Sie gehen dabei aber auch nicht verloren: Die Oberfläche liest beim Bearbeiten
alle Parameter einer Platte ein und schreibt sie unverändert zurück, auch die,
für die sie kein Eingabefeld hat. An den Bandbreiten einer Platte lässt sich
also gefahrlos über die Oberfläche schrauben.

Nachsehen lassen sie sich auf dem Node:

```bash
qm config 100 | grep scsi0
```

## Sinnvolle Werte finden

Die Werte hängen an der Hardware. Ein durchgerechnetes Beispiel für eine NVMe
der 4. Generation mit rund zehn VMs steht in [`config.example.yaml`](https://gitlab.techeve.de/techeve/pve-optimizer/-/blob/main/config.example.yaml?mtm_campaign=linking&mtm_kwd=doc) unter `nvme-gen4`.

Die Logik dahinter in Kurzform:

- **Dauerrate** so wählen, dass alle VMs zusammen die Platte nicht überfahren —
  bei zehn VMs also etwa ein Zehntel dessen, was die Platte dauerhaft leistet,
  plus etwas Luft.
- **Schreiben strenger begrenzen als Lesen.** Die Datenblattwerte gelten,
  solange der SLC-Cache reicht; danach bricht die Rate deutlich ein.
- **Burst großzügig, aber kurz.** Booten, ein Paket installieren, eine Datei
  kopieren — all das dauert Sekunden und soll mit voller Geschwindigkeit
  laufen. Erst dauerhafte Last fällt auf die Dauerrate zurück. Genau das trifft
  Restores, also die Fälle, in denen eine VM den Node lahmlegt.
- **IOPS aus der Praxis, nicht aus dem Datenblatt.** Die genannten
  Hunderttausende gelten bei einem Strom mit hoher Warteschlange, nicht bei zehn
  VMs mit gemischter Last.

## Abweichungen je Node

Alles Bisherige gilt clusterweit. Unter `nodes:` steht, was auf einzelnen Nodes
anders ist — genannt wird nur das Abweichende:

```yaml
rules:
  discard: enforce
  iothread: enforce

nodes:
  vmh03:
    rules:
      discard: off        # hier hängt ein Speicher ohne Discard
      iothread: report    # neu im Cluster, erst einmal beobachten
    services:
      restart_limit: 5    # dieser Node hat eine Vorgeschichte
    advice:
      backup_coverage: off  # hier stehen nur Wegwerf-Gäste
    report:
      every: 24h            # dieser Node wird enger beobachtet
    pools:
      local-pool:         # gleicher Name, langsamere Platte
        mbps_wr: 80
    defaults:
      mbps_rd: 150
```

Bei einer Regel wird nur überschrieben, was der Node tatsächlich nennt: Wer dort
allein den Modus setzt, behält deren übrige Optionen aus dem allgemeinen
Abschnitt. Für den Dienst-Monitor gilt dasselbe — im Beispiel oben erbt `vmh03`
Modus, Units und Frist aus `services:` und ändert nur die Zahl der Neustarts.

Ein Profil wird von speziell nach allgemein gesucht:

```
nodes.<node>.pools.<pool>   dieser Pool auf diesem Node
pools.<pool>                dieser Pool auf allen Nodes
nodes.<node>.defaults       alles Übrige auf diesem Node
defaults                    alles Übrige
```

Welches Profil gegriffen hat, steht im Protokoll.

## API-Token

Für `mode: api` einen Token mit Rechten auf `/vms` anlegen (`VM.Audit` und
`VM.Config.Disk` genügen). Das Secret gehört **nicht** in die
Konfigurationsdatei, sondern in die Umgebungsvariable
`PVE_OPTIMIZER_TOKEN_SECRET` — die mitgelieferte systemd-Unit liest sie aus
`/etc/pve-optimizer/secret.env`.
