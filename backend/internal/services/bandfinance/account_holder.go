package bandfinance

import (
	"context"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/services/accountholder"
)

type AccountHolder = accountholder.AccountHolder

func resolveAccountHolder(ctx context.Context, database *gorm.DB, userID *int64) (*int64, string, error) {
	return accountholder.Resolve(ctx, database, userID)
}

func listAccountHolders(ctx context.Context, database *gorm.DB) ([]AccountHolder, error) {
	return accountholder.List(ctx, database)
}

func sameAccountHolder(left, right *int64) bool {
	return accountholder.Same(left, right)
}
