// Package packing owns the band's reusable, offline-first packing list.
package packing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/storage"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

var (
	ErrInvalidOperation  = errors.New("packing: invalid operation")
	ErrInvalidID         = errors.New("packing: identifiers must be UUIDs")
	ErrInvalidName       = errors.New("packing: name is required and may contain at most 200 characters")
	ErrInvalidStatus     = errors.New("packing: invalid status")
	ErrTargetNotFound    = errors.New("packing: target no longer exists")
	ErrGenerationChanged = errors.New("packing: the list was reset while this change was offline")
	ErrSyncConflict      = errors.New("packing: event identifier was reused with different data")
)

const (
	OpCreateBag     = "create_bag"
	OpRenameBag     = "rename_bag"
	OpDeleteBag     = "delete_bag"
	OpReorderBags   = "reorder_bags"
	OpCreateItem    = "create_item"
	OpRenameItem    = "rename_item"
	OpDeleteItem    = "delete_item"
	OpReorderItems  = "reorder_items"
	OpSetBagStatus  = "set_bag_status"
	OpSetItemStatus = "set_item_status"
	OpDeletePhoto   = "delete_photo"
	OpReset         = "reset"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type Operation struct {
	EventID         string               `json:"event_id"`
	DeviceID        string               `json:"device_id"`
	ClientCreatedAt time.Time            `json:"client_created_at"`
	BaseGeneration  int64                `json:"base_generation"`
	Type            string               `json:"type"`
	BagID           string               `json:"bag_id,omitempty"`
	ItemID          string               `json:"item_id,omitempty"`
	PhotoID         string               `json:"photo_id,omitempty"`
	Name            string               `json:"name,omitempty"`
	Status          models.PackingStatus `json:"status,omitempty"`
	OrderIDs        []string             `json:"order_ids,omitempty"`
}

func (o Operation) IsStatusOnly() bool {
	return o.Type == OpSetBagStatus || o.Type == OpSetItemStatus
}

func (o Operation) IsManagement() bool { return !o.IsStatusOnly() }

type Photo struct {
	ID               string  `json:"id"`
	BagID            *string `json:"bag_id,omitempty"`
	ItemID           *string `json:"item_id,omitempty"`
	OriginalFilename string  `json:"original_filename"`
	SizeBytes        int64   `json:"size_bytes"`
	Position         int     `json:"position"`
}

type Item struct {
	ID       string               `json:"id"`
	BagID    string               `json:"bag_id"`
	Name     string               `json:"name"`
	Position int                  `json:"position"`
	Status   models.PackingStatus `json:"status"`
	Photos   []Photo              `json:"photos"`
}

type Bag struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	Position int                  `json:"position"`
	Status   models.PackingStatus `json:"status"`
	Photos   []Photo              `json:"photos"`
	Items    []Item               `json:"items"`
}

type Snapshot struct {
	Revision   int64 `json:"revision"`
	Generation int64 `json:"generation"`
	Bags       []Bag `json:"bags"`
}

type Result struct {
	Snapshot
	Replayed        bool     `json:"replayed"`
	DeletedFileKeys []string `json:"-"`
}

type Actor struct {
	UserID   int64
	Username string
}

type Service struct {
	db  *gorm.DB
	hub *Hub
}

func NewService(db *gorm.DB) *Service { return &Service{db: db, hub: NewHub()} }
func (s *Service) Hub() *Hub          { return s.hub }

func validID(value string) bool { return uuidPattern.MatchString(strings.TrimSpace(value)) }

func validateName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 200 {
		return "", ErrInvalidName
	}
	return value, nil
}

func validStatus(status models.PackingStatus) bool {
	return status == models.PackingOpen || status == models.PackingPacked || status == models.PackingStaysHere
}

func payloadHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) Snapshot(ctx context.Context) (*Snapshot, error) {
	state, err := s.ensureState(s.db.WithContext(ctx), ctx, false)
	if err != nil {
		return nil, err
	}
	return loadSnapshot(s.db.WithContext(ctx), state)
}

func (s *Service) Apply(ctx context.Context, operation Operation, actor Actor) (*Result, error) {
	operation.EventID = strings.TrimSpace(operation.EventID)
	operation.DeviceID = strings.TrimSpace(operation.DeviceID)
	if operation.EventID == "" || len(operation.EventID) > 64 || operation.DeviceID == "" || len(operation.DeviceID) > 64 {
		return nil, ErrInvalidID
	}
	if operation.ClientCreatedAt.IsZero() {
		operation.ClientCreatedAt = time.Now().UTC()
	}
	hash, err := payloadHash(operation)
	if err != nil {
		return nil, err
	}

	var result *Result
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var prior models.PackingSyncEvent
		if err := tx.WithContext(ctx).Where("event_id = ?", operation.EventID).First(&prior).Error; err == nil {
			if prior.PayloadHash != hash {
				return ErrSyncConflict
			}
			var replay Result
			if err := json.Unmarshal([]byte(prior.ResponseJSON), &replay); err != nil {
				return err
			}
			replay.Replayed = true
			result = &replay
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		state, err := s.ensureState(tx, ctx, true)
		if err != nil {
			return err
		}
		if operation.IsStatusOnly() && operation.BaseGeneration != state.Generation {
			return ErrGenerationChanged
		}

		deleted, err := applyOperation(ctx, tx, state, operation, actor)
		if err != nil {
			return err
		}
		state.Revision++
		if err := tx.WithContext(ctx).Model(state).Updates(map[string]any{
			"revision": state.Revision, "generation": state.Generation, "updated_at": time.Now().UTC(),
		}).Error; err != nil {
			return err
		}

		snapshot, err := loadSnapshot(tx.WithContext(ctx), state)
		if err != nil {
			return err
		}
		result = &Result{Snapshot: *snapshot, DeletedFileKeys: deleted}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		bandID := tenant.MustBandID(ctx)
		event := &models.PackingSyncEvent{
			Tenant: models.Tenant{BandID: bandID}, EventID: operation.EventID,
			DeviceID: operation.DeviceID, ActorUserID: actor.UserID, ActorUsername: actor.Username,
			PayloadHash: hash, ResponseJSON: string(encoded), ClientCreatedAt: operation.ClientCreatedAt,
		}
		return tx.WithContext(ctx).Create(event).Error
	})
	if err != nil {
		return nil, err
	}
	if result != nil && !result.Replayed {
		s.hub.Publish(tenant.MustBandID(ctx), result.Revision)
	}
	return result, nil
}

type PhotoInput struct {
	EventID          string
	DeviceID         string
	ClientCreatedAt  time.Time
	PhotoID          string
	BagID            string
	ItemID           string
	OriginalFilename string
	Data             []byte
}

func validatePhotoInput(input PhotoInput) error {
	if input.EventID == "" || len(input.EventID) > 64 || input.DeviceID == "" || len(input.DeviceID) > 64 || !validID(input.PhotoID) {
		return ErrInvalidID
	}
	if (input.BagID == "") == (input.ItemID == "") || (input.BagID != "" && !validID(input.BagID)) || (input.ItemID != "" && !validID(input.ItemID)) {
		return ErrInvalidID
	}
	return nil
}

func photoInputHash(input PhotoInput) (string, error) {
	imageHash := sha256.Sum256(input.Data)
	return payloadHash(struct {
		EventID, DeviceID, PhotoID, BagID, ItemID, OriginalFilename, ImageHash string
		ClientCreatedAt                                                        time.Time
	}{input.EventID, input.DeviceID, input.PhotoID, input.BagID, input.ItemID, input.OriginalFilename, hex.EncodeToString(imageHash[:]), input.ClientCreatedAt})
}

// ReplayPhoto returns a prior upload result before a caller reserves storage
// quota. That keeps a legitimate retry idempotent even if the first upload
// filled the band's remaining quota.
func (s *Service) ReplayPhoto(ctx context.Context, input PhotoInput) (*Result, bool, error) {
	input.EventID = strings.TrimSpace(input.EventID)
	input.DeviceID = strings.TrimSpace(input.DeviceID)
	if err := validatePhotoInput(input); err != nil {
		return nil, false, err
	}
	hash, err := photoInputHash(input)
	if err != nil {
		return nil, false, err
	}
	return s.replayPhoto(ctx, input.EventID, hash)
}

// UploadPhoto stores an already-normalised JPEG and records it as one
// idempotent packing mutation. The object is removed again if the database
// transaction does not commit.
func (s *Service) UploadPhoto(ctx context.Context, input PhotoInput, actor Actor, files storage.Store) (*Result, error) {
	input.EventID = strings.TrimSpace(input.EventID)
	input.DeviceID = strings.TrimSpace(input.DeviceID)
	if err := validatePhotoInput(input); err != nil {
		return nil, ErrInvalidID
	}
	if input.ClientCreatedAt.IsZero() {
		input.ClientCreatedAt = time.Now().UTC()
	}
	hash, err := photoInputHash(input)
	if err != nil {
		return nil, err
	}

	if replay, found, err := s.replayPhoto(ctx, input.EventID, hash); err != nil || found {
		return replay, err
	}
	if err := s.requirePhotoOwner(ctx, s.db.WithContext(ctx), input.BagID, input.ItemID); err != nil {
		return nil, err
	}

	object, err := files.Put(ctx, tenant.MustBandID(ctx), storage.CategoryPackingPhoto, "image/jpeg", bytes.NewReader(input.Data))
	if err != nil {
		return nil, err
	}
	keepObject := false
	defer func() {
		if !keepObject {
			_ = files.Delete(ctx, object.Key)
		}
	}()

	var result *Result
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if replay, found, err := s.replayPhotoWithDB(ctx, tx, input.EventID, hash); err != nil {
			return err
		} else if found {
			result = replay
			return nil
		}
		state, err := s.ensureState(tx, ctx, true)
		if err != nil {
			return err
		}
		if err := s.requirePhotoOwner(ctx, tx, input.BagID, input.ItemID); err != nil {
			return err
		}
		position, err := nextPhotoPosition(ctx, tx, input.BagID, input.ItemID)
		if err != nil {
			return err
		}
		photo := &models.PackingPhoto{
			ID: input.PhotoID, Tenant: models.Tenant{BandID: tenant.MustBandID(ctx)},
			FilePath: object.Key, OriginalFilename: input.OriginalFilename, SizeBytes: object.SizeBytes,
			Position: position, CreatedByUserID: &actor.UserID, CreatedByUsername: actor.Username,
		}
		if input.BagID != "" {
			photo.BagID = &input.BagID
		} else {
			photo.ItemID = &input.ItemID
		}
		if err := tx.WithContext(ctx).Create(photo).Error; err != nil {
			return err
		}
		state.Revision++
		if err := tx.WithContext(ctx).Model(state).Updates(map[string]any{"revision": state.Revision, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		snapshot, err := loadSnapshot(tx.WithContext(ctx), state)
		if err != nil {
			return err
		}
		result = &Result{Snapshot: *snapshot}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		event := &models.PackingSyncEvent{
			Tenant: models.Tenant{BandID: tenant.MustBandID(ctx)}, EventID: input.EventID, DeviceID: input.DeviceID,
			ActorUserID: actor.UserID, ActorUsername: actor.Username, PayloadHash: hash,
			ResponseJSON: string(encoded), ClientCreatedAt: input.ClientCreatedAt,
		}
		return tx.WithContext(ctx).Create(event).Error
	})
	if err != nil {
		return nil, err
	}
	if result != nil && !result.Replayed {
		keepObject = true
		s.hub.Publish(tenant.MustBandID(ctx), result.Revision)
	}
	return result, nil
}

func (s *Service) replayPhoto(ctx context.Context, eventID, hash string) (*Result, bool, error) {
	return s.replayPhotoWithDB(ctx, s.db.WithContext(ctx), eventID, hash)
}

func (s *Service) replayPhotoWithDB(ctx context.Context, db *gorm.DB, eventID, hash string) (*Result, bool, error) {
	var prior models.PackingSyncEvent
	err := db.WithContext(ctx).Where("event_id = ?", eventID).First(&prior).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if prior.PayloadHash != hash {
		return nil, true, ErrSyncConflict
	}
	var result Result
	if err := json.Unmarshal([]byte(prior.ResponseJSON), &result); err != nil {
		return nil, true, err
	}
	result.Replayed = true
	return &result, true, nil
}

func (s *Service) requirePhotoOwner(ctx context.Context, db *gorm.DB, bagID, itemID string) error {
	if bagID != "" {
		return requireBag(ctx, db, bagID)
	}
	var count int64
	if err := db.WithContext(ctx).Model(&models.PackingItem{}).Where("id = ?", itemID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrTargetNotFound
	}
	return nil
}

func nextPhotoPosition(ctx context.Context, tx *gorm.DB, bagID, itemID string) (int, error) {
	var max sql.NullInt64
	query := tx.WithContext(ctx).Model(&models.PackingPhoto{})
	if bagID != "" {
		query = query.Where("bag_id = ?", bagID)
	} else {
		query = query.Where("item_id = ?", itemID)
	}
	if err := query.Select("MAX(position)").Scan(&max).Error; err != nil {
		return 0, err
	}
	if !max.Valid {
		return 0, nil
	}
	return int(max.Int64) + 10, nil
}

func (s *Service) ensureState(tx *gorm.DB, ctx context.Context, lock bool) (*models.PackingListState, error) {
	query := tx.WithContext(ctx)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var state models.PackingListState
	err := query.First(&state).Error
	if err == nil {
		return &state, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	state = models.PackingListState{Tenant: models.Tenant{BandID: tenant.MustBandID(ctx)}, Generation: 1}
	if err := tx.WithContext(ctx).Create(&state).Error; err != nil {
		// A concurrent first reader may have inserted the one-per-band row.
		if err := query.First(&state).Error; err != nil {
			return nil, err
		}
	}
	return &state, nil
}

func applyOperation(ctx context.Context, tx *gorm.DB, state *models.PackingListState, op Operation, actor Actor) ([]string, error) {
	switch op.Type {
	case OpCreateBag:
		if !validID(op.BagID) {
			return nil, ErrInvalidID
		}
		name, err := validateName(op.Name)
		if err != nil {
			return nil, err
		}
		position, err := nextPosition[models.PackingBag](ctx, tx, "")
		if err != nil {
			return nil, err
		}
		return nil, tx.WithContext(ctx).Create(&models.PackingBag{
			ID: op.BagID, Tenant: models.Tenant{BandID: tenant.MustBandID(ctx)}, Name: name,
			Position: position, Status: models.PackingOpen,
			Actor: models.Actor{CreatedByUserID: &actor.UserID, CreatedByUsername: actor.Username},
		}).Error
	case OpRenameBag:
		name, err := validateName(op.Name)
		if err != nil {
			return nil, err
		}
		return nil, updateExisting(ctx, tx, &models.PackingBag{}, op.BagID, map[string]any{"name": name})
	case OpDeleteBag:
		if !validID(op.BagID) {
			return nil, ErrInvalidID
		}
		keys, err := photoKeysForBag(ctx, tx, op.BagID)
		if err != nil {
			return nil, err
		}
		res := tx.WithContext(ctx).Delete(&models.PackingBag{}, "id = ?", op.BagID)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			return nil, ErrTargetNotFound
		}
		return keys, nil
	case OpReorderBags:
		return nil, reorder(ctx, tx, &models.PackingBag{}, "", op.OrderIDs)
	case OpCreateItem:
		if !validID(op.ItemID) || !validID(op.BagID) {
			return nil, ErrInvalidID
		}
		if err := requireBag(ctx, tx, op.BagID); err != nil {
			return nil, err
		}
		name, err := validateName(op.Name)
		if err != nil {
			return nil, err
		}
		position, err := nextPosition[models.PackingItem](ctx, tx, op.BagID)
		if err != nil {
			return nil, err
		}
		err = tx.WithContext(ctx).Create(&models.PackingItem{
			ID: op.ItemID, Tenant: models.Tenant{BandID: tenant.MustBandID(ctx)}, BagID: op.BagID,
			Name: name, Position: position, Status: models.PackingOpen,
			Actor: models.Actor{CreatedByUserID: &actor.UserID, CreatedByUsername: actor.Username},
		}).Error
		if err == nil {
			err = tx.WithContext(ctx).Model(&models.PackingBag{}).Where("id = ? AND status <> ?", op.BagID, models.PackingStaysHere).Update("status", models.PackingOpen).Error
		}
		return nil, err
	case OpRenameItem:
		name, err := validateName(op.Name)
		if err != nil {
			return nil, err
		}
		return nil, updateExisting(ctx, tx, &models.PackingItem{}, op.ItemID, map[string]any{"name": name})
	case OpDeleteItem:
		if !validID(op.ItemID) {
			return nil, ErrInvalidID
		}
		var item models.PackingItem
		if err := tx.WithContext(ctx).Where("id = ?", op.ItemID).First(&item).Error; err != nil {
			return nil, ErrTargetNotFound
		}
		var photos []models.PackingPhoto
		if err := tx.WithContext(ctx).Where("item_id = ?", item.ID).Find(&photos).Error; err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(photos))
		for _, photo := range photos {
			keys = append(keys, photo.FilePath)
		}
		if err := tx.WithContext(ctx).Delete(&item).Error; err != nil {
			return nil, err
		}
		if err := recomputeBag(ctx, tx, item.BagID); err != nil {
			return nil, err
		}
		return keys, nil
	case OpReorderItems:
		if err := requireBag(ctx, tx, op.BagID); err != nil {
			return nil, err
		}
		return nil, reorder(ctx, tx, &models.PackingItem{}, op.BagID, op.OrderIDs)
	case OpSetItemStatus:
		if !validStatus(op.Status) || !validID(op.ItemID) {
			return nil, ErrInvalidStatus
		}
		var item models.PackingItem
		if err := tx.WithContext(ctx).Where("id = ?", op.ItemID).First(&item).Error; err != nil {
			return nil, ErrTargetNotFound
		}
		if err := tx.WithContext(ctx).Model(&item).Update("status", op.Status).Error; err != nil {
			return nil, err
		}
		return nil, recomputeBag(ctx, tx, item.BagID)
	case OpSetBagStatus:
		if !validStatus(op.Status) || !validID(op.BagID) {
			return nil, ErrInvalidStatus
		}
		var bag models.PackingBag
		if err := tx.WithContext(ctx).Where("id = ?", op.BagID).First(&bag).Error; err != nil {
			return nil, err
		}
		if err := tx.WithContext(ctx).Model(&models.PackingBag{}).Where("id = ?", op.BagID).Update("status", op.Status).Error; err != nil {
			return nil, err
		}
		var updates map[string]any
		switch op.Status {
		case models.PackingPacked:
			updates = map[string]any{"status": models.PackingPacked}
			return nil, tx.WithContext(ctx).Model(&models.PackingItem{}).
				Where("bag_id = ? AND status <> ?", op.BagID, models.PackingStaysHere).Updates(updates).Error
		case models.PackingOpen:
			// Excluding a whole bag deliberately preserves its child state. When
			// it is included again, derive the aggregate state without silently
			// reopening items that were packed before the exclusion.
			if bag.Status == models.PackingStaysHere {
				return nil, recomputeBag(ctx, tx, op.BagID)
			}
			return nil, tx.WithContext(ctx).Model(&models.PackingItem{}).
				Where("bag_id = ? AND status = ?", op.BagID, models.PackingPacked).Update("status", models.PackingOpen).Error
		default:
			return nil, nil
		}
	case OpDeletePhoto:
		if !validID(op.PhotoID) {
			return nil, ErrInvalidID
		}
		var photo models.PackingPhoto
		if err := tx.WithContext(ctx).Where("id = ?", op.PhotoID).First(&photo).Error; err != nil {
			return nil, ErrTargetNotFound
		}
		if err := tx.WithContext(ctx).Delete(&photo).Error; err != nil {
			return nil, err
		}
		return []string{photo.FilePath}, nil
	case OpReset:
		state.Generation++
		if err := tx.WithContext(ctx).Model(&models.PackingBag{}).Where("status <> ?", models.PackingOpen).Update("status", models.PackingOpen).Error; err != nil {
			return nil, err
		}
		return nil, tx.WithContext(ctx).Model(&models.PackingItem{}).Where("status <> ?", models.PackingOpen).Update("status", models.PackingOpen).Error
	default:
		return nil, ErrInvalidOperation
	}
}

func requireBag(ctx context.Context, tx *gorm.DB, id string) error {
	if !validID(id) {
		return ErrInvalidID
	}
	var count int64
	if err := tx.WithContext(ctx).Model(&models.PackingBag{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrTargetNotFound
	}
	return nil
}

func updateExisting(ctx context.Context, tx *gorm.DB, model any, id string, updates map[string]any) error {
	if !validID(id) {
		return ErrInvalidID
	}
	res := tx.WithContext(ctx).Model(model).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrTargetNotFound
	}
	return nil
}

func nextPosition[T any](ctx context.Context, tx *gorm.DB, bagID string) (int, error) {
	var max sql.NullInt64
	query := tx.WithContext(ctx).Model(new(T))
	if bagID != "" {
		query = query.Where("bag_id = ?", bagID)
	}
	if err := query.Select("MAX(position)").Scan(&max).Error; err != nil {
		return 0, err
	}
	if !max.Valid {
		return 0, nil
	}
	return int(max.Int64) + 10, nil
}

func reorder(ctx context.Context, tx *gorm.DB, model any, bagID string, ids []string) error {
	if len(ids) == 0 {
		return ErrInvalidOperation
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validID(id) || seen[id] {
			return ErrInvalidID
		}
		seen[id] = true
	}
	query := tx.WithContext(ctx).Model(model)
	if bagID != "" {
		query = query.Where("bag_id = ?", bagID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if int64(len(ids)) != count {
		return ErrTargetNotFound
	}
	for position, id := range ids {
		update := tx.WithContext(ctx).Model(model).Where("id = ?", id)
		if bagID != "" {
			update = update.Where("bag_id = ?", bagID)
		}
		if res := update.Update("position", position*10); res.Error != nil {
			return res.Error
		} else if res.RowsAffected == 0 {
			return ErrTargetNotFound
		}
	}
	return nil
}

func recomputeBag(ctx context.Context, tx *gorm.DB, bagID string) error {
	var bag models.PackingBag
	if err := tx.WithContext(ctx).Where("id = ?", bagID).First(&bag).Error; err != nil {
		return err
	}
	if bag.Status == models.PackingStaysHere {
		return nil
	}
	var open int64
	if err := tx.WithContext(ctx).Model(&models.PackingItem{}).Where("bag_id = ? AND status = ?", bagID, models.PackingOpen).Count(&open).Error; err != nil {
		return err
	}
	status := models.PackingPacked
	if open > 0 {
		status = models.PackingOpen
	}
	return tx.WithContext(ctx).Model(&bag).Update("status", status).Error
}

func photoKeysForBag(ctx context.Context, tx *gorm.DB, bagID string) ([]string, error) {
	var photos []models.PackingPhoto
	err := tx.WithContext(ctx).Where("bag_id = ? OR item_id IN (SELECT id FROM packing_items WHERE bag_id = ?)", bagID, bagID).Find(&photos).Error
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(photos))
	for _, photo := range photos {
		keys = append(keys, photo.FilePath)
	}
	return keys, nil
}

func loadSnapshot(tx *gorm.DB, state *models.PackingListState) (*Snapshot, error) {
	var bags []models.PackingBag
	if err := tx.Order("position, id").Find(&bags).Error; err != nil {
		return nil, err
	}
	var items []models.PackingItem
	if err := tx.Order("position, id").Find(&items).Error; err != nil {
		return nil, err
	}
	var photos []models.PackingPhoto
	if err := tx.Order("position, id").Find(&photos).Error; err != nil {
		return nil, err
	}

	bagIndex := map[string]int{}
	result := &Snapshot{Revision: state.Revision, Generation: state.Generation, Bags: make([]Bag, 0, len(bags))}
	for _, bag := range bags {
		bagIndex[bag.ID] = len(result.Bags)
		result.Bags = append(result.Bags, Bag{ID: bag.ID, Name: bag.Name, Position: bag.Position, Status: bag.Status, Photos: []Photo{}, Items: []Item{}})
	}
	itemIndex := map[string][2]int{}
	for _, item := range items {
		bi, ok := bagIndex[item.BagID]
		if !ok {
			continue
		}
		itemIndex[item.ID] = [2]int{bi, len(result.Bags[bi].Items)}
		result.Bags[bi].Items = append(result.Bags[bi].Items, Item{ID: item.ID, BagID: item.BagID, Name: item.Name, Position: item.Position, Status: item.Status, Photos: []Photo{}})
	}
	for _, photo := range photos {
		p := Photo{ID: photo.ID, BagID: photo.BagID, ItemID: photo.ItemID, OriginalFilename: photo.OriginalFilename, SizeBytes: photo.SizeBytes, Position: photo.Position}
		if photo.BagID != nil {
			if bi, ok := bagIndex[*photo.BagID]; ok {
				result.Bags[bi].Photos = append(result.Bags[bi].Photos, p)
			}
		} else if photo.ItemID != nil {
			if loc, ok := itemIndex[*photo.ItemID]; ok {
				result.Bags[loc[0]].Items[loc[1]].Photos = append(result.Bags[loc[0]].Items[loc[1]].Photos, p)
			}
		}
	}
	return result, nil
}

// Hub emits revision-only hints. Clients always fetch the authoritative snapshot.
type Hub struct {
	mu   sync.Mutex
	next int
	subs map[int64]map[int]chan int64
}

func NewHub() *Hub { return &Hub{subs: map[int64]map[int]chan int64{}} }

func (h *Hub) Subscribe(bandID int64) (<-chan int64, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := h.next
	ch := make(chan int64, 1)
	if h.subs[bandID] == nil {
		h.subs[bandID] = map[int]chan int64{}
	}
	h.subs[bandID][id] = ch
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if group := h.subs[bandID]; group != nil {
			delete(group, id)
			if len(group) == 0 {
				delete(h.subs, bandID)
			}
		}
	}
}

func (h *Hub) Publish(bandID, revision int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs[bandID] {
		select {
		case ch <- revision:
		default:
		}
	}
}
