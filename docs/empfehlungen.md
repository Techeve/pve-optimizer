---
title: Empfehlungen und Bericht
description: Prüfungen, die auf ungünstige Einstellungen hinweisen, ohne etwas zu ändern — auf Wunsch als regelmäßige Mail.
sidebar:
  order: 5
---

Manches gehört in Menschenhand. Welche VM in welchen Sicherungsauftrag gehört,
mit welchem Zeitplan und welcher Aufbewahrung, weiß der Dienst nicht — und er
soll es auch nicht raten.

Für solche Fälle gibt es **Empfehlungen**: Prüfungen, die nachsehen und sagen,
was auffällt, warum es zählt und was zu tun ist. Sie ändern **nichts**. Diese
Grenze steckt in der Bauweise, nicht in einem guten Vorsatz: Eine Prüfung
bekommt einen Zugang zu Proxmox, der keine einzige schreibende Methode hat.

```bash
pve-optimizer -config /etc/pve-optimizer/config.yaml -check
```

```
2 Befund(e):

backup_coverage
  - VM 3000 (Test-VM) auf pve02
  - Container 101 (Wegwerf-CT) auf pve03

  Warum:   Kein Sicherungsauftrag erfasst diesen Gast. Geht der Speicher verloren, ist er weg.
  Was tun: Unter Rechenzentrum → Backup einem Auftrag hinzufügen. Braucht der Gast
           bewusst keine Sicherung, gehört seine VMID unter advice.backup_coverage.ignore.
```

Befunde mit derselben Begründung werden zusammengefasst — vier ungesicherte
Gäste tragen die Erklärung einmal, nicht viermal.

## Die Prüfungen

| Prüfung | Was sie nachsieht | Optionen |
|---|---|---|
| `backup_coverage` | Gäste, die kein Sicherungsauftrag erfasst | `ignore` |
| `bandwidth_limits` | fehlende Bandbreitengrenzen des Rechenzentrums | — |
| `replication_rate` | Replikationsaufträge ohne Ratenbegrenzung | — |
| `zfs_health` | Pools mit Fehlerzählern oder gestörten Geräten | — |
| `zfs_redundancy` | Pools, die einen Ausfall nicht ausgleichen können | — |
| `memory_overcommit` | Nodes, deren Gäste mehr Speicher zugesagt haben als da ist | `max_percent` |

```yaml
advice:
  backup_coverage:
    mode: report
    ignore: [101, 3000]    # brauchen bewusst keine Sicherung
  bandwidth_limits: report
  replication_rate: off
```

Anders als die Regeln sind Empfehlungen **von Haus aus an**. Sie ändern nichts,
und was man nicht sieht, kann man nicht abstellen. Erlaubt sind nur `off` und
`report` — wer `enforce` einträgt, bekommt beim Start einen Fehler statt einer
stillen Erwartung, die der Dienst nie erfüllt. `dry_run` wirkt hier nicht: Es
gäbe nichts abzuschwächen.

### backup_coverage

Die Frage, welche Gäste ein Auftrag erfasst, beantwortet **Proxmox selbst**
(`/cluster/backup-info/not-backed-up`). Nachgebaut ist hier nichts — die
Feinheiten von `all`, `pool` und `exclude` müssten sonst bei jeder
Proxmox-Fassung nachgezogen werden.

Der häufigste Weg in die Lücke ist harmlos und deshalb tückisch: Ein Auftrag mit
fester VMID-Liste erfasst neue Gäste nicht. Wer eine VM anlegt und den Auftrag
nicht anfasst, hat sie schlicht nicht gesichert.

Gäste, die bewusst keine Sicherung brauchen, gehören unter `ignore`. Die Prüfung
meldet dabei auch **verwaiste Einträge** — Nummern auf der Liste, zu denen es
keinen Gast mehr gibt. Proxmox vergibt gelöschte VMIDs wieder, und ein alter
Eintrag würde sonst irgendwann stillschweigend einen neuen Gast von der Prüfung
ausnehmen.

### bandwidth_limits

Ein ungebremster Vorgang zieht die Leitung leer. Das trifft nicht nur die Gäste
— teilt sich der Cluster-Verkehr dieselbe Leitung, kann ein Wiederherstellen
Corosync abschnüren, und im schlimmsten Fall verlieren die Nodes darüber ihr
Quorum.

:::caution[Einheit]
Proxmox rechnet unter *Rechenzentrum → Optionen* in **KiB/s**, nicht in MB/s
wie an Platten und Netzwerkkarten.
:::

### zfs_health

Auf einem Node ohne ECC-Speicher ist ZFS oft das einzige Warnsignal, das
überhaupt kommt. Es prüft jeden gelesenen Block gegen seine Prüfsumme und merkt
damit Verfälschungen, die weder SMART noch die Machine-Check-Zähler je sehen.

Einen Befund gibt es deshalb zusätzlich: Zeigen **mehrere Pools** eines Nodes
Fehlerzähler, meldet die Prüfung das eigens. Zwei unabhängige Laufwerke fangen
nicht gleichzeitig an, Daten zu verfälschen — dann liegt die Ursache oberhalb der
Laufwerke, im Arbeitsspeicher, im Speichercontroller oder auf dem PCIe-Pfad.
Melden die Laufwerke dazu null Medienfehler, sind sie wahrscheinlich
unschuldig. Ein memtest findet so etwas oft **nicht**, weil er den
Speichercontroller unter gemischter Last nicht nachbildet.

### zfs_redundancy

Ohne zweite Kopie erkennt ZFS einen Prüfsummenfehler zwar, kann ihn aber nicht
beheben — die Datei ist dann verloren. Nachrüsten lässt sich das nicht, ein Pool
wächst nicht zum Spiegel. Der Hinweis zielt deshalb auf das, was bleibt:
Sicherung und regelmäßiger Scrub, damit ein Fehler auffällt, solange die
Sicherung noch gut ist.

Ein Node, der **offline** steht, wird bei beiden ZFS-Prüfungen übersprungen. Ihn
als gesund zu melden wäre falsch, ihn als gestört zu melden auch — es ist
schlicht nichts bekannt.

### memory_overcommit

Gezählt werden die **laufenden** Gäste; gestoppte und Vorlagen belegen nichts.
Der Host braucht selbst Speicher — ZFS für seinen Lesezwischenspeicher, eine
Sicherung für ihre Puffer, der Kernel ohnehin. Die Grenze steht auf 85 % und
lässt sich über `max_percent` verschieben.

## Regelmäßiger Bericht

Die Empfehlungen von Hand aufzurufen hilft nur dem, der daran denkt. Der Bericht
sieht in festem Abstand selbst nach und meldet sich per Mail:

```yaml
report:
  mode: enforce     # off = aus, report = nur ins Protokoll, enforce = Mail
  node: vmh01       # nur dieser Node verschickt
  every: 168h       # eine Woche
```

| Schlüssel | Vorgabe | Bedeutung |
|---|---|---|
| `mode` | `off` | `off`, `report` oder `enforce` — wie überall |
| `node` | — | welcher Node verschickt; **Pflicht**, wenn der Bericht an ist |
| `every` | `168h` | Abstand zwischen zwei Berichten |

Wohin die Mail geht, steht unter [Mailzugang](../dienst-monitor/#mailzugang).

**Warum `node` Pflicht ist:** Läuft der Dienst auf jedem Node, würde ohne diese
Angabe jede Instanz denselben Bericht verschicken — bei drei Nodes also drei
gleiche Mails. Fehlt die Angabe bei eingeschaltetem Bericht, startet der Dienst
nicht und sagt warum.

**Verschickt wird nur, wenn es etwas zu melden gibt.** Eine Mail, die jede Woche
„alles in Ordnung" sagt, liest nach dem vierten Mal niemand mehr — und dann auch
die nicht mehr, in der etwas steht.

Der Bericht enthält, was die eingeschalteten Prüfungen gefunden haben. Wer einen
Punkt bewusst so lassen will, schaltet die zugehörige Prüfung ab; sie taucht
dann auch im Bericht nicht mehr auf. Das ist die Lautstärkeregelung.

**Der erste Bericht geht gleich nach dem Einschalten hinaus.** So zeigt sich
sofort, ob der Mailweg steht — und nicht erst in einer Woche, wenn der erste
Bericht ausbleibt und niemand weiß, ob das gut oder schlecht ist. Der Zeitpunkt
des letzten Versands liegt als `report.json` neben dem `state_file`, damit ein
Update des Dienstes den Rhythmus nicht von vorn beginnen lässt.

Zwei Fälle gelten ausdrücklich **nicht** als erledigt und werden beim nächsten
Durchlauf wiederholt: ein fehlgeschlagener Versand, und ein Durchlauf, in dem
eine Prüfung nicht laufen konnte. Sonst fiele der Bericht dieser Woche aus, weil
die API zwei Minuten nicht erreichbar war.
