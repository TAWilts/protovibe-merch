# Merch Manager

Eine bewusst schlanke, selbst gehostete Merch-Verwaltung für eine Band.  Die
Anwendung läuft als einzelnes Python-/Flask-Containerprojekt auf der Synology
und bündelt Bestand, Buchungen und Auswertungen zentral.

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
- **Variantenfotos:** Manager können pro Variante mehrere JPG-, PNG- oder
  WebP-Bilder hinzufügen oder einzeln löschen. Uploads werden auf maximal
  1600 Pixel verkleinert, als JPEG gespeichert und liegen nicht in SQLite,
  sondern im geschützten lokalen Bildordner. Jede Person kann im Profil
  entscheiden, ob diese Bilder im Verkauf unter den Variantenoptionen sichtbar
  sind. Fehlt für eine Auswahl ein Foto, zeigt die App das ähnlichste Foto
  einer anderen Variante desselben Artikels.
- **Diashow:** Direkt nach der Artikelverwaltung finden Manager eine
  gemeinsame Galerie aller Bilder. Neue Uploads werden dort einer Variante
  zugeordnet oder als **Anderes** ohne Artikelbezug gespeichert und sind damit
  für alle Konten dauerhaft verfügbar. Jedes Bild ist standardmäßig für die
  Werbe-Diashow ausgewählt und kann einzeln ausgeschlossen werden. Die
  Bildwechselrate und die Animationsgeschwindigkeit lassen sich vor dem Start
  einstellen. **Produktpalette zeigen** startet eine Vollbildfolge in
  zufälliger Reihenfolge ohne Wiederholung pro Durchlauf; bei Produktfotos
  bewegen sich Artikelname, Variante und aktueller Preis versetzt zum Bild
  hinein. Ein beliebiger Klick oder Tastendruck beendet die Anzeige.
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
  Umsatz, Spenden, Saldo und Angebotsstatus. Varianten werden getrennt nach
  „nachbestellen“ und „obsolet“ angezeigt; beide Tabellen lassen sich
  alphanumerisch sortieren, filtern und bei Bedarf nach Artikel gruppieren.
  Die sichtbare Sortierung, Filterung und Gruppierung wird in den Bestands- und
  Artikel-CSV-Export übernommen. Pro Variante lassen sich optionale
  Mindestbestände hinterlegen; unterschrittene Grenzwerte werden in der Bilanz
  und vor einem direkten Verkauf hervorgehoben. Zusätzlich zeigen Ranglisten
  die meistverkauften und umsatzstärksten Artikel, Veranstaltungen und
  Verkäufer; die drei Geld-Ranglisten lassen sich zwischen Einnahmen und
  Gewinn umschalten. Ein lokales Diagramm zeichnet den Einnahmenverlauf pro
  Datum.
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
- **Export & Sicherung:** Download als CSV/ZIP auf ausdrückliche Anforderung
  sowie automatische, verschlüsselte Sicherung nach jeder erfolgreichen
  Änderung, einschließlich Versand- und Zahlungsstatus. Hochgeladene
  Rechnungen und Produktfotos gehören zum jeweiligen Sicherungspunkt dazu.
- **Konten, Rollen & Schutz:** Band-Admins können Seller, Member, Manager und
  weitere Band-Admins mit zeitlich begrenztem Einrichtungscode anlegen und
  zurücksetzen. Seller können
  ausschließlich verkaufen sowie die Diashow ansehen und abspielen. Member
  behalten den bisherigen Seller-Zugriff auf Historie, Vorgänge, Einkäufe,
  Bandfinanzen und Bilanzen. Manager verwalten zusätzlich Artikel und
  Einkaufswarenkörbe; Band-Admins verwalten die Konten ihrer Band und dürfen
  deren Betriebsdaten zurücksetzen. Konten, Passwörter und 2FA liegen
  unabhängig von Artikeln und Buchungen in einer eigenen, verschlüsselten
  SQLite-Datei. Konten lassen sich nach einer erneuten Sicherheitsbestätigung
  löschen, ohne ihre historischen Buchungen zu entfernen.
  Jede Person kann ihren eigenen Benutzernamen, Sprache, Farbthema und die
  Anzeige von Variantenfotos nach einer frischen Sicherheitsbestätigung ändern.
  Für Band-Admins ist die kostenlose, lokale TOTP-2FA optional; System- und
  Support-Admins müssen sie verwenden. Profilzugriff, Passwortwechsel und
  sensible Band-Admin-Aktionen verlangen das aktuelle Passwort und, sofern für
  das Konto eingerichtet, zusätzlich einen 2FA- oder Wiederherstellungscode.
## Wichtige Datenmodell-Entscheidungen

### Artikel und Varianten

Ein **Artikel** ist beispielsweise `Geometry Shirt`.  Seine **Optionen** sind
frei definierbar: `Farbe = weiß, schwarz` und `Größe = S, M, L`.  Daraus werden
Varianten erzeugt, etwa `Geometry Shirt — Farbe: schwarz · Größe: M`.

Jede Variante kann einen abweichenden Verkaufspreis, Standard-Einkaufspreis
und Mindestbestand haben. Das ist wichtig, weil beispielsweise Pullover oder
Sondergrößen einen anderen Preis haben können. Sie kann außerdem als
**nicht nachbestellen** markiert werden; dann erscheint sie getrennt als
obsolet, bleibt aber weiterhin vollständig in Bestand und Historie sichtbar.
Ein Mindestbestand kann zuerst
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
6. Öffne im Heimnetz `http://<IP-der-Synology>:8088`. Beim allerersten Start
   erscheint die Einrichtung der Datenbankverschlüsselung. Bestätige dort
   einmalig das `ADMIN_PASSWORD` aus der `.env`, wähle eine **separate
   Datenbank-Passphrase** und speichere den angezeigten
   Wiederherstellungsschlüssel offline.
7. Melde dich danach mit `ADMIN_USERNAME` und `ADMIN_PASSWORD` als erster
   Band-Admin an. Die 2FA kannst du im Profil aktivieren. Den ersten getrennten
   System-Admin legst du anschließend einmalig unter **Verwaltung** an; dieses
   Plattformkonto muss beim ersten Login 2FA einrichten.

Alle dauerhaften Daten liegen ausschließlich in `data/`:

- `merch.sqlite3` enthält ausschließlich Artikel, Varianten, Verkäufe,
  Einkäufe, Rechnungsbezüge und die betriebliche Historie – vollständig mit
  SQLCipher verschlüsselt;
- `users.sqlite3` enthält Benutzerkonten, Rollen, Passwort-Hashes und 2FA –
  ebenfalls vollständig verschlüsselt;
- `encryption.json` enthält nur die verschlüsselten Umschläge des zufällig
  erzeugten Datenbankschlüssels, niemals die Passphrase oder den Klartext-Key.

Rechnungen liegen als verschlüsselte Dateien unter `data/invoices/`.
Produktfotos liegen separat unter `data/variant-photos/`; in einer normalen
verschlüsselten Installation werden auch diese Dateien verschlüsselt, im
bewusst unverschlüsselten `LOCAL_DEV_MODE` als optimierte JPEGs abgelegt.
Bereits gebuchte Verkäufe und Einkäufe behalten zusätzlich den damaligen
Benutzernamen als Historien-Schnappschuss, sodass das Löschen eines Kontos
keine Buchung unlesbar macht.

### Benutzerkonten, Rollen und 2FA

Es ist kein externer Login-Dienst und kein kostenpflichtiger 2FA-Anbieter nötig.
Der optionale E-Mail-Versand für Support-Nachrichten kann über ein bestehendes
Mailkonto erfolgen. Beim Rollen-Upgrade wird das bisherige, aktive
`admin`-Konto (bevorzugt das zu `ADMIN_USERNAME` passende Konto, andernfalls
das einzige vorhandene Admin-Konto) zum `system_admin`. Gibt es genau einen
aktiven `manager`, wird dieser in derselben Migration zum `band_admin`. Beide
alten Sitzungen werden dabei ungültig. Gibt es keinen oder mehrere aktive
Manager, bleibt das bisherige Admin-Konto vorübergehend Band-Admin, damit das
Live-System bedienbar bleibt. In **Verwaltung** muss dann einmalig ein Manager
ausgewählt oder ein neues Band-Admin-Konto angelegt werden; erst mit diesem
atomaren Übergang wird das bisherige Konto System-Admin und abgemeldet. Ein
persistenter Migrationsstatus verhindert doppelte oder zufällige Zuweisungen
bei einem Neustart. Weitere alte Admin-Konten bleiben aus Gründen der kleinsten
Rechte Band-Admins. Die Rollen einer Band sind `seller`, `member`, `manager`
und `band_admin`.

Im Reiter **Verwaltung** kannst du diese Bandkonten anlegen. Sie
melden sich zuerst mit dem ausgegebenen Einrichtungscode an und setzen sofort
ihr eigenes Passwort. Der angemeldete Benutzername steht standardmäßig im Feld
**Verkauft von**, kann dort aber weiterhin überschrieben werden.

Bei einer frischen Installation – oder wenn mehrere alte Admin-Konten keinen
eindeutigen Plattforminhaber erkennen lassen – kann der Band-Admin über einen
einmaligen Bereich in **Verwaltung** das erste separate Plattformkonto anlegen.
Danach sind `system_admin` und `support_admin` ausschließlich der
**System-Verwaltung** zugeordnet. Beide Rollen müssen eine TOTP-2FA einrichten
und sehen dort das Supportpostfach sowie die systemweite Benutzerübersicht.
Ohne eine serverseitig gültige Tenant-Freigabe erhalten sie keinerlei Zugriff
auf Banddaten. Eine echte Bandliste, das Deaktivieren ganzer Bands und der
zeitlich begrenzte Zugriffsworkflow folgen erst zusammen mit der zentralen
Tenant-Struktur; die aktuelle Single-Band-Version täuscht diese Trennung nicht
über UI-Schalter vor.

Die Standardwerte in `.env` müssen nicht ergänzt werden. Optional kannst du
sie anpassen:

```dotenv
ACCOUNT_SETUP_CODE_DAYS=14      # Gültigkeit neuer/erneuerter Einrichtungscodes
PROFILE_REAUTH_SECONDS=600      # Dauer einer Profil-Sicherheitsbestätigung
MFA_ISSUER=Protovibe Merch Manager
```

### Optionale E-Mail-Benachrichtigung

Neue Issues und Fragen bleiben immer im privaten Supportpostfach der
**System-Verwaltung** gespeichert.
Optional kann die App danach zusätzlich eine E-Mail über den SMTP-Server eines
bestehenden Mailkontos senden. IMAP und POP3 dienen nur zum Abrufen von E-Mails
und werden dafür nicht benötigt. Bei vielen Anbietern kostet SMTP mit einem
vorhandenen Konto nichts zusätzlich; häufig ist statt des normalen Passworts
ein separates App-Passwort erforderlich.

Beispiel für implizites TLS auf Port 465:

```dotenv
EMAIL_NOTIFICATIONS_ENABLED=true
SMTP_HOST=smtp.example.org
SMTP_PORT=465
SMTP_SECURITY=ssl
SMTP_USERNAME=merch@example.org
SMTP_PASSWORD=hier-das-app-passwort
SMTP_FROM=merch@example.org
ADMIN_NOTIFICATION_EMAIL=admin@example.org
SMTP_TIMEOUT_SECONDS=8
```

Alternativ wird häufig Port `587` mit `SMTP_SECURITY=starttls` verwendet. Die
genauen Serverdaten liefert der jeweilige Mailanbieter. Nach einem Neustart
zeigt **Verwaltung → E-Mail bei neuen Nachrichten** nur den sicheren
Konfigurationsstatus, niemals Benutzername oder Passwort, und bietet einen
Button zum Senden einer Test-E-Mail. Schlägt SMTP fehl, bleibt die zuvor
gespeicherte Nachricht trotzdem im Supportpostfach erhalten; technische Details
stehen dann ausschließlich im Container-Log.

`SECRET_KEY` muss dauerhaft unverändert bleiben. Er schützt Sitzungen und
verschlüsselt die lokal gespeicherten TOTP-Geheimnisse; er ist **nicht** der
Schlüssel für die SQLite-Dateien. Ein Wechsel würde eingerichtete 2FA-Geräte
ungültig machen. Die Datenbank-Passphrase wird ausschließlich in der
Einrichtungs-/Entsperrseite eingegeben und liegt bewusst nicht in `.env`. Die
Uhr der Synology sollte über die DSM-Zeitsynchronisation korrekt laufen, weil
Authenticator-Codes zeitbasiert sind.

Der Datenreset im Reiter **Verwaltung** ist Band-Admins vorbehalten und fordert
das aktuelle Passwort, bei eingerichteter 2FA zusätzlich einen 2FA- oder
Wiederherstellungscode sowie die exakte Bestätigungsphrase. Vorher schreibt die
App ein ZIP unter `data/reset-archives/`. Danach werden nur Artikel, Buchungen
und Rechnungen frisch angelegt; sämtliche Benutzerkonten, Rollen, Passwörter
und 2FA-Einstellungen bleiben erhalten.

### Datenbankverschlüsselung und Wiederherstellung

Die App erzeugt bei der ersten Einrichtung einen zufälligen 256-Bit-
Datenbankschlüssel. Dieser Schlüssel existiert nur im Arbeitsspeicher des
laufenden Prozesses. In `data/encryption.json` wird er ausschließlich in zwei
verschlüsselten Umschlägen gespeichert:

- einer wird mit der von dir gewählten Datenbank-Passphrase geöffnet;
- der andere mit dem einmalig angezeigten Wiederherstellungsschlüssel.

Nach einem Container-, NAS- oder App-Neustart zeigt die App daher zuerst
**Datenbank entsperren**. Erst danach ist die normale Anmeldung mit Benutzer-
Passwort und 2FA möglich. Weder die Datenbank-Passphrase noch der
Wiederherstellungsschlüssel gehören in `.env`, ein Git-Repository oder einen
Shell-Befehl.

### Optionaler Einmal-Entsperrpass für geplante Image-Updates

Standardmäßig bleibt die vorherige Regel unverändert: Nach einem ungeplanten
Neustart, einem NAS-Neustart oder einem erneuten Start **derselben** Image-
Version bleibt die Datenbank gesperrt. Optional kann ein DSM-Aufgabenplaner
für genau ein bereits geprüftes, anderes Release-Image einen Einmalpass
anfordern. Das ist ausdrücklich kein allgemeines `GEPLANTER_NEUSTART=1`-
Flag und auch keine dauerhaft hinterlegte Datenbank-Passphrase.

Während die alte App noch entsperrt läuft, authentifiziert sich die
root-ausgeführte DSM-Aufgabe mit einem separaten, zufälligen Token. Die App
erstellt daraufhin einen zusätzlichen Schlüsselumschlag, der nur für die
angegebene Zielversion (zum Beispiel `v0.3.1`) und standardmäßig 20 Minuten
gilt (bewusst begrenzt auf 1 bis 60 Minuten).
Die Aufgabe erhält dazu einen frischen zweiten Einmalcode. Erst die Kombination
aus diesem kurzlebigen Umschlag in `data/` und dem Einmalcode öffnet beim Start
des **exakt passenden** neuen Images die Datenbank. Der Umschlag wird atomar
verbraucht und der Einmalcode danach vom NAS gelöscht. Das dauerhafte
Task-Token allein kann weder eine kopierte Datenbank noch ein Backup öffnen.

Bei einer falschen Zielversion, einem abgelaufenen Pass, einem fehlerhaften
Einmalcode oder einer fehlgeschlagenen Migration bleibt die Datenbank gesperrt;
die normale Entsperrseite mit Passphrase oder Wiederherstellungsschlüssel bleibt
der sichere Notfallweg. Beim automatischen Entsperren werden außerdem alle
Browser-Sitzungen abgemeldet, damit ein alter Sitzungs-Cookie nie eine Anmeldung
überspringt.

Die Funktion ist ab Werk ausgeschaltet. Für die erste Aktualisierung auf eine
Version, die diese Funktion enthält, ist deshalb noch eine manuelle
Entsperrung nötig.

1. Lege außerhalb von Projekt-, `data/`- und Backup-Ordnern ein nur für `root`
   zugängliches Verzeichnis an, zum Beispiel
   `/volume1/docker/protovibe-merch-secrets`. Die DSM-Aufgabe muss als `root`
   laufen. Verzeichnisrechte sind `0700`, Dateirechte `0600`:

   ```sh
   umask 077
   mkdir -p /volume1/docker/protovibe-merch-secrets
   openssl rand -base64 48 > /volume1/docker/protovibe-merch-secrets/authorisation-token
   chmod 700 /volume1/docker/protovibe-merch-secrets
   chmod 600 /volume1/docker/protovibe-merch-secrets/authorisation-token
   ```

   Die Datei `authorisation-token` ist ein dauerhaftes **Task**-Geheimnis,
   nicht die Datenbank-Passphrase. Die Datei
   `one-time-unlock-secret` erzeugt die Aufgabe nur kurzfristig und löscht sie
   wieder. Beide Dateien dürfen nicht in `.env`, Git, `data/`, Backups oder
   einen Docker-Command gelangen.

2. Ergänze in der Projekt-`.env` nur den Pfad (keinen Geheimwert):

   ```dotenv
   SCHEDULED_RESTART_SECRETS_DIR=/volume1/docker/protovibe-merch-secrets
   SCHEDULED_RESTART_UNLOCK_TTL_SECONDS=1200
   ```

   Starte das Synology-Projekt danach einmal neu, damit der schreibgeschützte
   Mount unter `/run/protovibe-scheduled-restart` aktiv wird. Die App akzeptiert
   keine Geheimdatei innerhalb des dauerhaften Datenordners.

3. Lege die folgende Aufgabe als **benutzerdefiniertes Skript** im
   Synology-Aufgabenplaner an. Sie erwartet den geprüften Release-Tag als erstes
   Argument, zum Beispiel `v0.3.1`. Setze `PROJECT_NAME` auf den tatsächlichen
   Namen des bestehenden Container-Manager-Projekts; ein abweichender Name
   könnte einen zweiten Compose-Stack erzeugen. Lege die aktiv ausgeführte
   Skriptdatei selbst in einem root-geschützten Verwaltungsordner ab, nicht in
   einem für normale NAS-Nutzer beschreibbaren Checkout. Beispiel: Speichere
   sie als `/volume1/docker/protovibe-merch-admin/scheduled-update.sh` mit
   Recht `0700`; im DSM-Aufgabenplaner lautet der eigentliche Befehl dann
   `/bin/sh /volume1/docker/protovibe-merch-admin/scheduled-update.sh v0.3.1`.
   Für einen anderen Release-Tag wird genau dieses letzte Argument geändert.

   ```sh
   #!/bin/sh
   set -eu
   umask 077

   PROJECT_DIR=/volume1/docker/protovibe-merch
   PROJECT_NAME=protovibe-merch
   SECRETS_DIR=/volume1/docker/protovibe-merch-secrets
   APP_URL=http://127.0.0.1:8088
   TARGET_VERSION="${1:?Bitte einen Release-Tag wie v0.3.1 angeben}"

   if ! printf '%s\n' "$TARGET_VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
     echo "Nur konkrete Release-Tags vX.Y.Z sind erlaubt." >&2
     exit 2
   fi

   AUTH_FILE="$SECRETS_DIR/authorisation-token"
   ONE_TIME_FILE="$SECRETS_DIR/one-time-unlock-secret"
   ONE_TIME_TMP="$ONE_TIME_FILE.tmp.$$"
   CURL_CONFIG="$(mktemp "$SECRETS_DIR/.update-unlock-curl.XXXXXX")"
   ENV_FILE="$PROJECT_DIR/.env"
   ENV_TMP=""

   cleanup() {
     rm -f "$ONE_TIME_FILE" "$ONE_TIME_TMP" "$CURL_CONFIG" ${ENV_TMP:+"$ENV_TMP"}
   }
   trap cleanup EXIT HUP INT TERM

   [ -r "$AUTH_FILE" ] || { echo "Autorisierungsdatei fehlt." >&2; exit 1; }
   [ -f "$ENV_FILE" ] || { echo ".env fehlt." >&2; exit 1; }

   compose() {
     MERCH_IMAGE_TAG="$TARGET_VERSION" docker compose \
       --project-name "$PROJECT_NAME" \
       -f "$PROJECT_DIR/docker-compose.synology.yml" "$@"
   }

   # Das Image erst laden; erst danach beginnt das kurze 20-Minuten-Fenster.
   compose pull merch

   ENV_TMP="$(mktemp "$PROJECT_DIR/.env.update.XXXXXX")"
   awk -v version="$TARGET_VERSION" '
     BEGIN { changed = 0 }
     /^MERCH_IMAGE_TAG=/ { print "MERCH_IMAGE_TAG=" version; changed = 1; next }
     { print }
     END { if (!changed) print "MERCH_IMAGE_TAG=" version }
   ' "$ENV_FILE" > "$ENV_TMP"
   chmod 600 "$ENV_TMP"
   mv "$ENV_TMP" "$ENV_FILE"
   ENV_TMP=""

   # Der Token liegt nur in einer kurzlebigen, root-lesbaren curl-Konfiguration,
   # nicht als Argument in der Prozessliste.
   {
     printf 'header = "Authorization: Bearer %s"\n' "$(tr -d '\r\n' < "$AUTH_FILE")"
     printf 'header = "X-Planned-Restart-Target-Version: %s"\n' "$TARGET_VERSION"
     printf 'url = "%s/system/verschluesselung/geplanter-neustart-pass"\n' "$APP_URL"
   } > "$CURL_CONFIG"
   curl --fail --silent --show-error --request POST --config "$CURL_CONFIG" --output "$ONE_TIME_TMP"
   [ -s "$ONE_TIME_TMP" ] || { echo "Kein Einmalcode empfangen." >&2; exit 1; }
   chmod 600 "$ONE_TIME_TMP"
   mv "$ONE_TIME_TMP" "$ONE_TIME_FILE"
   rm -f "$CURL_CONFIG"
   CURL_CONFIG=""

   compose up -d --no-deps --force-recreate merch

   ready=0
   for attempt in $(seq 1 60); do
     status="$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 5 "$APP_URL/login" || true)"
     if [ "$status" = 200 ]; then ready=1; break; fi
     sleep 2
   done
   [ "$ready" -eq 1 ] || { echo "Neues Image blieb gesperrt oder wurde nicht bereit." >&2; exit 1; }
   ```

   Der Ablauf akzeptiert nur konkrete Tags, nie `latest` oder `stable`. Er lädt
   das Zielimage vor dem Ausstellen des Passes, prüft nach dem Neustart die
   Anmeldeseite und löscht den Einmalcode bei **jedem** Ende der Aufgabe. Wird
   die Aufgabe nach dem Ausstellen abgebrochen, läuft der Pass höchstens bis
   zum Ablauf und ohne die gelöschte Geheimdatei nicht mehr nutzbar. Ein
   privilegierter DSM-/Docker-Administrator kann den Ablauf missbrauchen; das
   liegt in derselben Vertrauensgrenze wie ein bereits entsperrter Container.

Wenn sowohl Datenbank-Passphrase als auch Wiederherstellungsschlüssel verloren
gehen, sind die Daten kryptografisch nicht wiederherstellbar. Das ist keine
absichtliche Schikane, sondern die Konsequenz daraus, dass auf dem NAS kein
automatisch lesbarer Hauptschlüssel liegt. Bewahre beide getrennt und offline
auf.

Solange die Datenbank entsperrt ist, kann der Band-Admin unter **Verwaltung →
Datenbank-Sicherheit** nach erneuter Passwortbestätigung und, sofern
eingerichtet, zusätzlicher 2FA-Bestätigung eine neue Datenbank-Passphrase setzen
oder einen neuen Wiederherstellungsschlüssel erzeugen. Beim Erneuern wird der
vorherige Wiederherstellungsschlüssel sofort ungültig.

Die Verschlüsselung schützt Daten bei einem kopierten Datenträger oder einer
kopierten `data/`-Freigabe. Sie ersetzt keine Zugangssicherung eines bereits
laufenden, entsperrten NAS: Ein Angreifer mit Administratorzugriff auf Server
und laufenden Container kann Daten weiterhin auslesen. HTTPS über den Reverse
Proxy, ein starkes Synology-Admin-Passwort und restriktive Dateirechte bleiben
deshalb wichtig.

### Umstieg von einem bisherigen unverschlüsselten Datenordner

Ein vorhandener `data/`-Ordner wird absichtlich **nicht automatisch**
verschlüsselt oder überschrieben. Findet die neue Version dort alte
`merch.sqlite3`-/`users.sqlite3`-Dateien ohne `encryption.json`, zeigt sie nur
eine Anleitung an.

1. Die alte App beenden und den gesamten bisherigen `data/`-Ordner sicher als
   `data-legacy` ablegen.
2. Einen neuen, leeren `data/`-Ordner anlegen und die neue App starten.
3. Verschlüsselung einrichten, Wiederherstellungsschlüssel sichern und als
   neuer Band-Admin anmelden.
4. Die alten Daten getrennt und geschützt aufbewahren oder löschen, sobald sie
   nicht mehr benötigt werden.

Ungesicherte Datenbanken werden von dieser Version nicht übernommen. Lösche
alte unverschlüsselte Daten einschließlich CSV- und Backup-Ordner erst, wenn
sie nicht mehr benötigt werden und die neue verschlüsselte Sicherung geprüft
ist.

### Sichere Erreichbarkeit bei Konzerten

Die App sollte nicht per Router-Portfreigabe ins öffentliche Internet gestellt
werden.  Im Heimnetz genügt die lokale IP.  Für unterwegs empfiehlt sich ein
VPN-Zugang zur Synology; dann bleibt die App genauso privat wie im Heimnetz.

### Lokaler Entwicklungsmodus ohne HTTPS und SQLCipher

Die Anwendung läuft bereits über normales HTTP. Für eine lokale Testinstanz
kann zusätzlich ein ausdrücklich unsicherer Entwicklungsmodus aktiviert werden.
Er deaktiviert die SQLCipher-Datenbankverschlüsselung sowie die Verschlüsselung
hochgeladener Dateien. Passwort-Hashes, Sitzungs-Signaturen und die übrigen
Zugangskontrollen bleiben aktiv. Die 2FA wird im lokalen Modus weder beim
Login noch bei sensiblen Admin-Aktionen verlangt.

Wichtig: Verwende dafür immer einen separaten, leeren Datenordner. Die normale
`data/`-Freigabe enthält bei einer produktiven Installation verschlüsselte
SQLite-Dateien und darf nicht im Klartextmodus geöffnet werden.

In der lokalen `.env`:

```dotenv
LOCAL_DEV_MODE=true
DATA_VOLUME=./local-data
HOST_PORT=8089
```

Danach:

```powershell
docker compose up --build
```

Die lokale Instanz ist anschließend unter `http://localhost:8089` erreichbar.
Für `docker-compose.synology.yml` bleibt der Modus unabhängig davon
deaktiviert; dort wird weiterhin die verschlüsselte Datenbank verwendet. Der
lokale Modus ist nur für Entwicklung und Tests gedacht und darf nicht ins
Internet oder ungeschützt ins Heimnetz veröffentlicht werden.

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

1. Mit dem vorgesehenen Seller-/Member-/Manager-Konto online anmelden.
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

- `merch.sqlite3` – vollständige, wiederherstellbare und weiterhin
  verschlüsselte Kopie der Betriebsdaten;
- `encryption.json` – die dazugehörigen, weiterhin verschlüsselten
  Schlüsselumschläge (ohne Klartext-Passphrase oder Klartext-Key);
- `invoices/` – die zum Sicherungszeitpunkt vorhandenen hochgeladenen
  Rechnungen in ihrer verschlüsselten Speicherform. Die App verwendet dafür
  platzsparende Hardlinks, sofern das Dateisystem sie unterstützt.
- `variant-photos/` – die optimierten Produktfotos in derselben geschützten
  Speicherform.

Rechnungen selbst liegen im laufenden System verschlüsselt unter
`data/invoices/`; Produktfotos unter `data/variant-photos/`. Beim Ersetzen
oder Löschen eines Einkaufs beziehungsweise eines Produktfotos wird der
zugehörige Anhang entfernt; die Änderung wird im Audit-Protokoll festgehalten.

Alte Sicherungsordner werden nach der in `.env` gesetzten Anzahl von Tagen
gelöscht.  Ergänzend ist ein Synology-Snapshot oder Hyper Backup des gesamten
Projektordners empfehlenswert.

Die Sicherungen enthalten bewusst keine Benutzerdatei. CSV-/ZIP-Exporte sind
weiterhin möglich, werden aber nur auf ausdrücklichen Download im Browser
unverschlüsselt erzeugt; behandle sie anschließend wie sensible Dateien. Im
Reiter **Verwaltung** kann ein Band-Admin einen bestimmten Sicherungspunkt
auswählen und ihn nach Eingabe des aktuellen Passworts sowie, falls
eingerichtet, eines 2FA-Codes wiederherstellen. Vorher legt die App immer
zusätzlich einen neuen Sicherungspunkt des aktuellen Zustands an. Dabei werden
ausschließlich `merch.sqlite3`, Rechnungsanhänge und Produktfotos ersetzt; `users.sqlite3`
mit Konten, Rollen und MFA bleibt unverändert. Alte Ein-Datei-Sicherungen, die
noch eine `users`-Tabelle enthalten, werden absichtlich nicht automatisch
geladen.

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
liegen bereits verschlüsselte Datenbank-Sicherungen nach jeder Buchung vor;
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
