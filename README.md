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
  kann aber pro Einkauf geändert werden. Die vollständige Einkaufshistorie
  bleibt sichtbar; Einträge lassen sich nach einer dreisekündigen
  Sicherheitsbestätigung bearbeiten oder löschen. Rechnungen können direkt als
  PDF, PNG oder JPG (bis 10 MB) per Drag-and-drop angehängt werden.
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
  und vor einem direkten Verkauf hervorgehoben. Zusätzlich zeigen Ranglisten
  die meistverkauften und umsatzstärksten Artikel, Veranstaltungen und
  Verkäufer; ein lokales Diagramm zeichnet den Einnahmenverlauf pro Datum.
- **Schnelles Finden & Mobilansicht:** Historie, Einkäufe, offene Vorgänge,
  Bilanzen und Artikelliste lassen sich direkt nach Begriffen wie Artikel,
  Veranstaltung oder Verkäufer filtern. Auf Smartphones sind Pinch-Zoom und
  horizontales Wischen in breiten Tabellen ausdrücklich aktiviert, sodass auch
  eine vollständige Tabellenzeile kontrolliert werden kann.
- **Offline-Verkauf:** Die Verkaufsansicht lässt sich als PWA auf einem zuvor
  online vorbereiteten Gerät öffnen. Ohne Empfang werden Verkaufswarenkörbe
  mit einer zufälligen Ereignis-ID lokal vorgemerkt und später mit genau dieser
  ID übertragen. Der Server führt eine dauerhafte Synchronisationsliste und
  legt denselben Verkauf bei Wiederholungen höchstens einmal an.
- **Stornierungen:** ganze Warenkörbe oder einzelne Artikel können in der
  Historie mit einer dreisekündigen Sicherheitsbestätigung storniert werden.
  Sie bleiben nachvollziehbar, werden aber aus Bestand, Bilanzen und offenen
  Vorgängen herausgerechnet.
- **Export & Sicherung:** Download als CSV/ZIP sowie automatische Sicherung
  nach jeder erfolgreichen Änderung, einschließlich Versand- und
  Zahlungsstatus. Hochgeladene Rechnungen gehören zum jeweiligen
  Sicherungspunkt dazu.
- **Konten, Rollen & Schutz:** Der einzelne Admin kann Seller und Manager mit
  zeitlich begrenztem Einrichtungscode anlegen und zurücksetzen. Seller können
  verkaufen und Einkaufsdaten lesen, Manager verwalten zusätzlich Artikel und
  Einkaufswarenkörbe, nur der Admin verwaltet Konten oder setzt Betriebsdaten
  zurück. Konten, Passwörter und 2FA liegen unabhängig von Artikeln und
  Buchungen in einer eigenen SQLite-Datei.
  Jede Person kann ihren eigenen Benutzernamen nach einer frischen
  Sicherheitsbestätigung ändern. Der Admin benötigt eine kostenlose, lokale TOTP-2FA; die anderen Rollen
  können sie freiwillig aktivieren. Profilzugriff, Passwortwechsel und der
  Datenreset verlangen eine erneute Passwortbestätigung.
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

Beim ersten Start erzeugt die App automatisch die beiden SQLite-Dateien und
den Administrator. Alle dauerhaften Daten liegen ausschließlich in `data/`:

- `merch.sqlite3` enthält ausschließlich Artikel, Varianten, Verkäufe,
  Einkäufe, Rechnungsbezüge und die betriebliche Historie;
- `users.sqlite3` enthält Benutzerkonten, Rollen, Passwörter und 2FA.

Bei einem Update von einer älteren Ein-Datei-Installation erkennt die App die
alte `merch.sqlite3` automatisch. Vor der Aufteilung wird ein unverändertes
ZIP unter `data/migration-archives/` angelegt, anschließend werden die
Benutzer mit ihren IDs und MFA-Daten nach `users.sqlite3` kopiert. Bereits
gebuchte Verkäufe und Einkäufe behalten zusätzlich den damaligen
Benutzernamen als Historien-Schnappschuss, sodass das Löschen eines Kontos
keine Buchung unlesbar macht.

### Benutzerkonten, Rollen und 2FA

Es ist kein externer Login-Dienst, kein E-Mail-Versand und kein kostenpflichtiger
2FA-Anbieter nötig. Nach dem Update meldest du dich einmal als bisheriger
Admin an und richtest die verpflichtende Zwei-Faktor-Authentifizierung per
QR-Code in einer Authenticator-App ein. Anschließend speicherst du die zehn
einmalig nutzbaren Wiederherstellungscodes an einem sicheren, vom Handy
getrennten Ort. Bestehende Sitzungen werden beim Update einmal abgemeldet,
damit ein bereits geöffneter Admin-Browser die 2FA-Einrichtung nicht umgehen
kann.

Im Reiter **Verwaltung** kannst du danach Seller und Manager anlegen. Sie
melden sich zuerst mit dem ausgegebenen Einrichtungscode an und setzen sofort
ihr eigenes Passwort. Der angemeldete Benutzername steht standardmäßig im Feld
**Verkauft von**, kann dort aber weiterhin überschrieben werden.

Die Standardwerte in `.env` müssen nicht ergänzt werden. Optional kannst du
sie anpassen:

```dotenv
ACCOUNT_SETUP_CODE_DAYS=14      # Gültigkeit neuer/erneuerter Einrichtungscodes
PROFILE_REAUTH_SECONDS=600      # Dauer einer Profil-Sicherheitsbestätigung
MFA_ISSUER=Protovibe Merch Manager
```

`SECRET_KEY` muss dauerhaft unverändert bleiben. Er schützt bereits die
Sitzungen und verschlüsselt nun auch die lokal gespeicherten TOTP-Geheimnisse;
ein Wechsel würde eingerichtete 2FA-Geräte ungültig machen. Die Uhr der
Synology sollte über die DSM-Zeitsynchronisation korrekt laufen, weil
Authenticator-Codes zeitbasiert sind.

Der Datenreset im Admin-Reiter fordert das aktuelle Passwort, einen 2FA- oder
Wiederherstellungscode und die exakte Bestätigungsphrase. Vorher schreibt die
App ein ZIP unter `data/reset-archives/`. Danach werden nur Artikel,
Buchungen und Rechnungen frisch angelegt; sämtliche Benutzerkonten, Rollen,
Passwörter und 2FA-Einstellungen bleiben erhalten.

### Sichere Erreichbarkeit bei Konzerten

Die App sollte nicht per Router-Portfreigabe ins öffentliche Internet gestellt
werden.  Im Heimnetz genügt die lokale IP.  Für unterwegs empfiehlt sich ein
VPN-Zugang zur Synology; dann bleibt die App genauso privat wie im Heimnetz.

### Offline-Verkauf ohne mitgenommenen Server

Für den Offline-Modus muss kein Server zum Konzert mit. Die Synology bleibt
zu Hause; ein vorbereitetes Handy oder Tablet speichert nur neue
**Verkaufsereignisse** lokal und überträgt sie später an die Synology.

Service Worker – und damit der installierbare Offline-Modus – funktionieren
nur in einem sicheren Kontext: **HTTPS** (oder `localhost` bei lokaler
Entwicklung). Richte für die Synology deshalb einen DSM-Reverse-Proxy mit
gültigem Zertifikat und einer festen HTTPS-Adresse ein. Der normale Online-
Betrieb über `http://<IP>:8088` bleibt möglich, kann aber nicht als
Offline-PWA installiert werden.

Vor einem Gig:

1. Mit dem vorgesehenen Seller-/Manager-Konto online anmelden.
2. Die Seite **Verkauf** einmal vollständig öffnen und optional über den
   Browser zum Startbildschirm hinzufügen.
3. Den Status „Online und synchron“ abwarten und das Gerät mit einer
   Bildschirmsperre schützen.

Am Gig zeigt die Verkaufsansicht deutlich „Offline-Modus aktiv“. Jeder
bestätigte Verkauf landet dann in der lokalen Warteschlange. Nach Rückkehr ins
Netz genügt derselbe Account und der Button **Jetzt synchronisieren** (die
Synchronisierung startet zusätzlich automatisch). Jede Buchung trägt eine
zufällige UUID; der Server speichert diese Ereignis-ID samt
Payload-Fingerabdruck dauerhaft und antwortet bei Wiederholungen mit dem
bereits erstellten Beleg statt eine Doppelbuchung anzulegen.

Offline unterstützt bewusst nur neue **Verkäufe**. Einkäufe, Artikel- und
Benutzerverwaltung, Stornierungen sowie Statusänderungen benötigen weiterhin
eine Verbindung. Wenn sich auf dem Server Artikelpreise oder Varianten ändern,
während ein Gerät offline ist, bleibt die Buchung in der Warteschlange und
zeigt nach dem Sync eine nachvollziehbare Fehlermeldung statt stillschweigend
anders gebucht zu werden. Ein absichtlicher Admin-Datenreset löscht wie bisher
die gesamte operative Datenbank; vorherige Offline-Warteschlangen dürfen dann
nicht weiter synchronisiert werden.

## Automatische Backups und Wiederherstellung

Nach jedem erfolgreichen Verkauf, Einkauf oder Artikel-Update legt die App in
`data/backups/<Zeitstempel>/` an:

- `merch.sqlite3` – vollständige, wiederherstellbare Kopie der Betriebsdaten;
- `artikel.csv`, `verkaeufe.csv`, `einkaeufe.csv`, `bestand.csv` – lesbare
  Tabellenexporte.
- `invoices/` – die zum Sicherungszeitpunkt vorhandenen hochgeladenen
  Rechnungen. Die App verwendet dafür platzsparende Hardlinks, sofern das
  Dateisystem sie unterstützt.

Rechnungen selbst liegen im laufenden System unter `data/invoices/`. Beim
Ersetzen oder Löschen eines Einkaufs wird der zugehörige Anhang ebenfalls
entfernt; die Änderung wird im Audit-Protokoll festgehalten.

Alte Sicherungsordner werden nach der in `.env` gesetzten Anzahl von Tagen
gelöscht.  Ergänzend ist ein Synology-Snapshot oder Hyper Backup des gesamten
Projektordners empfehlenswert.

Die Sicherungen enthalten bewusst keine Benutzerdatei. Die normalen
CSV-Dateien sind zum Nachsehen/Weitergeben gedacht; die SQLite-Datei ist die
vollständige Wiederherstellung der Betriebsdaten.

## Import der bisherigen ODS

> Wichtig: Der Import ist nur für eine noch leere Artikel-, Verkaufs- und
> Einkaufsdatenbank vorgesehen. Vorher daher zuerst den Teststart machen und
> dann importieren, bevor neue Buchungen angelegt werden.

Für die bereinigte Neuaufstellung verwende die vorbereitete Datei
`protovibe-merch-bereinigt.ods`. Sie enthält eine nachvollziehbare
`Zuordnung` der alten Namen, explizite Varianten-IDs, die dynamischen Optionen
(`Farbe`, `Passform`, `Größe`, `Motiv`) und gruppierte historische
Verkaufsbelege.

1. Kopiere die ODS in den Ordner `imports`, etwa als
   `imports/protovibe-merch-bereinigt.ods`.
2. Öffne im Container Manager die Konsole des laufenden Containers oder nutze
   SSH auf der Synology.
3. Führe aus:

   ```bash
   docker exec -it protovibe-merch python scripts/import_ods.py /import/protovibe-merch-bereinigt.ods
   ```

4. Lade die App neu und kontrolliere zuerst Artikelbilanz, Einkaufswarenkörbe
   und ein paar alte Verkäufe.

Das Skript erkennt weiterhin auch die ursprüngliche ODS mit den Spalten
`Name`, `Art` und `Größe`. Die bereinigte Fassung ist jedoch vorzuziehen: Der
Importer liest dort nur die tatsächlich vorhandenen Varianten ein. Durch
Optionen technisch erzeugte, aber in der ODS nicht vorhandene Kombinationen
werden automatisch als **nicht angeboten** markiert und erscheinen deshalb
nicht im Verkaufsfenster.

Das Importskript verwendet für Buchungen immer die Eingangsdaten
`Stück × Preis/Stück`. Es kopiert also nicht versehentlich eine fehlerhafte
Berechnung aus einer abgeleiteten ODS-Spalte.

Einkaufszeilen desselben Kalendertags werden beim Import als ein
Einkaufswarenkorb mit gemeinsamer Beleg-ID angelegt. Preis, Lieferant,
Rechnungsnummer und Kommentar bleiben dabei an der jeweiligen Position.

## Für Entwickler: Orientierung im Quellcode

| Datei/Ordner | Aufgabe |
|---|---|
| `app.py` | Datenbankschema, Geschäftsregeln, Routen, CSV-Export und Backup. Alle zentralen Funktionen haben Docstrings. |
| `templates/` | Deutsche servergerenderte Oberflächen, ein Template pro Reiter. |
| `static/transaction.js` | Generische Artikelauswahl – kennt keine fest verdrahteten Optionen wie Farbe/Größe. |
| `static/sales.js` | Verkaufsspezifische Logik, Warenkorb, Belegvorschau und Spendenberechnung. |
| `static/offline-sales.js` | IndexedDB-Warteschlange für Offline-Verkäufe sowie sichere Nachsynchronisierung. |
| `static/service-worker.js` | Beschränktes PWA-Caching: statische Dateien und die letzte Verkaufsansicht, keine Admin-/Profildaten. |
| `static/purchases.js` | Einkaufswarenkorb, positions- und warenkorbbezogene Rechnungsanhänge sowie abgesicherte Korrektur/Löschung. |
| `static/operations.js` | Speichert die Statusänderungen für offene Sendungen und Zahlungen. |
| `static/articles.js` | Dynamische Optionsspalten, Live-Vorschau der Varianten sowie Mindestbestands- und Angebotssteuerung. |
| `scripts/import_ods.py` | Einmaliger ODS-Migrationsimport. |
| `tests/test_app.py` | Regressionstests für Bestand, Rollen, 2FA, Profil-Reauthentifizierung, Datenreset, Statusvorgänge und Artikeldefaults. |

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

- Separater Dialog für Fehldrucke, Geschenke und Inventurkorrekturen.
- Offline-fähige PWA mit Konfliktauflösung beim späteren Synchronisieren.
- Zeitbasierte Umsatzgrafiken und Mindestbestandswarnungen.
