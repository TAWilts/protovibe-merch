// Package accountholder validates and lists the band accounts through which
// money moved. A nil user ID consistently represents the shared band cash.
package accountholder

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

var ErrInvalid = errors.New("account holder must be an active user of this band")

type AccountHolder struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

func Resolve(ctx context.Context, database *gorm.DB, userID *int64) (*int64, string, error) {
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
			return nil, "", ErrInvalid
		}
		return nil, "", err
	}
	resolvedID := user.ID
	return &resolvedID, user.Username, nil
}

func List(ctx context.Context, database *gorm.DB) ([]AccountHolder, error) {
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

// CurrentUsernames resolves current names for existing band users. Callers
// keep their stored snapshot when an ID is not returned, so deleted accounts
// remain historically readable.
func CurrentUsernames(ctx context.Context, database *gorm.DB, userIDs []int64) (map[int64]string, error) {
	names := make(map[int64]string)
	if len(userIDs) == 0 {
		return names, nil
	}
	bandID, err := tenant.BandID(ctx)
	if err != nil {
		return nil, err
	}
	type userName struct {
		ID       int64
		Username string
	}
	var users []userName
	if err := database.WithContext(tenant.WithCrossBandAccess(ctx)).
		Model(&models.User{}).
		Select("id, username").
		Where("band_id = ? AND id IN ?", bandID, userIDs).
		Scan(&users).Error; err != nil {
		return nil, err
	}
	for _, user := range users {
		names[user.ID] = user.Username
	}
	return names, nil
}

func Same(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
