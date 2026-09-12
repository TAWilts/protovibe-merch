package bandfinance

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// AccountHolder is an active band account that can pay an expense or receive
// an income. A nil account-holder ID on a transaction represents the band cash
// account instead.
type AccountHolder struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

func resolveAccountHolder(
	ctx context.Context,
	database *gorm.DB,
	userID *int64,
) (*int64, string, error) {
	if userID == nil {
		return nil, "", nil
	}
	bandID, err := tenant.BandID(ctx)
	if err != nil {
		return nil, "", err
	}
	var user models.User
	err = database.WithContext(tenant.WithCrossBandAccess(ctx)).
		Where("id = ? AND band_id = ? AND is_active = ?", *userID, bandID, true).
		First(&user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", ErrInvalidAccountHolder
		}
		return nil, "", err
	}
	resolvedID := user.ID
	return &resolvedID, user.Username, nil
}

func listAccountHolders(ctx context.Context, database *gorm.DB) ([]AccountHolder, error) {
	bandID, err := tenant.BandID(ctx)
	if err != nil {
		return nil, err
	}
	var users []models.User
	if err := database.WithContext(tenant.WithCrossBandAccess(ctx)).
		Where("band_id = ? AND is_active = ?", bandID, true).
		Order("username, id").
		Find(&users).Error; err != nil {
		return nil, err
	}
	holders := make([]AccountHolder, 0, len(users))
	for _, user := range users {
		holders = append(holders, AccountHolder{ID: user.ID, Username: user.Username})
	}
	return holders, nil
}

func sameAccountHolder(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
