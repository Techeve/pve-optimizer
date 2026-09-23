---
title: pve-optimizer
description: Trägt nach, was Proxmox VE bei einer neuen VM offenlässt.
sidebar:
  order: 1
---

**Proxmox VE lässt bei jeder neuen VM Einstellungen offen. Dieser Dienst trägt sie nach.**

IO-Begrenzungen je Speicherpool · Discard · SSD-Kennzeichen · IO-Thread ·
Gast-Agent · gestaffelter Start für VMs **und Container** — jede Prüfung eine
eigene Regel, je Node einstellbar, und nichts davon überschreibt, was jemand
bewusst gesetzt hat.

## Warum

Frisch angelegte oder wiederhergestellte VMs laufen ohne Drosselung. Eine
einzige davon kann beim Hochfahren oder bei einem Restore den Speicher-Pool so
auslasten, dass alle anderen Gäste des Nodes darunter leiden. Die Limits von
Hand nachzutragen wird regelmäßig vergessen — genau das übernimmt dieser Dienst.

Dasselbe gilt für die übrigen Einstellungen, die Proxmox bei einer neuen VM
offenlässt: ohne Discard wächst eine dünn bereitgestellte Platte immer weiter,
ohne Gast-Agenten ist ein Backup nur so konsistent wie nach einem Stromausfall,
und ohne gestaffelte Startzeiten fährt nach einem Neustart des Nodes alles
gleichzeitig hoch. Jeder dieser Punkte ist eine eigene [Regel](regeln/), die
sich einzeln einschalten, nur beobachten oder je Node abweichend einstellen
lässt.

## Was er macht

1. Fragt regelmäßig `/cluster/tasks` ab.
2. Filtert auf abgeschlossene Aufgaben vom Typ `qmcreate`, `qmrestore` und
   `qmclone` — und bei Containern `vzcreate`, `vzrestore`, `vzclone`.
3. Liest die Konfiguration des betroffenen Gastes.
4. Lässt die eingeschalteten Regeln darüber laufen.
5. Schreibt einmal, was dabei zusammengekommen ist.

Übersprungen werden CD-ROM-Laufwerke sowie `efidisk`, `tpmstate` und
`unused*` — dort akzeptiert Proxmox keine Plattenoptionen.

Daneben kann er auf abgestürzte Dienste des Nodes aufpassen und sie wieder
hochholen — siehe [Dienst-Monitor](dienst-monitor/) — und auf ungünstige
Einstellungen hinweisen, ohne sie anzufassen — siehe
[Empfehlungen](empfehlungen/).

## Betriebsarten

| Modus | Zugriff | Installation | Token |
|---|---|---|---|
| `local` | `pvesh` auf dem Node | auf **jedem** Node | nicht nötig |
| `api` | HTTPS gegen die Cluster-API | **einmal**, auch außerhalb | nötig |

`local` ist der einfachere Weg und braucht keine Zugangsdaten, muss aber je
Node ausgerollt und aktualisiert werden. `api` sieht den ganzen Cluster aus
einer Installation heraus und läuft auch dann weiter, wenn ein Node neu startet.

### Achtung bei mehreren Instanzen

Auch im `local`-Modus greift `pvesh` **clusterweit** zu — eine Instanz sieht
also alle Nodes. Läuft der Dienst auf jedem Node, müssen sich die Instanzen
deshalb aufteilen, sonst nehmen sich mehrere dieselbe VM gleichzeitig vor.

Dafür sorgt `only_own_node`, das im `local`-Modus **von selbst aktiv** ist:
Jede Instanz bearbeitet nur die VMs ihres eigenen Nodes. Der Node-Name kommt
aus dem Hostnamen und lässt sich mit `node:` überschreiben.

Im `api`-Modus ist die Option aus — dort genügt eine Installation für den
ganzen Cluster.

## Grenzen

- **An Containern nur Staffelung und Netzbegrenzung.** Proxmox kennt für LXC
  keine Drosselung je Mountpoint und keinen Gast-Agenten; Drosselung ginge dort
  nur über cgroup-Limits für den ganzen Container.
- **Kein Neustart von Gästen.** Was erst beim nächsten Start der VM wirkt,
  wirkt erst dann.
- **Im laufenden Betrieb nur neue Aufgaben.** Beim ersten Start merkt sich der
  Dienst den aktuellen Zeitpunkt und arbeitet die Historie nicht nach — für
  bestehende Gäste gibt es den [einmaligen Durchlauf](installation/#bestehende-gäste-nachziehen).

## Lizenz

AGPL-3.0 — freie Nutzung, auch kommerziell. Wer den Dienst verändert und
betreibt, gibt die Änderungen unter derselben Lizenz weiter. Keine
Gewährleistung, keine Haftung. Quelltext und Releases auf
[gitlab.techeve.de](https://gitlab.techeve.de/techeve/pve-optimizer?mtm_campaign=linking&mtm_kwd=doc).
