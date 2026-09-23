---
title: Dienst-Monitor und Mail
description: Abgestürzte Proxmox-Dienste wieder hochholen und Meldungen per Mail verschicken.
sidebar:
  order: 6
---

## Dienst-Monitor

Proxmox liefert seine eigenen Dienste ohne `Restart=` aus. Stirbt `pvestatd` an
einem Signal, bleibt er liegen, bis jemand ihn von Hand startet — auf einem
unserer Nodes waren das im September 2026 gut siebzehn Stunden ohne
Statuswerte, ohne dass jemand etwas gemerkt hätte.

Der Monitor holt so einen Dienst wieder hoch. Blind endlos neu starten hilft
allerdings nicht: Ein Dienst, der immer wieder stirbt, hat eine Ursache, die ein
Neustart nicht behebt. Deshalb zählt der Monitor mit, steigt nach einer
einstellbaren Zahl von Versuchen aus und meldet sich dann bei einem Menschen.

```yaml
services:
  mode: enforce
  units:
    - pvestatd.service
  restart_limit: 3      # so viele Neustarts, dann ist Schluss
  stable_after: 24h     # so lange durchgelaufen = wieder gesund
```

| Schlüssel | Vorgabe | Bedeutung |
|---|---|---|
| `mode` | `off` | `off`, `report` oder `enforce` — wie bei den Regeln |
| `units` | `[pvestatd.service]` | die zu überwachenden systemd-Units |
| `restart_limit` | `3` | Neustarts je Dienst, bevor der Monitor aufgibt |
| `stable_after` | `24h` | Laufzeit am Stück, nach der der Zähler auf null fällt |

Was der Monitor **nicht** anfasst: einen Dienst, den jemand angehalten hat. Der
steht auf `inactive`, nicht auf `failed` — wer einen Dienst abschaltet, will ihn
nicht von einem Wächter wieder hochgeholt bekommen.

Im Modus `report` schreibt er den Absturz nur ins Protokoll; er startet dann
nichts neu und verschickt auch keine Mail. `dry_run` senkt ihn wie jede Regel
dorthin ab.

Der Zählerstand liegt als `services.json` neben dem `state_file` und überdauert
damit ein Update des Dienstes — sonst ließe sich die Grenze durch einen Neustart
aushebeln.

Der Monitor sieht immer nur die Maschine, auf der er läuft. Bei `mode: api` ist
das nicht zwangsläufig der Node, dessen Gäste der Dienst betreut.

## Mailzugang

Ein Zugang für den ganzen Dienst — der Dienst-Monitor benutzt ihn, der
[regelmäßige Bericht](../empfehlungen/#regelmäßiger-bericht) ebenfalls:

```yaml
mail:
  to: admin@example.com
  server: mail.example.com:25
  # from: pve-optimizer@vmh03      # Vorgabe: aus dem Rechnernamen
  # username: pve                  # nur, wenn der Server eine Anmeldung verlangt
```

`from` darf fehlen — dann bildet der Dienst die Adresse aus dem Rechnernamen,
und im Posteingang steht, von welchem Node die Meldung kam. Verlangt der Server
eine Anmeldung, gehört das Passwort **nicht** hierher, sondern in die
Umgebungsvariable `PVE_OPTIMIZER_SMTP_PASSWORD` (siehe systemd-Unit).

Ohne diesen Abschnitt bleibt jede Meldung im Protokoll. Das ist eine gültige
Wahl: Der Dienst-Monitor startet abgestürzte Dienste auch dann wieder.

**Warum ein eigener SMTP-Zugang und nicht das `sendmail` des Nodes?** Ein frisch
aufgesetzter Proxmox-Node hat zwar ein postfix, aber keinen Relay und eine
Platzhalteradresse als Empfänger. Eine Mail über diesen Weg landet in der
Warteschlange und nie bei einem Menschen — und eine Warnung, die niemand
bekommt, ist schlimmer als keine.

:::note[Umstieg von v0.5.x]
Der Zugang stand vorher unter `services.mail`. Wer ihn dort stehen lässt,
bekommt beim Start einen Fehler mit dem Hinweis auf den neuen Platz —
stillschweigend überlesen wird er nicht, sonst liefe der Dienst und die Warnung
käme nie an.
:::
