package legacyimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/auth"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/platform"
	"github.com/tawilts/protovibe-merch/backend/internal/storage"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

type Options struct {
	OperationalDB  string
	UsersDB        string
	LegacyStorage  string
	BandName       string
	BandSlug       string
	ContactEmail   string
	DryRun         bool
	CredentialsOut string
}

type Credential struct {
	Username  string      `json:"username"`
	Role      models.Role `json:"role"`
	SetupCode string      `json:"setup_code"`
	IsActive  bool        `json:"is_active"`
}

type Report struct {
	DryRun         bool           `json:"dry_run"`
	BandName       string         `json:"band_name"`
	BandSlug       string         `json:"band_slug"`
	BandID         int64          `json:"band_id,omitempty"`
	Counts         map[string]int `json:"counts"`
	Files          []FileReport   `json:"files"`
	Warnings       []string       `json:"warnings,omitempty"`
	Credentials    []Credential   `json:"-"`
	CredentialFile string         `json:"credentials_file,omitempty"`
}

type Service struct {
	db       *gorm.DB
	auth     *auth.Service
	platform *platform.Service
	store    storage.Store
}

func New(database *gorm.DB, authService *auth.Service, platformService *platform.Service, store storage.Store) *Service {
	return &Service{
		db:       database,
		auth:     authService,
		platform: platformService,
		store:    store,
	}
}

type snapshot struct {
	Users                      []row
	UserAuditLog               []row
	AdminMessages              []row
	Articles                   []row
	OptionGroups               []row
	OptionValues               []row
	Variants                   []row
	VariantPhotos              []row
	SlideshowExtraPhotos       []row
	SlideshowSettings          []row
	PaymentQRSettings          []row
	PaymentQRIntents           []row
	Purchases                  []row
	PurchaseReceiptAttachments []row
	BandTransactions           []row
	BandTransactionAttachments []row
	Sales                      []row
	SaleEvents                 []row
	SaleEventState             []row
	OperationalAuditLog        []row
	SyncEvents                 []row
}

func (s *Service) Run(ctx context.Context, opts Options) (*Report, error) {
	if strings.TrimSpace(opts.BandName) == "" {
		return nil, errors.New("legacy import: band name is required")
	}
	if strings.TrimSpace(opts.BandSlug) == "" {
		opts.BandSlug = Slugify(opts.BandName)
	}
	if _, _, err := platform.ValidateBandIdentity(opts.BandSlug, opts.BandName); err != nil {
		return nil, err
	}
	if !opts.DryRun && strings.TrimSpace(opts.CredentialsOut) == "" {
		return nil, errors.New("legacy import: -credentials-out is required for a real import")
	}
	if !opts.DryRun {
		absolute, err := filepath.Abs(opts.CredentialsOut)
		if err != nil {
			return nil, fmt.Errorf("legacy import: credentials path: %w", err)
		}
		opts.CredentialsOut = absolute
	}

	operational, err := openSource(opts.OperationalDB, "operational database")
	if err != nil {
		return nil, err
	}
	defer operational.Close()

	users, err := openSource(opts.UsersDB, "users database")
	if err != nil {
		return nil, err
	}
	defer users.Close()

	snap, err := loadSnapshot(ctx, operational, users)
	if err != nil {
		return nil, err
	}
	report, err := preflight(ctx, snap, opts)
	if err != nil {
		return nil, err
	}
	if s.platform != nil {
		if err := s.platform.EnsureSlugAvailable(ctx, opts.BandSlug); err != nil {
			return nil, fmt.Errorf("legacy import target: %w", err)
		}
	}
	if opts.DryRun {
		return report, nil
	}
	if s.db == nil || s.auth == nil || s.platform == nil || s.store == nil {
		return nil, errors.New("legacy import: destination services are not configured")
	}

	var storedKeys []string
	var credentialsFileWritten bool
	cleanup := func() {
		for i := len(storedKeys) - 1; i >= 0; i-- {
			_ = s.store.Delete(context.Background(), storedKeys[i])
		}
		if credentialsFileWritten {
			_ = os.Remove(opts.CredentialsOut)
		}
	}

	err = s.db.WithContext(tenant.WithCrossBandAccess(ctx)).Transaction(func(tx *gorm.DB) error {
		band, err := s.platform.CreateBandInTransaction(ctx, tx, opts.BandSlug, opts.BandName, opts.ContactEmail)
		if err != nil {
			return err
		}
		report.BandID = band.ID

		importer := destinationImporter{
			tx:             tx,
			auth:           s.auth,
			store:          s.store,
			bandID:         band.ID,
			legacyRoot:     opts.LegacyStorage,
			storedKeys:     &storedKeys,
			report:         report,
			userIDs:        map[int64]int64{},
			articleIDs:     map[int64]int64{},
			groupIDs:       map[int64]int64{},
			valueIDs:       map[int64]int64{},
			variantIDs:     map[int64]int64{},
			eventIDs:       map[int64]int64{},
			purchaseIDs:    map[int64]int64{},
			saleIDs:        map[int64]int64{},
			transactionIDs: map[int64]int64{},
		}
		if err := importer.importAll(ctx, snap); err != nil {
			return err
		}
		report.Credentials = importer.credentials

		if err := writeCredentialsFile(opts.CredentialsOut, report.Credentials); err != nil {
			return err
		}
		credentialsFileWritten = true
		report.CredentialFile = opts.CredentialsOut
		return nil
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	return report, nil
}

func loadSnapshot(ctx context.Context, operational, users *sourceDB) (*snapshot, error) {
	var snap snapshot
	var err error

	load := func(db *sourceDB, table string, required bool, target *[]row) error {
		rows, err := db.rows(ctx, table, required)
		if err != nil {
			return err
		}
		*target = rows
		return nil
	}

	for _, item := range []struct {
		db       *sourceDB
		table    string
		required bool
		target   *[]row
	}{
		{users, "users", true, &snap.Users},
		{users, "audit_log", false, &snap.UserAuditLog},
		{users, "admin_messages", false, &snap.AdminMessages},
		{operational, "articles", true, &snap.Articles},
		{operational, "option_groups", true, &snap.OptionGroups},
		{operational, "option_values", true, &snap.OptionValues},
		{operational, "variants", true, &snap.Variants},
		{operational, "variant_photos", false, &snap.VariantPhotos},
		{operational, "slideshow_extra_photos", false, &snap.SlideshowExtraPhotos},
		{operational, "slideshow_settings", false, &snap.SlideshowSettings},
		{operational, "payment_qr_settings", false, &snap.PaymentQRSettings},
		{operational, "payment_qr_intents", false, &snap.PaymentQRIntents},
		{operational, "purchases", true, &snap.Purchases},
		{operational, "purchase_receipt_attachments", false, &snap.PurchaseReceiptAttachments},
		{operational, "band_transactions", false, &snap.BandTransactions},
		{operational, "band_transaction_attachments", false, &snap.BandTransactionAttachments},
		{operational, "sales", true, &snap.Sales},
		{operational, "sale_events", false, &snap.SaleEvents},
		{operational, "sale_event_state", false, &snap.SaleEventState},
		{operational, "audit_log", false, &snap.OperationalAuditLog},
		{operational, "sync_events", false, &snap.SyncEvents},
	} {
		if err = load(item.db, item.table, item.required, item.target); err != nil {
			return nil, err
		}
	}
	return &snap, nil
}

type userPlan struct {
	legacyID int64
	row      row
	role     models.Role
}

func buildUserPlans(rows []row) ([]userPlan, []string, error) {
	plans := make([]userPlan, 0, len(rows))
	warnings := []string{}
	activeCount := 0
	hasActiveAdmin := false
	seen := map[string]bool{}

	for _, r := range rows {
		rawRole := strings.ToLower(strings.TrimSpace(stringValue(r, "role")))
		if rawRole == string(models.RoleSupportAdmin) || rawRole == string(models.RoleSystemAdmin) {
			warnings = append(warnings, fmt.Sprintf(
				"legacy platform user %q with role %q was skipped; platform accounts are instance-scoped and must be created separately through bootstrap",
				stringValue(r, "username"), rawRole,
			))
			continue
		}

		username := stringValue(r, "username")
		normalized, err := auth.NormalizeUsername(username)
		if err != nil {
			return nil, nil, fmt.Errorf("legacy user %q: %w", username, err)
		}
		key := strings.ToLower(normalized)
		if seen[key] {
			return nil, nil, fmt.Errorf("legacy users contain duplicate username %q", normalized)
		}
		seen[key] = true

		role, warning := mapLegacyRole(r)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		active := boolValue(r, "is_active", true)
		if active {
			activeCount++
			if role == models.RoleBandAdmin {
				hasActiveAdmin = true
			}
		}
		plans = append(plans, userPlan{legacyID: int64Value(r, "id"), row: r, role: role})
	}
	if activeCount == 0 {
		return nil, nil, errors.New("legacy import: users database contains no active users")
	}

	if !hasActiveAdmin {
		best := -1
		for idx, plan := range plans {
			if !boolValue(plan.row, "is_active", true) {
				continue
			}
			if best == -1 || plan.role.Level() > plans[best].role.Level() {
				best = idx
			}
		}
		if best < 0 {
			return nil, nil, errors.New("legacy import: could not choose an active band administrator")
		}
		warnings = append(warnings,
			fmt.Sprintf("legacy data had no active band_admin; %q will be promoted to band_admin", stringValue(plans[best].row, "username")))
		plans[best].role = models.RoleBandAdmin
	}

	return plans, warnings, nil
}

func preflight(ctx context.Context, snap *snapshot, opts Options) (*Report, error) {
	report := &Report{
		DryRun:   opts.DryRun,
		BandName: strings.TrimSpace(opts.BandName),
		BandSlug: strings.TrimSpace(opts.BandSlug),
		Counts:   map[string]int{},
	}
	counts := map[string]int{
		"users":                        len(snap.Users),
		"articles":                     len(snap.Articles),
		"option_groups":                len(snap.OptionGroups),
		"option_values":                len(snap.OptionValues),
		"variants":                     len(snap.Variants),
		"variant_photos":               len(snap.VariantPhotos),
		"slideshow_extra_photos":       len(snap.SlideshowExtraPhotos),
		"slideshow_settings":           len(snap.SlideshowSettings),
		"payment_qr_settings":          len(snap.PaymentQRSettings),
		"payment_qr_intents_skipped":   len(snap.PaymentQRIntents),
		"purchases":                    len(snap.Purchases),
		"purchase_receipt_attachments": len(snap.PurchaseReceiptAttachments),
		"sales":                        len(snap.Sales),
		"sale_events":                  len(snap.SaleEvents),
		"band_transactions":            len(snap.BandTransactions),
		"band_transaction_attachments": len(snap.BandTransactionAttachments),
		"admin_messages":               len(snap.AdminMessages),
		"audit_log":                    len(snap.UserAuditLog) + len(snap.OperationalAuditLog),
		"sync_events_skipped":          len(snap.SyncEvents),
	}
	for key, count := range counts {
		report.Counts[key] = count
	}

	if len(snap.Users) == 0 {
		return nil, errors.New("legacy import: users database contains no users")
	}
	userPlans, userWarnings, err := buildUserPlans(snap.Users)
	if err != nil {
		return nil, err
	}
	report.Counts["users"] = len(userPlans)
	report.Counts["platform_users_skipped"] = len(snap.Users) - len(userPlans)
	report.Warnings = append(report.Warnings, userWarnings...)
	report.Warnings = append(report.Warnings,
		"legacy password hashes, MFA secrets, sessions, setup/reset challenges and SMTP credentials are intentionally not migrated; every imported band user receives a new one-time setup code")

	articles := idSet(snap.Articles)
	groups := idSet(snap.OptionGroups)
	values := idSet(snap.OptionValues)
	variants := idSet(snap.Variants)
	events := idSet(snap.SaleEvents)
	transactions := idSet(snap.BandTransactions)

	for _, r := range snap.OptionGroups {
		if !articles[int64Value(r, "article_id")] {
			return nil, fmt.Errorf("legacy option group %d references missing article %d", int64Value(r, "id"), int64Value(r, "article_id"))
		}
	}
	for _, r := range snap.OptionValues {
		if !groups[int64Value(r, "option_group_id")] {
			return nil, fmt.Errorf("legacy option value %d references missing group %d", int64Value(r, "id"), int64Value(r, "option_group_id"))
		}
	}
	for _, r := range snap.Variants {
		if !articles[int64Value(r, "article_id")] {
			return nil, fmt.Errorf("legacy variant %d references missing article %d", int64Value(r, "id"), int64Value(r, "article_id"))
		}
		optionIDs, err := jsonInt64Slice(r, "option_value_ids_json")
		if err != nil {
			return nil, fmt.Errorf("legacy variant %d: %w", int64Value(r, "id"), err)
		}
		for _, optionID := range optionIDs {
			if !values[optionID] {
				return nil, fmt.Errorf("legacy variant %d references missing option value %d", int64Value(r, "id"), optionID)
			}
		}
	}
	for _, table := range []struct {
		name string
		rows []row
	}{
		{"purchases", snap.Purchases},
		{"sales", snap.Sales},
	} {
		for _, r := range table.rows {
			if !variants[int64Value(r, "variant_id")] {
				return nil, fmt.Errorf("legacy %s row %d references missing variant %d", table.name, int64Value(r, "id"), int64Value(r, "variant_id"))
			}
		}
	}
	for _, r := range snap.VariantPhotos {
		if !variants[int64Value(r, "variant_id")] {
			return nil, fmt.Errorf("legacy variant photo %d references missing variant %d", int64Value(r, "id"), int64Value(r, "variant_id"))
		}
	}
	purchaseReceipts := map[string]bool{}
	for _, r := range snap.Purchases {
		purchaseReceipts[stringValue(r, "receipt_id")] = true
	}
	for _, r := range snap.PurchaseReceiptAttachments {
		if !purchaseReceipts[stringValue(r, "receipt_id")] {
			return nil, fmt.Errorf("legacy purchase receipt attachment %d references missing receipt %q", int64Value(r, "id"), stringValue(r, "receipt_id"))
		}
	}
	for _, r := range snap.SaleEventState {
		if !events[int64Value(r, "event_id")] {
			return nil, fmt.Errorf("legacy sale_event_state references missing event %d", int64Value(r, "event_id"))
		}
	}
	for _, r := range snap.BandTransactionAttachments {
		if !transactions[int64Value(r, "transaction_id")] {
			return nil, fmt.Errorf("legacy band transaction attachment %d references missing transaction %d", int64Value(r, "id"), int64Value(r, "transaction_id"))
		}
	}

	fileRefs := []struct {
		category string
		path     string
	}{}
	addFile := func(category, path string) {
		if strings.TrimSpace(path) != "" {
			fileRefs = append(fileRefs, struct {
				category string
				path     string
			}{category, path})
		}
	}
	for _, r := range snap.VariantPhotos {
		addFile(storage.CategoryVariantPhoto, stringValue(r, "file_path"))
	}
	for _, r := range snap.SlideshowExtraPhotos {
		addFile(storage.CategoryVariantPhoto, stringValue(r, "file_path"))
	}
	for _, r := range snap.Purchases {
		addFile(storage.CategoryInvoice, stringValue(r, "invoice_file_path"))
	}
	for _, r := range snap.PurchaseReceiptAttachments {
		addFile(storage.CategoryInvoice, stringValue(r, "file_path"))
	}
	for _, r := range snap.BandTransactionAttachments {
		addFile(storage.CategoryInvoice, stringValue(r, "file_path"))
	}
	for _, ref := range fileRefs {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		inspected, err := inspectLegacyFile(opts.LegacyStorage, ref.category, ref.path)
		if err != nil {
			return nil, err
		}
		report.Files = append(report.Files, *inspected)
	}

	if len(snap.PaymentQRIntents) > 0 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("%d expired/ephemeral payment QR intents are intentionally not migrated", len(snap.PaymentQRIntents)))
	}
	if len(snap.SyncEvents) > 0 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("%d offline sync idempotency records are intentionally not migrated", len(snap.SyncEvents)))
	}
	for _, r := range snap.AdminMessages {
		if int64Value(r, "assigned_to_user_id") != 0 || stringValue(r, "assigned_to_username") != "" {
			report.Warnings = append(report.Warnings,
				"legacy support-message assignments keep only their assignee name; old platform-account IDs cannot safely be reused in the new control plane")
			break
		}
	}
	return report, nil
}

func idSet(rows []row) map[int64]bool {
	result := make(map[int64]bool, len(rows))
	for _, r := range rows {
		result[int64Value(r, "id")] = true
	}
	return result
}

type destinationImporter struct {
	tx         *gorm.DB
	auth       *auth.Service
	store      storage.Store
	bandID     int64
	legacyRoot string
	storedKeys *[]string
	report     *Report

	userIDs        map[int64]int64
	articleIDs     map[int64]int64
	groupIDs       map[int64]int64
	valueIDs       map[int64]int64
	variantIDs     map[int64]int64
	eventIDs       map[int64]int64
	purchaseIDs    map[int64]int64
	saleIDs        map[int64]int64
	transactionIDs map[int64]int64

	credentials []Credential
}

func (i *destinationImporter) scoped(ctx context.Context) *gorm.DB {
	return i.tx.WithContext(tenant.WithBand(ctx, i.bandID))
}

func (i *destinationImporter) importAll(ctx context.Context, snap *snapshot) error {
	if err := i.importUsers(ctx, snap.Users); err != nil {
		return err
	}
	for _, step := range []func(context.Context) error{
		func(ctx context.Context) error { return i.importArticles(ctx, snap.Articles) },
		func(ctx context.Context) error { return i.importOptionGroups(ctx, snap.OptionGroups) },
		func(ctx context.Context) error { return i.importOptionValues(ctx, snap.OptionValues) },
		func(ctx context.Context) error { return i.importVariants(ctx, snap.Variants) },
		func(ctx context.Context) error { return i.importVariantPhotos(ctx, snap.VariantPhotos) },
		func(ctx context.Context) error { return i.importSlideshowExtraPhotos(ctx, snap.SlideshowExtraPhotos) },
		func(ctx context.Context) error { return i.importSlideshowSettings(ctx, snap.SlideshowSettings) },
		func(ctx context.Context) error { return i.importPaymentQRSettings(ctx, snap.PaymentQRSettings) },
		func(ctx context.Context) error { return i.importSaleEvents(ctx, snap.SaleEvents) },
		func(ctx context.Context) error { return i.importSaleEventState(ctx, snap.SaleEventState) },
		func(ctx context.Context) error { return i.importPurchases(ctx, snap.Purchases) },
		func(ctx context.Context) error {
			return i.importPurchaseReceiptAttachments(ctx, snap.PurchaseReceiptAttachments)
		},
		func(ctx context.Context) error { return i.importSales(ctx, snap.Sales) },
		func(ctx context.Context) error { return i.importBandTransactions(ctx, snap.BandTransactions) },
		func(ctx context.Context) error {
			return i.importBandTransactionAttachments(ctx, snap.BandTransactionAttachments)
		},
		func(ctx context.Context) error { return i.importAdminMessages(ctx, snap.AdminMessages) },
		func(ctx context.Context) error {
			return i.importAuditLogs(ctx, snap.UserAuditLog, snap.OperationalAuditLog)
		},
	} {
		if err := step(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (i *destinationImporter) importUsers(ctx context.Context, rows []row) error {
	plans, _, err := buildUserPlans(rows)
	if err != nil {
		return err
	}

	bandID := i.bandID
	for _, plan := range plans {
		username := stringValue(plan.row, "username")
		user, code, err := i.auth.CreateUserInTransaction(ctx, i.tx, &bandID, username, plan.role)
		if err != nil {
			return fmt.Errorf("import user %q: %w", username, err)
		}
		createdAt, err := timeValue(plan.row, "created_at")
		if err != nil {
			return fmt.Errorf("import user %q created_at: %w", username, err)
		}
		updates := map[string]any{
			"is_active":           boolValue(plan.row, "is_active", true),
			"contact_email":       stringValue(plan.row, "contact_email"),
			"ui_theme":            defaultString(stringValue(plan.row, "ui_theme"), "aurora"),
			"ui_language":         defaultString(stringValue(plan.row, "ui_language"), "de"),
			"show_variant_photos": boolValue(plan.row, "show_variant_photos", false),
		}
		if !createdAt.IsZero() {
			updates["created_at"] = createdAt
			updates["updated_at"] = createdAt
		}
		if err := i.tx.WithContext(tenant.WithCrossBandAccess(ctx)).Model(user).Updates(updates).Error; err != nil {
			return fmt.Errorf("update imported user %q: %w", username, err)
		}
		i.userIDs[plan.legacyID] = user.ID
		i.credentials = append(i.credentials, Credential{
			Username:  username,
			Role:      plan.role,
			SetupCode: code,
			IsActive:  boolValue(plan.row, "is_active", true),
		})
	}
	return nil
}

func mapLegacyRole(r row) (models.Role, string) {
	raw := strings.ToLower(strings.TrimSpace(stringValue(r, "role")))
	isAdmin := boolValue(r, "is_admin", false)
	switch raw {
	case string(models.RoleSeller):
		if isAdmin {
			return models.RoleBandAdmin, ""
		}
		return models.RoleSeller, ""
	case string(models.RoleMember):
		if isAdmin {
			return models.RoleBandAdmin, ""
		}
		return models.RoleMember, ""
	case string(models.RoleManager):
		if isAdmin {
			return models.RoleBandAdmin, ""
		}
		return models.RoleManager, ""
	case string(models.RoleBandAdmin), "admin":
		return models.RoleBandAdmin, ""
	case string(models.RoleSupportAdmin), string(models.RoleSystemAdmin):
		return models.RoleBandAdmin,
			fmt.Sprintf("legacy platform role %q for %q was reduced to band_admin", raw, stringValue(r, "username"))
	case "":
		if isAdmin {
			return models.RoleBandAdmin, ""
		}
		return models.RoleSeller, fmt.Sprintf("legacy user %q had no role; mapped to seller", stringValue(r, "username"))
	default:
		if isAdmin {
			return models.RoleBandAdmin,
				fmt.Sprintf("unknown legacy role %q for %q was mapped to band_admin because is_admin is set", raw, stringValue(r, "username"))
		}
		return models.RoleSeller,
			fmt.Sprintf("unknown legacy role %q for %q was mapped to seller", raw, stringValue(r, "username"))
	}
}

func (i *destinationImporter) importArticles(ctx context.Context, rows []row) error {
	for _, r := range rows {
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		updated, err := timeValue(r, "updated_at")
		if err != nil {
			return err
		}
		if updated.IsZero() {
			updated = created
		}
		record := models.Article{
			Tenant:                models.Tenant{BandID: i.bandID},
			Name:                  stringValue(r, "name"),
			DefaultSalePriceCents: int64Value(r, "default_sale_price_cents"),
			IsOffered:             boolValue(r, "is_offered", true),
			IsActive:              boolValue(r, "is_active", true),
			Timestamps:            models.Timestamps{CreatedAt: created, UpdatedAt: updated},
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return fmt.Errorf("import article %d: %w", int64Value(r, "id"), err)
		}
		i.articleIDs[int64Value(r, "id")] = record.ID
	}
	return nil
}

func (i *destinationImporter) importOptionGroups(ctx context.Context, rows []row) error {
	for _, r := range rows {
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		updated, err := timeValue(r, "updated_at")
		if err != nil {
			return err
		}
		if updated.IsZero() {
			updated = created
		}
		record := models.OptionGroup{
			Tenant:     models.Tenant{BandID: i.bandID},
			ArticleID:  i.articleIDs[int64Value(r, "article_id")],
			Name:       stringValue(r, "name"),
			Position:   intValue(r, "position"),
			IsActive:   boolValue(r, "is_active", true),
			Timestamps: models.Timestamps{CreatedAt: created, UpdatedAt: updated},
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return fmt.Errorf("import option group %d: %w", int64Value(r, "id"), err)
		}
		i.groupIDs[int64Value(r, "id")] = record.ID
	}
	return nil
}

func (i *destinationImporter) importOptionValues(ctx context.Context, rows []row) error {
	for _, r := range rows {
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		updated, err := timeValue(r, "updated_at")
		if err != nil {
			return err
		}
		if updated.IsZero() {
			updated = created
		}
		record := models.OptionValue{
			Tenant:        models.Tenant{BandID: i.bandID},
			OptionGroupID: i.groupIDs[int64Value(r, "option_group_id")],
			Value:         stringValue(r, "value"),
			Position:      intValue(r, "position"),
			IsActive:      boolValue(r, "is_active", true),
			Timestamps:    models.Timestamps{CreatedAt: created, UpdatedAt: updated},
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return fmt.Errorf("import option value %d: %w", int64Value(r, "id"), err)
		}
		i.valueIDs[int64Value(r, "id")] = record.ID
	}
	return nil
}

func (i *destinationImporter) importVariants(ctx context.Context, rows []row) error {
	for _, r := range rows {
		legacyValues, err := jsonInt64Slice(r, "option_value_ids_json")
		if err != nil {
			return err
		}
		newValues := make([]int64, len(legacyValues))
		for idx, oldID := range legacyValues {
			newValues[idx] = i.valueIDs[oldID]
		}
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		updated, err := timeValue(r, "updated_at")
		if err != nil {
			return err
		}
		if updated.IsZero() {
			updated = created
		}
		record := models.Variant{
			Tenant:         models.Tenant{BandID: i.bandID},
			ArticleID:      i.articleIDs[int64Value(r, "article_id")],
			OptionValueIDs: models.JSONInt64Slice(newValues),
			CombinationKey: combinationKey(newValues),
			SalePriceCents: int64Value(r, "sale_price_cents"),
			MinimumStock:   nullableInt(r, "minimum_stock"),
			IsOffered:      boolValue(r, "is_offered", true),
			NoReorder:      boolValue(r, "no_reorder", false),
			IsActive:       boolValue(r, "is_active", true),
			Timestamps:     models.Timestamps{CreatedAt: created, UpdatedAt: updated},
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return fmt.Errorf("import variant %d: %w", int64Value(r, "id"), err)
		}
		i.variantIDs[int64Value(r, "id")] = record.ID
	}
	return nil
}

func combinationKey(ids []int64) string {
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
	parts := make([]string, len(sorted))
	for idx, id := range sorted {
		parts[idx] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, "|")
}

func (i *destinationImporter) importVariantPhotos(ctx context.Context, rows []row) error {
	for _, r := range rows {
		obj, err := i.copyFile(ctx, storage.CategoryVariantPhoto, stringValue(r, "file_path"))
		if err != nil {
			return fmt.Errorf("import variant photo %d: %w", int64Value(r, "id"), err)
		}
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		record := models.VariantPhoto{
			Tenant:             models.Tenant{BandID: i.bandID},
			VariantID:          i.variantIDs[int64Value(r, "variant_id")],
			FilePath:           obj.Key,
			OriginalFilename:   defaultString(stringValue(r, "original_filename"), filepath.Base(stringValue(r, "file_path"))),
			Position:           intValue(r, "position"),
			IncludeInSlideshow: boolValue(r, "include_in_slideshow", true),
			ShowPrice:          boolValue(r, "show_price", true),
			SizeBytes:          obj.SizeBytes,
			CreatedAt:          created,
			Actor:              i.actor(r, "created_by", "created_by_username"),
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return err
		}
	}
	return nil
}

func (i *destinationImporter) importSlideshowExtraPhotos(ctx context.Context, rows []row) error {
	for _, r := range rows {
		obj, err := i.copyFile(ctx, storage.CategorySlideshow, stringValue(r, "file_path"))
		if err != nil {
			return fmt.Errorf("import slideshow photo %d: %w", int64Value(r, "id"), err)
		}
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		record := models.SlideshowExtraPhoto{
			Tenant:             models.Tenant{BandID: i.bandID},
			FilePath:           obj.Key,
			OriginalFilename:   defaultString(stringValue(r, "original_filename"), filepath.Base(stringValue(r, "file_path"))),
			Position:           intValue(r, "position"),
			IncludeInSlideshow: boolValue(r, "include_in_slideshow", true),
			ShowPrice:          boolValue(r, "show_price", true),
			SizeBytes:          obj.SizeBytes,
			CreatedAt:          created,
			Actor:              i.actor(r, "created_by", "created_by_username"),
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return err
		}
	}
	return nil
}

func (i *destinationImporter) importSlideshowSettings(ctx context.Context, rows []row) error {
	if len(rows) == 0 {
		return nil
	}
	r := rows[len(rows)-1]
	interval := intValue(r, "collage_interval")
	if interval <= 0 {
		interval = 8
	}
	record := models.SlideshowSettings{
		Tenant:            models.Tenant{BandID: i.bandID},
		CollageShowPrices: boolValue(r, "collage_show_prices", true),
		CollageInterval:   interval,
		CollageModes:      defaultString(stringValue(r, "collage_modes"), "scroll,reveal,filmstrip"),
		UpdatedAt:         time.Now().UTC(),
	}
	return i.scoped(ctx).Create(&record).Error
}

func (i *destinationImporter) importPaymentQRSettings(ctx context.Context, rows []row) error {
	if len(rows) == 0 {
		return nil
	}
	r := rows[len(rows)-1]
	updated, err := timeValue(r, "updated_at")
	if err != nil {
		return err
	}
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	record := models.PaymentQRSettings{
		Tenant:             models.Tenant{BandID: i.bandID},
		PayPalMeURL:        stringValue(r, "paypal_me_url"),
		BankAccountHolder:  stringValue(r, "bank_account_holder"),
		BankIBAN:           stringValue(r, "bank_iban"),
		BankBIC:            stringValue(r, "bank_bic"),
		BankRemittanceText: defaultString(stringValue(r, "bank_remittance_text"), "Merch-Kauf"),
		UpdatedAt:          updated,
		UpdatedByUserID:    i.mappedUserPtr(int64Value(r, "updated_by_user_id")),
		UpdatedByUsername:  stringValue(r, "updated_by_username"),
	}
	return i.scoped(ctx).Create(&record).Error
}

func (i *destinationImporter) importSaleEvents(ctx context.Context, rows []row) error {
	for _, r := range rows {
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		last, err := timeValue(r, "last_selected_at")
		if err != nil {
			return err
		}
		if last.IsZero() {
			last = created
		}
		record := models.SaleEvent{
			Tenant:         models.Tenant{BandID: i.bandID},
			Name:           stringValue(r, "name"),
			CreatedAt:      created,
			LastSelectedAt: last,
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return err
		}
		i.eventIDs[int64Value(r, "id")] = record.ID
	}
	return nil
}

func (i *destinationImporter) importSaleEventState(ctx context.Context, rows []row) error {
	if len(rows) == 0 {
		return nil
	}
	r := rows[len(rows)-1]
	updated, err := timeValue(r, "updated_at")
	if err != nil {
		return err
	}
	record := models.SaleEventState{
		Tenant:    models.Tenant{BandID: i.bandID},
		EventID:   i.eventIDs[int64Value(r, "event_id")],
		UpdatedAt: updated,
	}
	return i.scoped(ctx).Create(&record).Error
}

func (i *destinationImporter) importPurchases(ctx context.Context, rows []row) error {
	for _, r := range rows {
		purchasedOn, err := dateValue(r, "purchased_on")
		if err != nil {
			return fmt.Errorf("purchase %d date: %w", int64Value(r, "id"), err)
		}
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		record := models.Purchase{
			Tenant:             models.Tenant{BandID: i.bandID},
			ReceiptID:          stringValue(r, "receipt_id"),
			VariantID:          i.variantIDs[int64Value(r, "variant_id")],
			Quantity:           intValue(r, "quantity"),
			UnitCostCents:      int64Value(r, "unit_cost_cents"),
			PricesIncludeVAT:   true,
			VATRateBasisPoints: 1900,
			PurchasedOn:        purchasedOn,
			Supplier:           stringValue(r, "supplier"),
			InvoiceReference:   stringValue(r, "invoice_reference"),
			Comment:            stringValue(r, "comment"),
			IsCancelled:        false,
			CreatedAt:          created,
			UpdatedAt:          created,
			Actor:              i.actor(r, "created_by", "created_by_username"),
		}
		if path := stringValue(r, "invoice_file_path"); path != "" {
			obj, err := i.copyFile(ctx, storage.CategoryInvoice, path)
			if err != nil {
				return fmt.Errorf("purchase %d invoice: %w", int64Value(r, "id"), err)
			}
			record.InvoiceFilePath = obj.Key
			record.InvoiceOriginalFilename = filepath.Base(path)
			record.InvoiceSizeBytes = obj.SizeBytes
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return err
		}
		i.purchaseIDs[int64Value(r, "id")] = record.ID
	}
	return nil
}

func (i *destinationImporter) importPurchaseReceiptAttachments(ctx context.Context, rows []row) error {
	for _, r := range rows {
		obj, err := i.copyFile(ctx, storage.CategoryInvoice, stringValue(r, "file_path"))
		if err != nil {
			return err
		}
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		record := models.PurchaseReceiptAttachment{
			Tenant:           models.Tenant{BandID: i.bandID},
			ReceiptID:        stringValue(r, "receipt_id"),
			FilePath:         obj.Key,
			OriginalFilename: defaultString(stringValue(r, "original_filename"), filepath.Base(stringValue(r, "file_path"))),
			SizeBytes:        obj.SizeBytes,
			CreatedAt:        created,
			Actor:            i.actor(r, "created_by", "created_by_username"),
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return err
		}
	}
	return nil
}

func (i *destinationImporter) importSales(ctx context.Context, rows []row) error {
	for _, r := range rows {
		soldOn, err := dateValue(r, "sold_on")
		if err != nil {
			return fmt.Errorf("sale %d date: %w", int64Value(r, "id"), err)
		}
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		cancelledAt, err := timePtrValue(r, "cancelled_at")
		if err != nil {
			return err
		}
		var amountGiven *int64
		if value, ok := r["amount_given_cents"]; ok && value != nil && strings.TrimSpace(fmt.Sprint(value)) != "" {
			n := int64Value(r, "amount_given_cents")
			amountGiven = &n
		}
		record := models.Sale{
			Tenant:           models.Tenant{BandID: i.bandID},
			ReceiptID:        stringValue(r, "receipt_id"),
			VariantID:        i.variantIDs[int64Value(r, "variant_id")],
			Quantity:         intValue(r, "quantity"),
			UnitPriceCents:   int64Value(r, "unit_price_cents"),
			AmountDueCents:   int64Value(r, "amount_due_cents"),
			AmountGivenCents: amountGiven,
			DonationCents:    int64Value(r, "donation_cents"),
			PaymentMethod:    stringValue(r, "payment_method"),
			IsPaid:           boolValue(r, "is_paid", true),
			PaymentFollowUp:  boolValue(r, "payment_follow_up", false),
			IsReceived:       boolValue(r, "is_received", true),
			DeliveryStatus:   models.DeliveryStatus(defaultString(stringValue(r, "delivery_status"), string(models.DeliveryNotApplicable))),
			IsCancelled:      boolValue(r, "is_cancelled", false),
			CancelledAt:      cancelledAt,
			CustomerName:     stringValue(r, "customer_name"),
			CustomerAddress:  stringValue(r, "customer_address"),
			EventName:        stringValue(r, "event_name"),
			SoldBy:           stringValue(r, "sold_by"),
			Comment:          stringValue(r, "comment"),
			SoldOn:           soldOn,
			CreatedAt:        created,
			Actor:            i.actor(r, "created_by", "created_by_username"),
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return err
		}
		i.saleIDs[int64Value(r, "id")] = record.ID
	}
	return nil
}

func (i *destinationImporter) importBandTransactions(ctx context.Context, rows []row) error {
	for _, r := range rows {
		on, err := dateValue(r, "transaction_on")
		if err != nil {
			return err
		}
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		cancelledAt, err := timePtrValue(r, "cancelled_at")
		if err != nil {
			return err
		}
		record := models.BandTransaction{
			Tenant:              models.Tenant{BandID: i.bandID},
			TransactionType:     models.BandTransactionType(stringValue(r, "transaction_type")),
			TransactionOn:       on,
			Category:            stringValue(r, "category"),
			Description:         stringValue(r, "description"),
			AmountCents:         int64Value(r, "amount_cents"),
			IsSettled:           true,
			IsAsset:             false,
			SettledAt:           &created,
			SettledByUserID:     i.actorID(r, "created_by"),
			SettledByUsername:   stringValue(r, "created_by_username"),
			IsCancelled:         boolValue(r, "is_cancelled", false),
			CancelledAt:         cancelledAt,
			CancelledByUserID:   i.actorID(r, "cancelled_by_user_id"),
			CancelledByUsername: stringValue(r, "cancelled_by_username"),
			CreatedAt:           created,
			UpdatedAt:           created,
			Actor:               i.actor(r, "created_by", "created_by_username"),
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return err
		}
		i.transactionIDs[int64Value(r, "id")] = record.ID
	}
	return nil
}

func (i *destinationImporter) importBandTransactionAttachments(ctx context.Context, rows []row) error {
	for _, r := range rows {
		obj, err := i.copyFile(ctx, storage.CategoryBandDocument, stringValue(r, "file_path"))
		if err != nil {
			return err
		}
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		record := models.BandTransactionAttachment{
			Tenant:           models.Tenant{BandID: i.bandID},
			TransactionID:    i.transactionIDs[int64Value(r, "transaction_id")],
			FilePath:         obj.Key,
			OriginalFilename: defaultString(stringValue(r, "original_filename"), filepath.Base(stringValue(r, "file_path"))),
			SizeBytes:        obj.SizeBytes,
			CreatedAt:        created,
			Actor:            i.actor(r, "created_by", "created_by_username"),
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return err
		}
	}
	return nil
}

func (i *destinationImporter) importAdminMessages(ctx context.Context, rows []row) error {
	for _, r := range rows {
		created, err := timeValue(r, "created_at")
		if err != nil {
			return err
		}
		resolvedAt, err := timePtrValue(r, "resolved_at")
		if err != nil {
			return err
		}
		record := models.AdminMessage{
			Tenant:             models.Tenant{BandID: i.bandID},
			SenderUserID:       i.actorID(r, "sender_user_id"),
			SenderUsername:     stringValue(r, "sender_username"),
			SenderEmail:        stringValue(r, "sender_email"),
			MessageType:        models.AdminMessageType(defaultString(stringValue(r, "message_type"), string(models.AdminMessageQuestion))),
			Subject:            stringValue(r, "subject"),
			Body:               stringValue(r, "body"),
			AssignedToUsername: stringValue(r, "assigned_to_username"),
			IsResolved:         boolValue(r, "is_resolved", false),
			ResolvedAt:         resolvedAt,
			ResolvedByUserID:   i.actorID(r, "resolved_by_user_id"),
			ResolvedByUsername: stringValue(r, "resolved_by_username"),
			CreatedAt:          created,
		}
		if err := i.scoped(ctx).Create(&record).Error; err != nil {
			return err
		}
	}
	return nil
}

func (i *destinationImporter) importAuditLogs(ctx context.Context, groups ...[]row) error {
	for _, rows := range groups {
		for _, r := range rows {
			created, err := timeValue(r, "created_at")
			if err != nil {
				return err
			}
			legacyEntityID := int64Value(r, "entity_id")
			entityID := i.mapEntityID(stringValue(r, "entity_type"), legacyEntityID)
			details := jsonMapValue(r, "details_json")
			if legacyEntityID != 0 && entityID == nil {
				details["legacy_entity_id"] = legacyEntityID
			}
			record := models.AuditLog{
				BandID:     &i.bandID,
				UserID:     i.actorID(r, "user_id"),
				Username:   defaultString(stringValue(r, "user_username"), stringValue(r, "username")),
				Action:     stringValue(r, "action"),
				EntityType: stringValue(r, "entity_type"),
				EntityID:   entityID,
				Details:    details,
				CreatedAt:  created,
			}
			if err := i.tx.WithContext(tenant.WithCrossBandAccess(ctx)).Create(&record).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (i *destinationImporter) mapEntityID(entityType string, legacyID int64) *int64 {
	if legacyID == 0 {
		return nil
	}
	var mapped int64
	switch strings.ToLower(strings.TrimSpace(entityType)) {
	case "user", "users":
		mapped = i.userIDs[legacyID]
	case "article", "articles":
		mapped = i.articleIDs[legacyID]
	case "option_group", "option_groups":
		mapped = i.groupIDs[legacyID]
	case "option_value", "option_values":
		mapped = i.valueIDs[legacyID]
	case "variant", "variants":
		mapped = i.variantIDs[legacyID]
	case "purchase", "purchases":
		mapped = i.purchaseIDs[legacyID]
	case "sale", "sales":
		mapped = i.saleIDs[legacyID]
	case "band_transaction", "band_transactions":
		mapped = i.transactionIDs[legacyID]
	case "sale_event", "sale_events":
		mapped = i.eventIDs[legacyID]
	default:
		return nil
	}
	if mapped == 0 {
		return nil
	}
	return &mapped
}

func (i *destinationImporter) actor(r row, idKey, usernameKey string) models.Actor {
	return models.Actor{
		CreatedByUserID:   i.actorID(r, idKey),
		CreatedByUsername: stringValue(r, usernameKey),
	}
}

func (i *destinationImporter) actorID(r row, key string) *int64 {
	return i.mappedUserPtr(int64Value(r, key))
}

func (i *destinationImporter) mappedUserPtr(oldID int64) *int64 {
	if oldID == 0 {
		return nil
	}
	newID := i.userIDs[oldID]
	if newID == 0 {
		return nil
	}
	return &newID
}

func (i *destinationImporter) copyFile(ctx context.Context, category, storedPath string) (*storage.Object, error) {
	legacy, err := inspectLegacyFile(i.legacyRoot, legacyCategory(category), storedPath)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(legacy.SourcePath)
	if err != nil {
		return nil, err
	}
	obj, putErr := i.store.Put(ctx, i.bandID, category, legacy.MediaType, file)
	closeErr := file.Close()
	if putErr != nil {
		return nil, putErr
	}
	if closeErr != nil {
		_ = i.store.Delete(ctx, obj.Key)
		return nil, closeErr
	}
	*i.storedKeys = append(*i.storedKeys, obj.Key)

	reader, stored, err := i.store.Open(ctx, obj.Key)
	if err != nil {
		return nil, err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(hasher, reader)
	closeErr = reader.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if stored.SizeBytes != legacy.SizeBytes || hex.EncodeToString(hasher.Sum(nil)) != legacy.SHA256 {
		return nil, fmt.Errorf("SHA-256 verification failed after copying %q", storedPath)
	}
	return obj, nil
}

func legacyCategory(destination string) string {
	switch destination {
	case storage.CategorySlideshow:
		return storage.CategoryVariantPhoto
	case storage.CategoryBandDocument:
		return storage.CategoryInvoice
	default:
		return destination
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func Slugify(value string) string {
	replacer := strings.NewReplacer(
		"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
		"Ä", "ae", "Ö", "oe", "Ü", "ue",
	)
	value = strings.ToLower(replacer.Replace(strings.TrimSpace(value)))
	var out strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			lastDash = false
		default:
			if out.Len() > 0 && !lastDash {
				out.WriteByte('-')
				lastDash = true
			}
		}
	}
	result := strings.Trim(out.String(), "-")
	if len(result) > 64 {
		result = strings.Trim(result[:64], "-")
	}
	if len(result) < 2 {
		result = "imported-band"
	}
	return result
}

func writeCredentialsFile(path string, credentials []Credential) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("credentials file %q already exists; refusing to overwrite it", absolute)
		}
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		_ = os.Remove(absolute)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(absolute)
		return err
	}
	return file.Close()
}
