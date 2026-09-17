<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="logo/pve-optimizer-wordmark-dark.svg">
  <img src="logo/pve-optimizer-wordmark-light.svg" alt="pve-optimizer" width="420">
</picture>

**Proxmox VE lässt bei jeder neuen VM Einstellungen offen. Dieser Dienst trägt sie nach.**

IO-Begrenzungen je Speicherpool · Discard · SSD-Kennzeichen · IO-Thread ·
Gast-Agent · gestaffelter Start für VMs **und Container** — jede Prüfung
eine eigene Regel, je Node einstellbar, und nichts davon überschreibt, was
jemand bewusst gesetzt hat.

[Installation](#installation) · [Regeln](#regeln) · [Konfiguration](#konfiguration) · [Lizenz](#lizenz) · [English](#english)

</div>

---

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
   und `qmclone` — und bei Containern `vzcreate`, `vzrestore`, `vzclone`.
3. Liest die Konfiguration des betroffenen Gastes.
4. Lässt die eingeschalteten Regeln darüber laufen.
5. Schreibt einmal, was dabei zusammengekommen ist.

Übersprungen werden CD-ROM-Laufwerke sowie `efidisk`, `tpmstate` und
`unused*` — dort akzeptiert Proxmox keine Plattenoptionen.

Daneben kann er auf abgestürzte Dienste des Nodes aufpassen und sie wieder
hochholen — siehe [Dienst-Monitor](#dienst-monitor).

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

| Regel | Was sie ergänzt | Gilt für | Optionen |
|---|---|---|---|
| `io_limits` | die IO-Begrenzungen aus dem Profil des Pools | VMs | — |
| `discard` | `discard=on` | VMs | `pools` |
| `ssd` | `ssd=1` | VMs | `pools` |
| `iothread` | `iothread=1` | VMs | — |
| `guest_agent` | `agent=1` | VMs | `fstrim_cloned_disks` |
| `startup` | `up=<sekunden>` im Feld `startup` | VMs **und Container** | `up`, `vm`, `lxc` |

Ein `pools`-Eintrag schränkt die Regel auf die genannten Speicherpools
ein; ohne ihn gilt sie für alle.

Bis auf die Staffelung betrifft alles nur VMs: Proxmox kennt für Container
weder eine Drosselung je Mountpoint noch einen Gast-Agenten. Eine Regel,
die für die Gastart nicht gilt, läuft dort erst gar nicht.

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
Abstand, den Proxmox nach diesem Gast einhält, bevor der nächste startet.
Ohne das fährt alles, was automatisch mitstartet, gleichzeitig hoch und
erzeugt genau die Lastspitze, gegen die die IO-Begrenzung sonst arbeitet.

Als einzige Regel betrifft sie auch **Container** — ein Node fährt beide
Gastarten gemeinsam hoch. Weil ein Container in Sekunden oben ist, eine VM
aber erst ihr BIOS durchläuft, lässt sich der Abstand getrennt setzen:

```yaml
rules:
  startup:
    mode: enforce
    up: 30s          # gilt für beide, solange darunter nichts steht
    vm:
      up: 45s        # nur VMs
    lxc:
      up: 10s        # nur Container
```

Ein Abstand von `0` heißt hier wie überall: nicht setzen. `lxc: {up: 0}`
lässt Container also ganz in Ruhe.

Angefasst wird nur, was `onboot: 1` trägt — ob ein Gast mitstarten soll,
entscheidet der Betreiber, und eine fehlende Angabe ist hier keine
vergessene Einstellung.

Zwei Felder wirken erst beim nächsten Start der VM: `iothread` und der
Gast-Agent. Der Dienst startet dafür **nichts** neu.

### Dienst-Monitor

Proxmox liefert seine eigenen Dienste ohne `Restart=` aus. Stirbt
`pvestatd` an einem Signal, bleibt er liegen, bis jemand ihn von Hand
startet — auf einem unserer Nodes waren das im September 2026 gut
siebzehn Stunden ohne Statuswerte, ohne dass jemand etwas gemerkt hätte.

Der Monitor holt so einen Dienst wieder hoch. Blind endlos neu starten
hilft allerdings nicht: Ein Dienst, der immer wieder stirbt, hat eine
Ursache, die ein Neustart nicht behebt. Deshalb zählt der Monitor mit,
steigt nach einer einstellbaren Zahl von Versuchen aus und meldet sich
dann bei einem Menschen.

```yaml
services:
  mode: enforce
  units:
    - pvestatd.service
  restart_limit: 3      # so viele Neustarts, dann ist Schluss
  stable_after: 24h     # so lange durchgelaufen = wieder gesund
  mail:
    to: admin@example.com
    from: pve-optimizer@example.com
    server: mail.example.com:25
```

| Schlüssel | Vorgabe | Bedeutung |
|---|---|---|
| `mode` | `off` | `off`, `report` oder `enforce` — wie bei den Regeln |
| `units` | `[pvestatd.service]` | die zu überwachenden systemd-Units |
| `restart_limit` | `3` | Neustarts je Dienst, bevor der Monitor aufgibt |
| `stable_after` | `24h` | Laufzeit am Stück, nach der der Zähler auf null fällt |
| `mail` | — | wohin die Meldung geht; ohne den Abschnitt bleibt sie im Protokoll |

Was der Monitor **nicht** anfasst: einen Dienst, den jemand angehalten hat.
Der steht auf `inactive`, nicht auf `failed` — wer einen Dienst abschaltet,
will ihn nicht von einem Wächter wieder hochgeholt bekommen.

Im Modus `report` schreibt er den Absturz nur ins Protokoll; er startet
dann nichts neu und verschickt auch keine Mail. `dry_run` senkt ihn wie
jede Regel dorthin ab.

Der Zählerstand liegt als `services.json` neben dem `state_file` und
überdauert damit ein Update des Dienstes — sonst ließe sich die Grenze
durch einen Neustart aushebeln.

Der Monitor sieht immer nur die Maschine, auf der er läuft. Bei `mode: api`
ist das nicht zwangsläufig der Node, dessen Gäste der Dienst betreut.

**Zur Mail:** Der Weg geht bewusst über einen eigenen SMTP-Zugang und nicht
über das `sendmail` des Nodes. Ein frisch aufgesetzter Proxmox-Node hat zwar
ein postfix, aber keinen Relay und eine Platzhalteradresse als Empfänger —
eine Mail über diesen Weg landet in der Warteschlange und nie bei einem
Menschen. Verlangt der Server eine Anmeldung, gehört das Passwort nicht in
die Konfiguration, sondern in die Umgebungsvariable
`PVE_OPTIMIZER_SMTP_PASSWORD`.

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
    services:
      restart_limit: 5    # dieser Node hat eine Vorgeschichte
    pools:
      local-pool:         # gleicher Name, langsamere Platte
        mbps_wr: 80
    defaults:
      mbps_rd: 150
```

Bei einer Regel wird nur überschrieben, was der Node tatsächlich nennt: Wer
dort allein den Modus setzt, behält deren übrige Optionen aus dem
allgemeinen Abschnitt. Für den Dienst-Monitor gilt dasselbe — im Beispiel
oben erbt `vmh03` Modus, Units und Frist aus `services:` und ändert nur die
Zahl der Neustarts.

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

### Aus dem Techeve-Repository

Der empfohlene Weg — auf jedem Node, der den Dienst bekommen soll. Proxmox
ist Debian, das Paket kommt also aus `apt` wie alles andere auch:

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
schreibt in die Konfiguration laufender VMs, und womit, hängt an der
Hardware des Clusters — was ohne Zutun losliefe, wäre geraten. Das Paket
bringt deshalb nur die Vorlage mit:

```bash
cp /etc/pve-optimizer/config.example.yaml /etc/pve-optimizer/config.yaml
$EDITOR /etc/pve-optimizer/config.yaml       # dry_run: true stehen lassen
systemctl enable --now pve-optimizer
journalctl -u pve-optimizer -f               # was er tun würde
```

Sieht das Protokoll gut aus, `dry_run: false` setzen und neu starten.

Ein `apt upgrade` tauscht später das Binary und startet den Dienst neu,
sofern er eingerichtet ist. Die eigene `config.yaml` bleibt unangetastet;
die Vorlage daneben wird auf den neuen Stand gebracht.

### Umstieg von einer Handinstallation

Wer den Dienst bisher von Hand eingerichtet hat, hat ihn unter
`/usr/local/bin/pve-optimizer` liegen und seine Unit unter
`/etc/systemd/system/`. **Ein `apt install` allein reicht dort nicht** —
im Gegenteil, es sieht danach nur so aus, als wäre der Dienst aktuell:

Das Paket legt das Binary nach `/usr/bin` und die Unit nach
`/lib/systemd/system`. Systemd bevorzugt aber, was unter `/etc` steht. Die
alte Unit bleibt also maßgeblich und zeigt weiter auf `/usr/local/bin` —
das Paket-Binary wird nie gestartet. Das Installationsskript startet den
Dienst neu, weil er eingeschaltet ist, und bringt damit **die alte Version
wieder hoch**. Ein `pve-optimizer -version` am eingeschalteten Dienst
zeigt weiterhin den alten Stand.

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

Die eigene `/etc/pve-optimizer/config.yaml` bleibt dabei liegen und gilt
weiter. Zwei Dinge sind daran zu prüfen:

- Steht dort noch ein Pool in der alten Schreibweise `<node>:<pool>`,
  bricht der Dienst beim Start mit einem Hinweis ab — der Eintrag gehört
  unter [`nodes:`](#abweichungen-je-node).
- Ohne einen Abschnitt `rules:` bleibt es beim bisherigen Verhalten: Die
  IO-Begrenzung ist scharf, alle neuen Regeln sind aus.

### Von Hand

Ohne Repository — die Binaries hängen an jedem
[Release](https://gitlab.techeve.de/techeve/pve-optimizer/-/releases):

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

## Entwicklung

```bash
make            # Übersicht aller Ziele
make check      # fmt, vet, lint, test, govulncheck — das, was auch die CI prüft
make build      # Binary für den eigenen Rechner
make deb        # Debian-Pakete für amd64 und arm64
```

Vor jedem Push `make check` — die Pipeline prüft dasselbe.

## Bestehende Gäste nachziehen

Die laufende Beobachtung greift nur bei neuen Aufgaben — an bereits
vorhandenen Gästen bliebe also alles, wie es ist. Für den Rollout gibt es
deshalb einen einmaligen Durchlauf über alle VMs und Container des
Clusters:

```bash
pve-optimizer -config /etc/pve-optimizer/config.yaml -sweep
```

Erst mit `dry_run: true` laufen lassen und das Protokoll prüfen, dann scharf.
Der Durchlauf beendet sich nach getaner Arbeit; ein Gast, der sich nicht
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

- **An Containern nur die Staffelung.** Proxmox kennt für LXC keine
  Drosselung je Mountpoint und keinen Gast-Agenten; Drosselung ginge dort
  nur über cgroup-Limits für den ganzen Container. Der gestaffelte Start
  dagegen ist bei beiden Gastarten dasselbe Feld und wird deshalb auch bei
  Containern ergänzt.
- **Kein Neustart von Gästen.** Was erst beim nächsten Start der VM wirkt,
  wirkt erst dann.
- **Im laufenden Betrieb nur neue Aufgaben.** Beim ersten Start merkt sich
  der Dienst den aktuellen Zeitpunkt und arbeitet die Historie nicht nach —
  für bestehende Gäste ist `-sweep` da.

## Lizenz

[AGPL-3.0](LICENSE) — freie Nutzung, auch kommerziell. Wer den Dienst
verändert und betreibt, gibt die Änderungen unter derselben Lizenz weiter.
Keine Gewährleistung, keine Haftung.

## Über uns

pve-optimizer entsteht bei **[Techeve](https://techeve.de/?mtm_campaign=linking&mtm_kwd=README)** —
wir bauen Go-Backends, Weboberflächen und Werkzeuge für den eigenen Betrieb,
für Kunden und als eigene Produkte. Das meiste davon läuft dort, wo auch
dieser Dienst zu Hause ist: auf selbst betriebenen Servern.

Die Dokumentation aller Projekte steht unter
[doc.techeve.de](https://doc.techeve.de/?mtm_campaign=linking&mtm_kwd=README),
der Quelltext auf
[gitlab.techeve.de](https://gitlab.techeve.de/?mtm_campaign=linking&mtm_kwd=README).

Fragen, Fehler, Wünsche: gerne als Issue.

---

## English

A small service for Proxmox VE. It watches the cluster task list and, once
a guest — VM or container — has been **created, cloned or restored from
backup**, fills in what Proxmox leaves open: IO limits, discard, the SSD
flag, the guest agent, staggered start-up delays.

Each check is a rule with a mode of its own — `off`, `report` (log only) or
`enforce` (write). Rules, per-pool throttling profiles and their defaults
are configured in YAML and can be overridden **per node**. **Existing
values are never overwritten**; the service only fills gaps. It runs either
locally on each node via `pvesh` (`mode: local`) or once against the
cluster API (`mode: api`).

Disk and agent settings apply to VMs only — Proxmox has no per-mountpoint
throttling and no guest agent for LXC containers. The start-up stagger is
the same field on both, so containers get it too, with a delay of their
own.

See [`config.example.yaml`](config.example.yaml) for all options.

Licensed under the [AGPL-3.0](LICENSE). Built by
[Techeve](https://techeve.de/?mtm_campaign=linking&mtm_kwd=README).
