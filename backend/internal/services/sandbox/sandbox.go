// Package sandbox manages disposable demo tenants. Demo operations themselves
// still run through the normal catalogue, purchase, sales and report services.
package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/storage"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

const TemplateVersion = 1

var (
	ErrDisabled = errors.New("sandbox: disabled")
	ErrCapacity = errors.New("sandbox: capacity reached")
	ErrExpired  = errors.New("sandbox: expired")
	ErrNotFound = errors.New("sandbox: not found")
)

type Config struct {
	Enabled           bool
	IdleTTL           time.Duration
	MaxActive         int
	StorageQuotaBytes int64
}

type Service struct {
	db    *gorm.DB
	files storage.Store
	cfg   Config
	now   func() time.Time
}

func NewService(db *gorm.DB, files storage.Store, cfg Config) *Service {
	return &Service{db: db, files: files, cfg: cfg, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Enabled() bool { return s.cfg.Enabled }

func (s *Service) cross(ctx context.Context) *gorm.DB {
	return s.db.WithContext(tenant.WithCrossBandAccess(ctx))
}

// ActiveForSource finds the one reusable environment belonging to a signed-in
// real account. Anonymous sandboxes are resumed only through their cookie.
func (s *Service) ActiveForSource(ctx context.Context, userID int64) (*models.SandboxEnvironment, error) {
	var env models.SandboxEnvironment
	err := s.cross(ctx).
		Where("source_user_id = ? AND status = ? AND expires_at > ?", userID, models.SandboxStatusActive, s.now()).
		Order("created_at DESC, id DESC").First(&env).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &env, err
}

func (s *Service) Load(ctx context.Context, id int64) (*models.SandboxEnvironment, error) {
	var env models.SandboxEnvironment
	if err := s.cross(ctx).First(&env, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if env.Status != models.SandboxStatusActive || !s.now().Before(env.ExpiresAt) {
		_ = s.cross(ctx).Model(&env).Update("status", models.SandboxStatusPurging).Error
		return nil, ErrExpired
	}
	return &env, nil
}

func (s *Service) User(ctx context.Context, env *models.SandboxEnvironment) (*models.User, error) {
	var user models.User
	if err := s.cross(ctx).First(&user, env.UserID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func initialProgress() models.JSONMap {
	return models.JSONMap{"catalogue": false, "purchase": false, "sale": false, "balance": false}
}

func (s *Service) Create(ctx context.Context, sourceUserID *int64) (*models.SandboxEnvironment, *models.User, error) {
	return s.create(ctx, sourceUserID, 0)
}

// Replace builds a fresh environment before the caller retires the old one.
// The replaced slot is excluded from the capacity count so reset remains
// available even while the instance is exactly at its configured limit.
func (s *Service) Replace(ctx context.Context, sourceUserID *int64, replacingID int64) (*models.SandboxEnvironment, *models.User, error) {
	return s.create(ctx, sourceUserID, replacingID)
}

func (s *Service) create(ctx context.Context, sourceUserID *int64, replacingID int64) (*models.SandboxEnvironment, *models.User, error) {
	if !s.cfg.Enabled {
		return nil, nil, ErrDisabled
	}

	now := s.now()
	var env models.SandboxEnvironment
	var user models.User
	var photoKey string
	err := s.cross(ctx).Transaction(func(tx *gorm.DB) error {
		// Every instance has exactly one settings row. Locking it serialises the
		// capacity check with tenant creation, so concurrent starts cannot both
		// claim the final free slot.
		var settings models.PlatformSettings
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&settings, 1).Error; err != nil {
			return err
		}
		var active int64
		query := tx.Model(&models.SandboxEnvironment{}).
			Where("status = ? AND expires_at > ?", models.SandboxStatusActive, now)
		if replacingID > 0 {
			query = query.Where("id <> ?", replacingID)
		}
		if err := query.Count(&active).Error; err != nil {
			return err
		}
		if active >= int64(s.cfg.MaxActive) {
			return ErrCapacity
		}

		suffix := uuid.NewString()[:12]
		band := models.Band{
			Slug: "sandbox-" + suffix, Name: "Demo Band · Sandbox", IsActive: true,
			StorageQuotaBytes: s.cfg.StorageQuotaBytes,
			FeatureFlags: models.FeatureFlags{
				PaymentQR: boolPointer(false), OfflineSales: boolPointer(false),
			},
		}
		if err := tx.Create(&band).Error; err != nil {
			return err
		}
		hash, err := auth.HashPassword(uuid.NewString() + uuid.NewString())
		if err != nil {
			return err
		}
		user = models.User{
			BandID: &band.ID, Username: "Demo", PasswordHash: hash,
			Role: models.RoleBandAdmin, IsActive: true,
			MFARecoveryCodeHashes: models.JSONSlice{}, UITheme: "aurora", UILanguage: "de",
			ShowVariantPhotos: true, TelemetryDecidedAt: &now,
			TelemetryConsentVersion: models.CurrentTelemetryConsentVersion,
		}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		env = models.SandboxEnvironment{
			BandID: band.ID, UserID: user.ID, SourceUserID: sourceUserID,
			TemplateVersion: TemplateVersion, TutorialState: initialProgress(), TutorialVisible: true,
			Status: models.SandboxStatusActive, LastActiveAt: now, ExpiresAt: now.Add(s.cfg.IdleTTL),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&env).Error; err != nil {
			return err
		}
		key, err := s.seed(tx.WithContext(tenant.WithBand(ctx, band.ID)), band.ID, user.ID, now)
		if err != nil {
			return err
		}
		photoKey = key
		return nil
	})
	if err != nil {
		if photoKey != "" {
			_ = s.files.Delete(ctx, photoKey)
		}
		return nil, nil, err
	}
	return &env, &user, nil
}

func boolPointer(value bool) *bool { return &value }

func (s *Service) seed(db *gorm.DB, bandID, userID int64, now time.Time) (string, error) {
	article := models.Article{Name: "Tour-Shirt", DefaultSalePriceCents: 2500, IsOffered: true, IsActive: true}
	if err := db.Create(&article).Error; err != nil {
		return "", err
	}
	group := models.OptionGroup{ArticleID: article.ID, Name: "Ausführung", Position: 0, IsActive: true}
	if err := db.Create(&group).Error; err != nil {
		return "", err
	}
	labels := []string{"Schwarz M", "Weiß L", "Vorbestellung", "Pausiert", "Aus Sortiment"}
	values := make([]models.OptionValue, len(labels))
	for i, label := range labels {
		values[i] = models.OptionValue{OptionGroupID: group.ID, Value: label, Position: i, IsActive: true}
		if err := db.Create(&values[i]).Error; err != nil {
			return "", err
		}
	}
	zero, target := 0, 15
	variants := []models.Variant{
		{ArticleID: article.ID, OptionValueIDs: models.JSONInt64Slice{values[0].ID}, CombinationKey: fmt.Sprint(values[0].ID), SalePriceCents: 2500, MinimumStock: intPointer(5), TargetStock: &target, IsOffered: true, IsActive: true},
		{ArticleID: article.ID, OptionValueIDs: models.JSONInt64Slice{values[1].ID}, CombinationKey: fmt.Sprint(values[1].ID), SalePriceCents: 2500, TargetStock: &target, IsOffered: true, NoReorder: true, IsActive: true},
		{ArticleID: article.ID, OptionValueIDs: models.JSONInt64Slice{values[2].ID}, CombinationKey: fmt.Sprint(values[2].ID), SalePriceCents: 2500, TargetStock: &zero, IsOffered: true, IsActive: true},
		{ArticleID: article.ID, OptionValueIDs: models.JSONInt64Slice{values[3].ID}, CombinationKey: fmt.Sprint(values[3].ID), SalePriceCents: 2500, TargetStock: &target, IsOffered: false, IsActive: true},
		{ArticleID: article.ID, OptionValueIDs: models.JSONInt64Slice{values[4].ID}, CombinationKey: fmt.Sprint(values[4].ID), SalePriceCents: 2500, IsOffered: false, NoReorder: true, IsActive: true},
	}
	for i := range variants {
		if err := db.Create(&variants[i]).Error; err != nil {
			return "", err
		}
	}

	today := models.NewDate(now.Year(), now.Month(), now.Day())
	actorID := userID
	actor := models.Actor{CreatedByUserID: &actorID, CreatedByUsername: "Demo"}
	purchases := []models.Purchase{
		{ReceiptID: "DEMO-E-001", VariantID: variants[0].ID, Quantity: 20, UnitCostCents: 900, LineTotalCostCents: 18000, PriceMode: models.PurchasePriceUnit, PricesIncludeVAT: true, VATRateBasisPoints: 1900, PurchasedOn: today, Supplier: "Beispieltextilien", InvoiceReference: "DEMO-4711", Actor: actor},
		{ReceiptID: "DEMO-E-001", VariantID: variants[1].ID, Quantity: 6, UnitCostCents: 900, LineTotalCostCents: 5400, PriceMode: models.PurchasePriceUnit, PricesIncludeVAT: true, VATRateBasisPoints: 1900, PurchasedOn: today, Supplier: "Beispieltextilien", InvoiceReference: "DEMO-4711", Actor: actor},
	}
	for i := range purchases {
		if err := db.Create(&purchases[i]).Error; err != nil {
			return "", err
		}
	}
	event := models.SaleEvent{Name: "Demo-Konzert Berlin", LastSelectedAt: now}
	if err := db.Create(&event).Error; err != nil {
		return "", err
	}
	if err := db.Create(&models.SaleEventState{EventID: event.ID, UpdatedAt: now}).Error; err != nil {
		return "", err
	}
	variantID := variants[0].ID
	if err := db.Create(&models.Sale{
		ReceiptID: "DEMO-V-001", LineType: models.SaleLineMerchandise, VariantID: &variantID,
		Quantity: 3, UnitPriceCents: 2500, AmountDueCents: 7500, AmountGivenCents: int64Pointer(7500),
		PaymentMethod: models.PaymentMethodCash, IsPaid: true, IsReceived: true,
		DeliveryStatus: models.DeliveryNotApplicable, EventName: event.Name, SoldBy: "Demo", SoldOn: today, Actor: actor,
	}).Error; err != nil {
		return "", err
	}
	if err := db.Create(&models.BandTransaction{
		TransactionType: models.BandExpense, TransactionOn: today, Category: "Werbung / Marketing",
		Description: "Demo-Plakate", AmountCents: 4500, IsSettled: true,
		CreatedAt: now, UpdatedAt: now, Actor: actor,
	}).Error; err != nil {
		return "", err
	}
	bagID, itemID := uuid.NewString(), uuid.NewString()
	if err := db.Create(&models.PackingListState{Revision: 2, Generation: 1}).Error; err != nil {
		return "", err
	}
	if err := db.Create(&models.PackingBag{ID: bagID, Name: "Merch-Kiste", Position: 0, Status: models.PackingOpen, Actor: actor}).Error; err != nil {
		return "", err
	}
	if err := db.Create(&models.PackingItem{ID: itemID, BagID: bagID, Name: "Kartenterminal", Position: 0, Status: models.PackingPacked, Actor: actor}).Error; err != nil {
		return "", err
	}

	image, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2n2kAAAAASUVORK5CYII=")
	object, err := s.files.Put(db.Statement.Context, bandID, storage.CategoryVariantPhoto, "image/png", bytes.NewReader(image))
	if err != nil {
		return "", err
	}
	photo := models.VariantPhoto{VariantID: variants[0].ID, FilePath: object.Key, OriginalFilename: "demo-shirt.png", IncludeInSlideshow: true, ShowPrice: true, SizeBytes: object.SizeBytes, CreatedAt: now, Actor: actor}
	if err := db.Create(&photo).Error; err != nil {
		_ = s.files.Delete(db.Statement.Context, object.Key)
		return "", err
	}
	return object.Key, nil
}

func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }

func (s *Service) Touch(ctx context.Context, env *models.SandboxEnvironment) error {
	now := s.now()
	if now.Sub(env.LastActiveAt) < time.Minute {
		return nil
	}
	env.LastActiveAt, env.ExpiresAt = now, now.Add(s.cfg.IdleTTL)
	return s.cross(ctx).Model(env).Updates(map[string]any{"last_active_at": env.LastActiveAt, "expires_at": env.ExpiresAt, "updated_at": now}).Error
}

func (s *Service) SetRole(ctx context.Context, env *models.SandboxEnvironment, role models.Role) (*models.User, error) {
	if !role.IsBandRole() {
		return nil, fmt.Errorf("sandbox: invalid role")
	}
	if err := s.cross(ctx).Model(&models.User{}).Where("id = ?", env.UserID).Update("role", role).Error; err != nil {
		return nil, err
	}
	return s.User(ctx, env)
}

func (s *Service) SetTutorialVisible(ctx context.Context, env *models.SandboxEnvironment, visible bool, restart bool) error {
	updates := map[string]any{"tutorial_visible": visible, "updated_at": s.now()}
	if restart {
		updates["tutorial_state"] = initialProgress()
	}
	return s.cross(ctx).Model(env).Updates(updates).Error
}

func (s *Service) MarkProgress(ctx context.Context, env *models.SandboxEnvironment, step string) error {
	if step != "catalogue" && step != "purchase" && step != "sale" && step != "balance" {
		return nil
	}
	state := env.TutorialState
	if state == nil {
		state = initialProgress()
	}
	if done, _ := state[step].(bool); done {
		return nil
	}
	state[step] = true
	env.TutorialState = state
	return s.cross(ctx).Model(env).Updates(map[string]any{"tutorial_state": state, "updated_at": s.now()}).Error
}

func (s *Service) MarkPurging(ctx context.Context, env *models.SandboxEnvironment) error {
	env.Status = models.SandboxStatusPurging
	return s.cross(ctx).Model(env).Updates(map[string]any{"status": models.SandboxStatusPurging, "updated_at": s.now()}).Error
}

func (s *Service) PurgeExpired(ctx context.Context) (int, error) {
	var environments []models.SandboxEnvironment
	if err := s.cross(ctx).Where("status = ? OR expires_at <= ?", models.SandboxStatusPurging, s.now()).Find(&environments).Error; err != nil {
		return 0, err
	}
	removed := 0
	for i := range environments {
		if err := s.Purge(ctx, &environments[i]); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func (s *Service) Purge(ctx context.Context, env *models.SandboxEnvironment) error {
	if err := s.MarkPurging(ctx, env); err != nil {
		return err
	}
	if err := s.files.DeleteBand(ctx, env.BandID); err != nil {
		return err
	}
	return s.cross(ctx).Transaction(func(tx *gorm.DB) error {
		bandID := env.BandID
		for _, table := range []string{
			"packing_sync_events", "packing_photos", "packing_items", "packing_bags", "packing_list_states",
			"recurring_band_transaction_runs", "recurring_band_transactions",
			"band_transaction_attachments", "band_transactions", "purchase_receipt_attachments", "purchases",
			"payment_qr_intents", "payment_qr_settings", "sync_events", "sales", "sale_event_state", "sale_events",
			"variant_photos", "slideshow_extra_photos", "slideshow_settings", "variants", "option_values", "option_groups", "articles", "admin_messages",
		} {
			if err := tx.Exec("DELETE FROM "+table+" WHERE band_id = ?", bandID).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec("DELETE FROM audit_log WHERE band_id = ?", bandID).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM sessions WHERE sandbox_environment_id = ? OR user_id = ?", env.ID, env.UserID).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.SandboxEnvironment{}, env.ID).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM pending_auth WHERE user_id = ?", env.UserID).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM password_reset_challenges WHERE user_id = ?", env.UserID).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.User{}, env.UserID).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Band{}, bandID).Error
	})
}
