---
title: Regeln
description: Welche Einstellungen pve-optimizer nachträgt und wie weit jede Regel dabei geht.
sidebar:
  order: 4
---

Jede Regel prüft einen Punkt und trägt nach, was fehlt. Wie weit sie dabei geht,
bestimmt ihr Modus:

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

Ohne den Abschnitt ist `io_limits` scharf und alles Weitere aus — ein Update
ändert also von sich aus nichts am Verhalten. `dry_run: true` senkt jede scharfe
Regel auf `report` ab.

**Bereits gesetzte Werte bleiben immer unangetastet.** Eine Regel ergänzt nur,
was fehlt.

## Übersicht

| Regel | Was sie ergänzt | Gilt für | Optionen |
|---|---|---|---|
| `io_limits` | die IO-Begrenzungen aus dem Profil des Pools | VMs | — |
| `discard` | `discard=on` | VMs | `pools` |
| `ssd` | `ssd=1` | VMs | `pools` |
| `iothread` | `iothread=1` | VMs | — |
| `guest_agent` | `agent=1` | VMs | `fstrim_cloned_disks` |
| `startup` | `up=<sekunden>` im Feld `startup` | VMs **und Container** | `up`, `vm`, `lxc` |
| `net_rate` | `rate=<MB/s>` an jeder Netzwerkkarte | VMs **und Container** | `rate`, `vm`, `lxc` |

Ein `pools`-Eintrag schränkt die Regel auf die genannten Speicherpools ein; ohne
ihn gilt sie für alle.

Bis auf Staffelung und Netzbegrenzung betrifft alles nur VMs: Proxmox kennt für
Container weder eine Drosselung je Mountpoint noch einen Gast-Agenten. Eine
Regel, die für die Gastart nicht gilt, läuft dort erst gar nicht.

Zwei Felder wirken erst beim nächsten Start der VM: `iothread` und der
Gast-Agent. Der Dienst startet dafür **nichts** neu. Eine geänderte `rate`
greift dagegen sofort.

## io_limits

Setzt die Durchsatz- und IOPS-Grenzen aus dem Profil des Speicherpools, auf dem
die Platte liegt. Wie die Profile aufgebaut sind und welches greift, steht unter
[Konfiguration](../konfiguration/#die-schlüssel-der-drosselprofile).

## discard

Gibt gelöschte Blöcke an den Speicher zurück. Ohne das wächst eine dünn
bereitgestellte Platte immer weiter, auch wenn im Gast aufgeräumt wird.

## ssd

Meldet dem Gast, dass die Platte nicht dreht — Linux und Windows schalten
daraufhin die Optimierungen für Magnetplatten ab und schicken von sich aus
TRIM-Befehle. virtio-blk kennt das Kennzeichen nicht und wird übersprungen.

## iothread

Gibt der Platte einen eigenen Thread. Am SCSI-Bus geht das nur mit
`scsihw: virtio-scsi-single`; steht dort etwas anderes, wird die Platte
übersprungen. Den Controller stellt der Dienst **nicht** um — das ist ein
Eingriff am Gast, nach dem manches Windows nicht mehr bootet.

## guest_agent

Erlaubt den QEMU-Gast-Agenten. Ohne ihn ist ein Herunterfahren nur ein Druck
auf den Ausschalter, und beim Backup fehlt das Einfrieren des Dateisystems — die
Sicherung ist dann nur so konsistent wie nach einem Stromausfall. Im Gast muss
der Agent zusätzlich installiert sein; ist er nicht da, ändert das Kennzeichen
nichts.

## startup

Staffelt den Start nach einem Neustart des Nodes: `up` ist der Abstand, den
Proxmox nach diesem Gast einhält, bevor der nächste startet. Ohne das fährt
alles, was automatisch mitstartet, gleichzeitig hoch und erzeugt genau die
Lastspitze, gegen die die IO-Begrenzung sonst arbeitet.

Die Regel betrifft auch **Container** — ein Node fährt beide Gastarten
gemeinsam hoch. Weil ein Container in Sekunden oben ist, eine VM aber erst ihr
BIOS durchläuft, lässt sich der Abstand getrennt setzen:

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

Ein Abstand von `0` heißt hier wie überall: nicht setzen. `lxc: {up: 0}` lässt
Container also ganz in Ruhe.

Angefasst wird nur, was `onboot: 1` trägt — ob ein Gast mitstarten soll,
entscheidet der Betreiber, und eine fehlende Angabe ist hier keine vergessene
Einstellung.

## net_rate

Begrenzt den Durchsatz je Netzwerkkarte. Ohne Begrenzung zieht ein einzelner
Gast die Leitung des Nodes leer — ein Backup, ein Klon oder ein durchgedrehter
Dienst genügt, und alle anderen Gäste auf derselben Brücke hängen mit. Das ist
dasselbe Problem, gegen das `io_limits` an der Platte arbeitet, nur eine Etage
weiter.

Proxmox kennt `rate` bei VMs und Containern gleichermaßen, deshalb gilt die
Regel für beide — getrennt einstellbar wie die Staffelung:

```yaml
rules:
  net_rate:
    mode: enforce
    rate: 125        # gilt für beide, solange darunter nichts steht
    vm:
      rate: 200      # nur VMs
    lxc:
      rate: 12.5     # nur Container
```

:::caution[Einheit]
Die Einheit ist **Megabyte je Sekunde**, so wie Proxmox sie führt — nicht
Megabit. Eine Gigabit-Leitung sind also `125`, 100 Mbit/s sind `12.5`.
Nachkommastellen sind erlaubt.
:::

Eine Rate von `0` heißt hier wie überall: nicht setzen. `lxc: {rate: 0}` lässt
Container also ganz in Ruhe.

Ergänzt wird an **jeder** Netzwerkkarte des Gastes, die noch keine Begrenzung
trägt — `net0`, `net1` und so fort. Eine bereits gesetzte Rate bleibt
unangetastet, auch eine höhere.

Was die Regel **nicht** kann: nach Brücke unterscheiden. Hängt ein Gast mit
einer Karte am 1G-LAN und mit einer zweiten am 10G-Speichernetz, bekommen beide
denselben Wert. Wer das braucht, setzt die zweite Karte von Hand — gesetzte
Werte rührt der Dienst nicht an.
