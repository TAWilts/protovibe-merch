# Merch Manager

Eine bewusst schlanke, selbst gehostete Merch-Verwaltung für eine Band.  Die
Anwendung läuft als einzelnes Python-/Flask-Containerprojekt auf der Synology
und ersetzt die festen Tabellenbereiche und Formelketten der bisherigen ODS.

Sie ist kein öffentlicher Onlineshop und kein großes ERP.  Ihr Zweck ist die
schnelle, nachvollziehbare Erfassung am Merch-Stand: Verkauf, Wareneingang,
Bestand, Bilanz und CSV-Export.

## Enthaltene Funktionen

- **Verkauf:** links Artikelauswahl, mittig generische Optionen, rechts
  Zahlung/Abholung/Kontaktdaten/Menge und ein Warenkorb. Mit **Artikel
  hinzufügen** bleiben Artikel- und Optionsauswahl stehen; mehrere Positionen
  werden gemeinsam unter einer Beleg-ID gespeichert. Die Beleg-ID wird vor dem
  Speichern angezeigt und nach erfolgreichem Speichern bestätigt. Ein fehlender
  Lagerbestand blockiert keinen Verkauf; bei Direktübergabe weist die App
  vorher sichtbar darauf hin. Bei nicht bezahlten oder noch nicht übergebenen
  Artikeln sind Name und Adresse Pflicht. Das optionale Feld **Verkauft von**
  bleibt für die nächsten Eingaben erhalten.
- **Artikeloptionen:** ein Artikel kann beliebige Optionsspalten haben, etwa
  `Farbe`, `Größe`, `Schnitt` oder `Material`.  Aus den Werten erzeugt die App
  automatisch die gültigen Varianten. Die Varianten-/Preistabelle aktualisiert
  sich bereits beim Bearbeiten der Optionen. Neue Artikel starten mit `Schwarz`,
  `Weiß` sowie `S` bis `XXL`, die jederzeit editierbar sind.
- **Einkäufe:** der zuletzt für eine Variante bezahlte Preis wird übernommen,
  kann aber pro Einkauf geändert werden.
- **Historie:** jeder Kauf erscheint als Beleg, dessen Warenkorb sich über den
  Pfeil links aufklappen lässt – inklusive Kontakt, Bezahlt-/Erhalten-Status,
  Bezahlart, Spende und Kommentar.
- **Offene Vorgänge:** nicht direkt übergebene Artikel können von „Noch nicht
  versendet“ über „Versendet“ bis „Erhalten“ geführt werden. Nicht bezahlte
  Verkäufe lassen sich dort separat als bezahlt markieren. Beide abgeschlossenen
  Vorgänge erscheinen jeweils in einer getrennten Historie unterhalb der offenen
  Listen.
- **Angebotssteuerung:** Artikel oder einzelne Varianten lassen sich aus dem
  Verkauf nehmen, ohne ihre Historie oder ihren Bestand zu verlieren. Sie
  verschwinden sofort aus dem Verkaufsfenster, bleiben aber für Einkäufe,
  Bilanzen und Exporte verfügbar.
- **Bilanzen:** gekauft, verkauft und Bestand je Variante sowie Ausgaben,
  Umsatz, Spenden, Saldo und Angebotsstatus. Pro Variante lassen sich optionale
  Mindestbestände hinterlegen; unterschrittene Grenzwerte werden in der Bilanz
  und vor einem direkten Verkauf hervorgehoben.
- **Stornierungen:** ganze Warenkörbe oder einzelne Artikel können in der
  Historie mit einer dreisekündigen Sicherheitsbestätigung storniert werden.
  Sie bleiben nachvollziehbar, werden aber aus Bestand, Bilanzen und offenen
  Vorgängen herausgerechnet.
- **Export & Sicherung:** Download als CSV/ZIP sowie automatische Sicherung
  nach jeder erfolgreichen Änderung, einschließlich Versand- und
  Zahlungsstatus.
- **Legacy-Import:** ein Skript importiert die vorhandene ODS als echte
  Buchungen, nicht als fragile Tabellenformeln.

## Wichtige Datenmodell-Entscheidungen

### Artikel und Varianten

Ein **Artikel** ist beispielsweise `Geometry Shirt`.  Seine **Optionen** sind
frei definierbar: `Farbe = weiß, schwarz` und `Größe = S, M, L`.  Daraus werden
Varianten erzeugt, etwa `Geometry Shirt — Farbe: schwarz · Größe: M`.

Jede Variante kann einen abweichenden Verkaufspreis, Standard-Einkaufspreis
und Mindestbestand haben. Das ist wichtig, weil beispielsweise Pullover oder
Sondergrößen einen anderen Preis haben können. Ein Mindestbestand kann zuerst
mit einem Klick auf alle Varianten eines Artikels übertragen und danach für
einzelne Varianten überschrieben werden. Ein leeres Feld deaktiviert die
Warnung; `0` bedeutet, dass erst bei ausverkauftem Bestand gewarnt wird.

Ein Artikel oder eine einzelne Variante kann außerdem als **nicht mehr
angeboten** markiert werden. Das ist keine Löschung: Bestehende Buchungen,
Bestände und mögliche spätere Einkäufe bleiben erhalten. Die Verkaufsauswahl
und die Verkaufs-API schließen nicht angebotene Einträge jedoch aus.

### Sammelbelege und Warenkorb

Ein **Sammelbeleg** ist ein Kauf mit mehreren Positionen. Technisch bleibt jede
Position eine eigene Verkaufszeile, aber alle teilen dieselbe `receipt_id`.
Dadurch wird der Warenkorb in der Historie als ein Kauf dargestellt, während
Bestand, Zahlungs- und Versandstatus weiterhin pro Artikel funktionieren. Der
eingegebene Gesamtbetrag und eine mögliche Spende werden beim Speichern
centgenau auf die Positionen verteilt; so lässt sich auch nur eine Position
stornieren, ohne die Bilanz des restlichen Warenkorbs zu verfälschen.

### Warum gelöschte Optionen nicht wirklich gelöscht werden

Historische Verkäufe zeigen immer die aktuell gepflegten Namen ihrer Optionen.
Wenn `schwarz` in `Black` umbenannt wird, steht der neue Name auch in alten
Belegen.  Wird ein Wert gelöscht, wird er deshalb nur **deaktiviert**.  Er ist
für neue Verkäufe nicht mehr auswählbar, bleibt aber für alte Belege lesbar.

Das erfüllt zwei Ziele zugleich:

1. Die verlangte rückwirkende Umbenennung funktioniert zuverlässig.
2. Keine alte Buchung verliert ihren Bezug zu einer inzwischen eingestellten
   Größe, Farbe oder Variante.

### Versandstatus und Erhaltungsstatus

Ein Verkauf, bei dem der Artikel direkt am Stand übergeben wurde, hat keinen
Versandstatus. Wird beim Verkauf hingegen **Artikel erhalten** oder **Bezahlt**
abgewählt, erfordert die App Kontaktdaten. Ein nicht direkt übergebener Artikel
startet zusätzlich einen Versandvorgang mit dem Status **Noch nicht versendet**.
In **Offene Vorgänge** kann er anschließend auf
**Versendet** und schließlich **Erhalten** gesetzt werden. Erst dann wandert
er in die Liste **Gesendete Waren**. Die ursprüngliche Verkaufstransaktion wird
dabei nicht verändert oder dupliziert.

Nicht bezahlte Verkäufe werden analog nach dem Umschalten auf **Bezahlt** in
**Bezahlte Verkäufe** verschoben. Direkt als bezahlt erfasste Verkäufe erscheinen
nicht in dieser speziellen Nachbearbeitungshistorie.

### Saldo statt unklarer „Gewinn"

`Saldo` bedeutet in dieser App: **eingenommene Zahlungen plus Spenden minus
erfasste Wareneingänge**.  Offene Zahlungen werden separat angezeigt.  Das ist
genauer als ein als „Gewinn“ bezeichneter Wert, der bei einer Nachbestellung
kurzzeitig stark schwanken würde.  Eine spätere Erweiterung kann zusätzlich
einen FIFO- oder Durchschnittskosten-Gewinn pro verkauftem Artikel berechnen.

## Installation auf der Synology DS225+

Die DS225+ mit Container Manager ist für diese kleine App mehr als ausreichend.
Die folgenden Schritte sind bewusst ohne SSH-Zwang beschrieben.

1. Entpacke dieses Projekt auf dem NAS, zum Beispiel nach
   `docker/protovibe-merch`.
2. Kopiere `.env.example` nach `.env` und setze dort:

   ```dotenv
   SECRET_KEY=<lange-zufällige-Zeichenfolge>
   ADMIN_USERNAME=<dein-admin-name>
   ADMIN_PASSWORD=<langes-eigenes-passwort>
   HOST_PORT=8088
   BACKUP_RETENTION_DAYS=90
   ```

   Eine sichere Zeichenfolge für `SECRET_KEY` erzeugst du beispielsweise mit
   `python3 -c "import secrets; print(secrets.token_urlsafe(48))"`.

3. Lege neben `docker-compose.yml` die Ordner `data` und `imports` an.  Der
   Container kann die Ordner auch selbst anlegen; manuell ist es nur besser
   sichtbar.
4. Öffne DSM → **Container Manager** → **Projekt** → **Erstellen**.
5. Wähle als Projektpfad den entpackten Projektordner und als Quelle die dort
   liegende `docker-compose.yml`.  Starte anschließend den Build.
6. Öffne im Heimnetz `http://<IP-der-Synology>:8088` und melde dich mit den
   Werten aus `.env` an.

Beim ersten Start erzeugt die App automatisch die SQLite-Datenbank und den
Administrator.  Alle dauerhaften Daten liegen ausschließlich in `data/`.

### Sichere Erreichbarkeit bei Konzerten

Die App sollte nicht per Router-Portfreigabe ins öffentliche Internet gestellt
werden.  Im Heimnetz genügt die lokale IP.  Für unterwegs empfiehlt sich ein
VPN-Zugang zur Synology; dann bleibt die App genauso privat wie im Heimnetz.

Dieser erste Stand erwartet eine erreichbare Synology.  Eine Offline-PWA mit
späterer Synchronisation ist sinnvoll, wenn mehrere Geräte am Gig unabhängig
vom Mobilfunk verkaufen sollen, aber bewusst nicht Teil dieses stabilen Kerns.

## Automatische Backups und Wiederherstellung

Nach jedem erfolgreichen Verkauf, Einkauf oder Artikel-Update legt die App in
`data/backups/<Zeitstempel>/` an:

- `merch.sqlite3` – vollständige, wiederherstellbare Datenbankkopie;
- `artikel.csv`, `verkaeufe.csv`, `einkaeufe.csv`, `bestand.csv` – lesbare
  Tabellenexporte.

Alte Sicherungsordner werden nach der in `.env` gesetzten Anzahl von Tagen
gelöscht.  Ergänzend ist ein Synology-Snapshot oder Hyper Backup des gesamten
Projektordners empfehlenswert.

Für eine Wiederherstellung Projekt zuerst stoppen, `data/merch.sqlite3` durch
die gewünschte Snapshot-Datei ersetzen und dann erneut starten.  Die normalen
CSV-Dateien sind zum Nachsehen/Weitergeben gedacht; die SQLite-Datei ist die
vollständige Wiederherstellung.

## Import der bisherigen ODS

> Wichtig: Der Import ist nur für eine noch leere Artikel-, Verkaufs- und
> Einkaufsdatenbank vorgesehen. Vorher daher zuerst den Teststart machen und
> dann importieren, bevor neue Buchungen angelegt werden.

1. Kopiere die ODS in den Ordner `imports`, etwa als
   `imports/merch-bisher.ods`.
2. Öffne im Container Manager die Konsole des laufenden Containers oder nutze
   SSH auf der Synology.
3. Führe aus:

   ```bash
   docker exec -it protovibe-merch python scripts/import_ods.py /import/merch-bisher.ods
   ```

4. Lade die App neu und kontrolliere zuerst Artikelbilanz, einzelne Einkäufe
   und ein paar alte Verkäufe.

Das Importskript verwendet die Eingangsdaten `Stück × Preis/Stück`.  Es kopiert
also nicht versehentlich eine fehlerhafte Berechnung aus einer abgeleiteten
ODS-Spalte.  Das ist insbesondere relevant für die in der bisherigen Datei
entdeckte, um eine Zeile verschobene Einkaufsformel.

## Für Entwickler: Orientierung im Quellcode

| Datei/Ordner | Aufgabe |
|---|---|
| `app.py` | Datenbankschema, Geschäftsregeln, Routen, CSV-Export und Backup. Alle zentralen Funktionen haben Docstrings. |
| `templates/` | Deutsche servergerenderte Oberflächen, ein Template pro Reiter. |
| `static/transaction.js` | Generische Artikelauswahl – kennt keine fest verdrahteten Optionen wie Farbe/Größe. |
| `static/sales.js` | Verkaufsspezifische Logik, Warenkorb, Belegvorschau und Spendenberechnung. |
| `static/purchases.js` | Einkaufsspezifische Logik und Übernahme des letzten Einkaufspreises. |
| `static/operations.js` | Speichert die Statusänderungen für offene Sendungen und Zahlungen. |
| `static/articles.js` | Dynamische Optionsspalten, Live-Vorschau der Varianten sowie Mindestbestands- und Angebotssteuerung. |
| `scripts/import_ods.py` | Einmaliger ODS-Migrationsimport. |
| `tests/test_app.py` | Regressionstests für Bestand, Statusvorgänge, Artikeldefaults, Pflichtkontaktdaten und rückwirkende Optionsnamen. |

Die Anwendung speichert Geldbeträge immer als ganzzahlige Cent-Werte und
Bestände als Bewegungen.  Deshalb gibt es weder Gleitkomma-Rundungsfehler noch
eine vom Bestand getrennte, manuell zu pflegende Bestandsspalte.

## Lokale Entwicklung und Test

Wenn Docker lokal installiert ist:

```bash
cp .env.example .env
# .env mit einem echten SECRET_KEY und ADMIN_PASSWORD ausfüllen
docker compose up --build
```

Die Regressionstests lassen sich im gebauten Container ausführen:

```bash
docker compose exec merch python -m unittest discover -s tests -v
```

## GitHub-Releases und kontrollierte Container-Updates

Ab Version 0.3.0 ist der Quellcode von der laufenden Datenhaltung getrennt.
Der GitHub-Release-Tag ist dabei die einzige Versionsquelle für veröffentlichten
Code:

- GitHub Actions testet jeden Push nach `main`.
- Ein Git-Tag wie `v0.3.1` erzeugt nach erfolgreichen Tests ein
  DS225+-kompatibles Image `ghcr.io/tawilts/protovibe-merch:v0.3.1` und bettet
  exakt diesen Tag beim Build als `APP_VERSION` in das Image ein.
- Die App prüft nach einem Admin-Login asynchron und zwischengespeichert die
  neueste veröffentlichte GitHub-Version. Unter **Updates** kann die Prüfung
  jederzeit bewusst wiederholt werden.

Die Web-App führt ausdrücklich kein `git pull`, keinen Docker-Befehl und keine
automatische Datenbankwiederherstellung aus. Ein kompromittierter Browser-Login
kann dadurch kein beliebigen Code auf dem NAS starten. Ein Update bleibt eine
bewusste Administratoraktion.

### Release-Ablauf

1. Code, Tests und Dokumentation committen und nach `main` pushen.
2. Den grünen Workflow **Test application** abwarten.
3. Auf GitHub ein Release mit einem neuen Tag erstellen, zum Beispiel `v0.3.1`.
   Der Workflow **Publish release image** testet erneut und veröffentlicht erst
   dann das Container-Image. Der Tag wird zugleich die in der App angezeigte
   Versionsnummer.
4. Erst wenn dieser Workflow grün ist, die Synology auf genau dieses Image
   aktualisieren.

Es gibt keine `VERSION`-Datei mehr und deshalb auch keinen Abgleich zweier
Versionsnummern. Für lokale Entwicklungs-Builds ohne Release-Tag zeigt die App
neutral `v0.0.0`; ein veröffentlichtes Container-Image zeigt immer seinen
GitHub-Tag.

### Einmaliger Wechsel auf das Release-Image

Die vorhandene `docker-compose.yml` bleibt für Entwicklung und lokale Builds.
Nach dem ersten veröffentlichten Image steht für die Synology die separate
`docker-compose.synology.yml` bereit. Sie lädt ein fertiges Image, baut also
nicht mehr auf dem NAS.

1. Warten, bis das GitHub-Release `v0.3.0` den erfolgreichen Workflow
   **Publish release image** zeigt.
2. Unter GitHub **Packages** das Paket `protovibe-merch` öffnen. Ist es
   öffentlich, kann die Synology es ohne Registry-Zugangsdaten laden. Bei einem
   privaten Paket muss die Synology stattdessen einmal mit einem auf
   `read:packages` beschränkten GitHub-Token bei `ghcr.io` angemeldet werden.
3. Im Projektordner auf der Synology die bestehende `.env` unverändert lassen
   und ergänzen:

   ```dotenv
   MERCH_IMAGE_TAG=v0.3.0
   ```

4. Das laufende Projekt stoppen, dann die Produktion-Datei
   `docker-compose.synology.yml` als Compose-Datei des Projekts verwenden.
   Falls du per SSH arbeitest, lauten die entsprechenden Befehle:

   ```bash
   docker compose -f docker-compose.synology.yml pull
   docker compose -f docker-compose.synology.yml up -d
   ```

   Ohne SSH kannst du in Container Manager ein Projekt aus dieser Datei im
   gleichen Ordner neu anlegen. Entscheidend ist, dass der Ordner `data/`
   unverändert am selben Ort bleibt; darin liegen Datenbank und Backups.

Für eine spätere Version änderst du lediglich `MERCH_IMAGE_TAG`, zum Beispiel
auf `v0.3.1`, und führst erneut `pull` und `up -d` aus. Das ist keine zweite
Versionspflege: Es wählt nur bewusst aus, welches bereits veröffentlichte Image
auf der Synology laufen soll. Die App-Version selbst ist bereits im gewählten
Image hinterlegt. Ein Rücksprung auf die vorige Code-Version ist genauso
möglich, indem du wieder den vorherigen Tag einträgst. Vor jeder Aktualisierung
erzeugt die App bereits reguläre SQLite-/CSV-Sicherungen nach jeder Buchung;
zusätzlich ist ein Synology Snapshot oder Hyper Backup des Projektordners
sinnvoll.

### Private Repositories und die Update-Prüfung

Für ein öffentliches Repository funktioniert die Versionsprüfung ohne weitere
Konfiguration. Bei einem privaten Repository kann in `.env` ein separat
erzeugter, feingranularer GitHub-Token mit ausschließlich lesendem Zugriff auf
dieses Repository hinterlegt werden:

```dotenv
UPDATE_CHECK_TOKEN=<nur-lesender-token>
```

Dieser Token ist nur für die Versionsabfrage vorgesehen und nicht identisch mit
einem möglichen `read:packages`-Token für das Laden privater Container-Images.
Beide gehören ausschließlich in `.env`, niemals ins Git-Repository.

## Nächste sinnvolle Erweiterungen

- Benutzerverwaltung und persönliche Konten für Bandmitglieder.
- Separater Dialog für Fehldrucke, Geschenke und Inventurkorrekturen.
- Offline-fähige PWA mit Konfliktauflösung beim späteren Synchronisieren.
- Zeitbasierte Umsatzgrafiken und Mindestbestandswarnungen.
