// Command importlegacy migrates a decrypted installation of the previous
// SQLite/Flask version into MariaDB as a single band.
//
// The old databases are SQLCipher-encrypted; decrypt them first, for example
// with the sqlcipher CLI, and point this command at the plain files. The real
// import lands in phase 6.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var (
		operationalPath = flag.String("operational-db", "", "path to a decrypted merch.sqlite3")
		usersPath       = flag.String("users-db", "", "path to a decrypted users.sqlite3")
		storagePath     = flag.String("legacy-storage", "", "directory holding the decrypted invoices/ and variant-photos/")
		bandName        = flag.String("band", "", "name of the band to create for the imported data")
		dryRun          = flag.Bool("dry-run", true, "report what would be imported without writing")
	)
	flag.Parse()

	for name, value := range map[string]string{
		"-operational-db": *operationalPath,
		"-users-db":       *usersPath,
		"-band":           *bandName,
	} {
		if value == "" {
			fmt.Fprintf(os.Stderr, "%s is required\n", name)
			flag.Usage()
			os.Exit(2)
		}
	}
	_ = storagePath
	_ = dryRun

	fmt.Fprintln(os.Stderr, "legacy import is implemented in phase 6")
	os.Exit(1)
}
