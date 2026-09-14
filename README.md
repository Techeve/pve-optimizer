# pve-optimizer

Ein kleiner Dienst für Proxmox VE. Er beobachtet die Aufgabenliste des
Clusters und trägt an neu angelegten, geklonten oder wiederhergestellten
VMs nach, was Proxmox offenlässt: IO-Begrenzungen, Discard, das
SSD-Kennzeichen, den Gast-Agenten, gestaffelte Startzeiten.

**Bereits gesetzte Werte bleiben unangetastet.** Der Dienst ergänzt nur,
er korrigiert nicht.

## Warum

Frisch angelegte oder wiederhergestellte VMs laufen ohne Drosselung. Eine
einzige davon kann beim Hochfahren oder bei einem Restore den Speicher-Pool
so auslasten, dass alle anderen Gäste des Nodes darunter leiden. Die Limits
von Hand nachzutragen wird regelmäßig vergessen — genau das übernimmt dieser
Dienst.

Dasselbe gilt für die übrigen Einstellungen, die Proxmox bei einer neuen VM
offenlässt: ohne Discard wächst eine dünn bereitgestellte Platte immer
weiter, ohne Gast-Agenten ist ein Backup nur so konsistent wie nach einem
Stromausfall, und ohne gestaffelte Startzeiten fährt nach einem Neustart des
Nodes alles gleichzeitig hoch. Jeder dieser Punkte ist eine eigene Regel,
die sich einzeln einschalten, nur beobachten oder je Node abweichend
einstellen lässt.

## Was er macht

1. Fragt regelmäßig `/cluster/tasks` ab.
2. Filtert auf abgeschlossene Aufgaben vom Typ `qmcreate`, `qmrestore`
   und `qmclone`.
3. Liest die Konfiguration der betroffenen VM.
4. Lässt die eingeschalteten Regeln darüber laufen.
5. Schreibt einmal, was dabei zusammengekommen ist.

Übersprungen werden CD-ROM-Laufwerke sowie `efidisk`, `tpmstate` und
`unused*` — dort akzeptiert Proxmox keine Plattenoptionen.

## Betriebsarten

| Modus | Zugriff | Installation | Token |
|---|---|---|---|
| `local` | `pvesh` auf dem Node | auf **jedem** Node | nicht nötig |
| `api` | HTTPS gegen die Cluster-API | **einmal**, auch außerhalb | nötig |

`local` ist der einfachere Weg und braucht keine Zugangsdaten, muss aber je
Node ausgerollt und aktualisiert werden. `api` sieht den ganzen Cluster aus
einer Installation heraus und läuft auch dann weiter, wenn ein Node neu
startet.

### Achtung bei mehreren Instanzen

Auch im `local`-Modus greift `pvesh` **clusterweit** zu — eine Instanz sieht
also alle Nodes. Läuft der Dienst auf jedem Node, müssen sich die Instanzen
deshalb aufteilen, sonst nehmen sich mehrere dieselbe VM gleichzeitig vor.

Dafür sorgt `only_own_node`, das im `local`-Modus **von selbst aktiv** ist:
Jede Instanz bearbeitet nur die VMs ihres eigenen Nodes. Der Node-Name kommt
aus dem Hostnamen und lässt sich mit `node:` überschreiben.

Im `api`-Modus ist die Option aus — dort genügt eine Installation für den
ganzen Cluster.

## Konfiguration

Vorlage: [`config.example.yaml`](config.example.yaml).

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

### Regeln

Jede Regel prüft einen Punkt und trägt nach, was fehlt. Wie weit sie dabei
geht, bestimmt ihr Modus:

| Modus | Wirkung |
|---|---|
| `off` | die Regel läuft nicht |
| `report` | die Regel protokolliert, was sie ergänzen würde, schreibt aber nichts |
| `enforce` | die Regel schreibt |

Kurzform ist der Modus allein, Langform eine Zuordnung mit `mode` und den
Optionen der Regel:

```yaml
rules:
  io_limits: enforce
  iothread: report
  discard:
    mode: enforce
    pools: [local-pool]
  startup:
    mode: enforce
    up: 30s
```

Ohne den Abschnitt ist `io_limits` scharf und alles Weitere aus — ein
Update ändert also von sich aus nichts am Verhalten. `dry_run: true` senkt
jede scharfe Regel auf `report` ab.

| Regel | Was sie ergänzt | Optionen |
|---|---|---|
| `io_limits` | die IO-Begrenzungen aus dem Profil des Pools | — |
| `discard` | `discard=on` | `pools` |
| `ssd` | `ssd=1` | `pools` |
| `iothread` | `iothread=1` | — |
| `guest_agent` | `agent=1` | `fstrim_cloned_disks` |
| `startup` | `up=<sekunden>` im Feld `startup` | `up` |

Ein `pools`-Eintrag schränkt die Regel auf die genannten Speicherpools
ein; ohne ihn gilt sie für alle.

Zu den einzelnen Regeln:

**`discard`** gibt gelöschte Blöcke an den Speicher zurück. Ohne das wächst
eine dünn bereitgestellte Platte immer weiter, auch wenn im Gast aufgeräumt
wird.

**`ssd`** meldet dem Gast, dass die Platte nicht dreht — Linux und Windows
schalten daraufhin die Optimierungen für Magnetplatten ab und schicken von
sich aus TRIM-Befehle. virtio-blk kennt das Kennzeichen nicht und wird
übersprungen.

**`iothread`** gibt der Platte einen eigenen Thread. Am SCSI-Bus geht das
nur mit `scsihw: virtio-scsi-single`; steht dort etwas anderes, wird die
Platte übersprungen. Den Controller stellt der Dienst **nicht** um — das
ist ein Eingriff am Gast, nach dem manches Windows nicht mehr bootet.

**`guest_agent`** erlaubt den QEMU-Gast-Agenten. Ohne ihn ist ein
Herunterfahren nur ein Druck auf den Ausschalter, und beim Backup fehlt das
Einfrieren des Dateisystems — die Sicherung ist dann nur so konsistent wie
nach einem Stromausfall. Im Gast muss der Agent zusätzlich installiert
sein; ist er nicht da, ändert das Kennzeichen nichts.

**`startup`** staffelt den Start nach einem Neustart des Nodes: `up` ist der
Abstand, den Proxmox nach dieser VM einhält, bevor die nächste startet.
Ohne das fahren alle automatisch startenden VMs gleichzeitig hoch und
erzeugen genau die Lastspitze, gegen die die IO-Begrenzung sonst arbeitet.
Angefasst werden nur VMs mit `onboot: 1` — ob eine VM mitstarten soll,
entscheidet der Betreiber, und eine fehlende Angabe ist hier keine
vergessene Einstellung.

Zwei Felder wirken erst beim nächsten Start der VM: `iothread` und der
Gast-Agent. Der Dienst startet dafür **nichts** neu.

### Abweichungen je Node

Alles Bisherige gilt clusterweit. Unter `nodes:` steht, was auf einzelnen
Nodes anders ist — genannt wird nur das Abweichende:

```yaml
rules:
  discard: enforce
  iothread: enforce

nodes:
  vmh03:
    rules:
      discard: off        # hier hängt ein Speicher ohne Discard
      iothread: report    # neu im Cluster, erst einmal beobachten
    pools:
      local-pool:         # gleicher Name, langsamere Platte
        mbps_wr: 80
    defaults:
      mbps_rd: 150
```

Bei einer Regel wird nur überschrieben, was der Node tatsächlich nennt: Wer
dort allein den Modus setzt, behält deren übrige Optionen aus dem
allgemeinen Abschnitt.

Ein Profil wird von speziell nach allgemein gesucht:

```
nodes.<node>.pools.<pool>   dieser Pool auf diesem Node
pools.<pool>                dieser Pool auf allen Nodes
nodes.<node>.defaults       alles Übrige auf diesem Node
defaults                    alles Übrige
```

Welches Profil gegriffen hat, steht im Protokoll.

### Die Burst-Dauer ist in der Weboberfläche unsichtbar

Proxmox bietet unter *Disk → Bandwidth* nur die Dauer- und die Spitzenrate
an — Felder für `_max_length` gibt es dort nicht. Die Werte, die dieser
Dienst setzt, sind in der Oberfläche also **nicht zu sehen**.

Sie gehen dabei aber auch nicht verloren: Die Oberfläche liest beim
Bearbeiten alle Parameter einer Platte ein und schreibt sie unverändert
zurück, auch die, für die sie kein Eingabefeld hat. An den Bandbreiten einer
Platte lässt sich also gefahrlos über die Oberfläche schrauben.

Nachsehen lassen sie sich auf dem Node:

```bash
qm config 100 | grep scsi0
```

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

## Bestehende VMs nachziehen

Die laufende Beobachtung greift nur bei neuen Aufgaben — an bereits
vorhandenen VMs bliebe also alles, wie es ist. Für den Rollout gibt es
deshalb einen einmaligen Durchlauf über alle VMs des Clusters:

```bash
pve-optimizer -config /etc/pve-optimizer/config.yaml -sweep
```

Erst mit `dry_run: true` laufen lassen und das Protokoll prüfen, dann scharf.
Der Durchlauf beendet sich nach getaner Arbeit; eine VM, die sich nicht
anpassen lässt, bricht ihn nicht ab, sondern wird am Ende gemeldet.

## Sinnvolle Werte finden

Die Werte hängen an der Hardware. Ein durchgerechnetes Beispiel für eine
NVMe der 4. Generation mit rund zehn VMs steht in
[`config.example.yaml`](config.example.yaml) unter `nvme-gen4`.

Die Logik dahinter in Kurzform:

- **Dauerrate** so wählen, dass alle VMs zusammen die Platte nicht
  überfahren — bei zehn VMs also etwa ein Zehntel dessen, was die Platte
  dauerhaft leistet, plus etwas Luft.
- **Schreiben strenger begrenzen als Lesen.** Die Datenblattwerte gelten,
  solange der SLC-Cache reicht; danach bricht die Rate deutlich ein.
- **Burst großzügig, aber kurz.** Booten, ein Paket installieren, eine Datei
  kopieren — all das dauert Sekunden und soll mit voller Geschwindigkeit
  laufen. Erst dauerhafte Last fällt auf die Dauerrate zurück. Genau das
  trifft Restores, also die Fälle, in denen eine VM den Node lahmlegt.
- **IOPS aus der Praxis, nicht aus dem Datenblatt.** Die genannten
  Hunderttausende gelten bei einem Strom mit hoher Warteschlange, nicht bei
  zehn VMs mit gemischter Last.

## Grenzen

- **Nur VMs, keine Container.** Proxmox kennt für LXC keine Drosselung je
  Mountpoint; dort ginge das nur über cgroup-Limits für den ganzen
  Container.
- **Kein Neustart von Gästen.** Was erst beim nächsten Start der VM wirkt,
  wirkt erst dann.
- **Im laufenden Betrieb nur neue Aufgaben.** Beim ersten Start merkt sich
  der Dienst den aktuellen Zeitpunkt und arbeitet die Historie nicht nach —
  für bestehende VMs ist `-sweep` da.

---

## English

A small service for Proxmox VE. It watches the cluster task list and, once
a VM has been **created, cloned or restored from backup**, fills in what
Proxmox leaves open: IO limits, discard, the SSD flag, the guest agent,
staggered start-up delays.

Each check is a rule with a mode of its own — `off`, `report` (log only) or
`enforce` (write). Rules, per-pool throttling profiles and their defaults
are configured in YAML and can be overridden **per node**. **Existing
values are never overwritten**; the service only fills gaps. It runs either
locally on each node via `pvesh` (`mode: local`) or once against the
cluster API (`mode: api`).

VMs only — Proxmox has no per-mountpoint throttling for LXC containers.

See [`config.example.yaml`](config.example.yaml) for all options.
