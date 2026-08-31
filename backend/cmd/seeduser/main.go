//go:build seed

// Command seeduser creates a local development account and prints its one-time
// setup code. It is build-tagged so it never ships in a release image.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/config"
	"github.com/tawilts/protovibe-merch/backend/internal/db"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: seeduser <username> <role> [band-id]")
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	database, err := db.Open(cfg)
	if err != nil {
		panic(err)
	}
	service, err := auth.NewService(database, cfg)
	if err != nil {
		panic(err)
	}

	role := models.Role(os.Args[2])
	var bandID *int64
	if len(os.Args) > 3 {
		parsed, err := strconv.ParseInt(os.Args[3], 10, 64)
		if err != nil {
			panic(err)
		}
		bandID = &parsed
	}

	user, code, err := service.CreateUser(context.Background(), bandID, os.Args[1], role)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s %s\n", user.Username, code)
}
