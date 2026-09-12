# pve-optimizer

Ein kleiner Dienst für Proxmox VE. Er beobachtet die Aufgabenliste des
Clusters, und sobald eine VM **neu angelegt, geklont oder aus einem Backup
wiederhergestellt** wurde, geht er deren Platten durch und ergänzt fehlende
IO-Begrenzungen.

## Warum

Frisch angelegte oder wiederhergestellte VMs laufen ohne Drosselung. Eine
einzige davon kann beim Hochfahren oder bei einem Restore den Speicher-Pool
so auslasten, dass alle anderen Gäste des Nodes darunter leiden. Die Limits
von Hand nachzutragen wird regelmäßig vergessen — genau das übernimmt dieser
Dienst.

## Was er macht

1. Fragt regelmäßig `/cluster/tasks` ab.
2. Filtert auf abgeschlossene Aufgaben vom Typ `qmcreate`, `qmrestore`
   und `qmclone`.
3. Liest die Konfiguration der betroffenen VM.
4. Bestimmt für jede Platte den Speicherpool — das ist der Name vor dem
   Doppelpunkt, bei `local-pool:vm-100-disk-0` also `local-pool`.
5. Ergänzt die Werte aus dem Profil des Pools, die noch nicht gesetzt sind.

**Bereits gesetzte Werte bleiben unangetastet.** Wer eine Platte bewusst
abweichend konfiguriert hat, behält seine Einstellung. Der Dienst ergänzt
nur, er korrigiert nicht.

Übersprungen werden CD-ROM-Laufwerke sowie `efidisk`, `tpmstate` und
`unused*` — dort akzeptiert Proxmox keine Drosselung.

## Betriebsarten

| Modus | Zugriff | Installation | Token |
|---|---|---|---|
| `local` | `pvesh` auf dem Node | auf **jedem** Node | nicht nötig |
| `api` | HTTPS gegen die Cluster-API | **einmal**, auch außerhalb | nötig |

`local` ist der einfachere Weg und braucht keine Zugangsdaten, muss aber je
Node ausgerollt und aktualisiert werden. `api` sieht den ganzen Cluster aus
einer Installation heraus und läuft auch dann weiter, wenn ein Node neu
startet.

## Konfiguration

Vorlage: [`config.example.yaml`](config.example.yaml).

```yaml
mode: local
poll_interval: 30s
dry_run: false

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

### Die Schlüssel

| Zweck | Durchsatz (MB/s) | IOPS |
|---|---|---|
| Dauerrate | `mbps_rd`, `mbps_wr` | `iops_rd`, `iops_wr` |
| Spitze | `mbps_rd_max`, `mbps_wr_max` | `iops_rd_max`, `iops_wr_max` |
| Dauer der Spitze (s) | `bps_rd_max_length`, `bps_wr_max_length` | `iops_rd_max_length`, `iops_wr_max_length` |
| beide Richtungen gemeinsam | `mbps`, `mbps_max`, `bps_max_length` | `iops`, `iops_max`, `iops_max_length` |

Zwei Fallstricke, die Proxmox hier mitbringt:

**Die Dauer der Spitze heißt beim Durchsatz `bps_..._max_length`, nicht
`mbps_...`.** Und sie gehört zwingend dazu: Ein `mbps_rd_max` ohne
Längenangabe lässt QEMU nur eine Sekunde bursten, die Spitze verpufft also.

**Gemeinsames und getrenntes Limit schließen sich aus.** `mbps` neben
`mbps_rd` weist QEMU zurück. Der Dienst prüft das beim Start für jedes
Profil und lässt an einer Platte, die bereits die andere Variante nutzt, die
jeweilige Familie unangetastet — sonst würde eine einzige unpassende Platte
die ganze Änderung scheitern lassen.

Ein weggelassener oder auf `0` gesetzter Schlüssel wird nicht geschrieben.

`defaults` ist Pflicht — ohne Standardprofil bliebe eine Platte auf einem
unbekannten Pool ungedrosselt, und genau das soll nicht passieren. Ein
Tippfehler in einem Schlüssel lässt den Dienst beim Start abbrechen, statt
die Begrenzung stillschweigend zu verschlucken.

### API-Token

Für `mode: api` einen Token mit Rechten auf `/vms` anlegen
(`VM.Audit` und `VM.Config.Disk` genügen). Das Secret gehört **nicht** in
die Konfigurationsdatei, sondern in die Umgebungsvariable
`PVE_OPTIMIZER_TOKEN_SECRET` — die mitgelieferte systemd-Unit liest sie aus
`/etc/pve-optimizer/secret.env`.

## Installation

```bash
install -m 0755 pve-optimizer-linux-amd64 /usr/local/bin/pve-optimizer
install -d /etc/pve-optimizer
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

## Entwicklung

```bash
make            # Übersicht aller Ziele
make check      # fmt, vet, lint, test, govulncheck — das, was auch die CI prüft
make build      # Binary für den eigenen Rechner
```

Vor jedem Push `make check` — die Pipeline prüft dasselbe.

## Grenzen

- **Nur VMs, keine Container.** Proxmox kennt für LXC keine Drosselung je
  Mountpoint; dort ginge das nur über cgroup-Limits für den ganzen
  Container.
- **Nur neue Aufgaben.** Beim ersten Start merkt sich der Dienst den
  aktuellen Zeitpunkt und arbeitet die Historie nicht nach. Bestehende VMs
  bleiben also unverändert.

---

## English

A small service for Proxmox VE. It watches the cluster task list and, once a
VM has been **created, cloned or restored from backup**, walks its disks and
fills in any missing IO limits.

Freshly restored VMs run unthrottled and a single one can saturate a storage
pool, hurting every other guest on the node. Limits are easy to forget — this
service adds them automatically.

Per-pool profiles are configured in YAML, with a mandatory `defaults`
section for any pool not listed explicitly. **Existing values are never
overwritten**; the service only fills gaps. It runs either locally on each
node via `pvesh` (`mode: local`) or once against the cluster API
(`mode: api`).

VMs only — Proxmox has no per-mountpoint throttling for LXC containers.

See [`config.example.yaml`](config.example.yaml) for all options.
