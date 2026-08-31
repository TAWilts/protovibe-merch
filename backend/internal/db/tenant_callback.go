package db

import (
	"reflect"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/tenant"
)

// tenantColumn is the band discriminator present on every band-scoped table.
const tenantColumn = "band_id"

// RegisterTenantCallbacks installs the guards that make shared-database
// multi-tenancy safe.
//
// For any statement whose model embeds models.Tenant:
//   - a missing scope is an error, never an unfiltered query;
//   - reads, updates and deletes gain "band_id = ?" automatically;
//   - creates get band_id filled in, and a record carrying a different band is
//     rejected;
//   - writes under a read-only support grant are rejected.
//
// Statements on tables without models.Tenant (bands, users, sessions, audit
// log, platform settings) are untouched: those are guarded by handler-level
// authorisation instead, because platform staff legitimately work across bands
// there.
func RegisterTenantCallbacks(gdb *gorm.DB) error {
	cb := gdb.Callback()
	if err := cb.Query().Before("gorm:query").Register("tenant:query", scopeRead); err != nil {
		return err
	}
	if err := cb.Row().Before("gorm:row").Register("tenant:row", scopeRead); err != nil {
		return err
	}
	if err := cb.Update().Before("gorm:update").Register("tenant:update", scopeWrite); err != nil {
		return err
	}
	if err := cb.Delete().Before("gorm:delete").Register("tenant:delete", scopeWrite); err != nil {
		return err
	}
	return cb.Create().Before("gorm:create").Register("tenant:create", stampCreate)
}

// scopeRead adds the band filter to reads.
func scopeRead(db *gorm.DB) {
	scope, ok := tenantGuard(db)
	if !ok {
		return
	}
	addBandFilter(db, scope.BandID)
}

// scopeWrite adds the band filter to updates and deletes and enforces
// read-only support grants.
func scopeWrite(db *gorm.DB) {
	scope, ok := tenantGuard(db)
	if !ok {
		return
	}
	if scope.ReadOnly {
		_ = db.AddError(tenant.ErrReadOnlyScope)
		return
	}
	addBandFilter(db, scope.BandID)
}

// stampCreate fills in band_id on new records and rejects a record that was
// explicitly built for another band.
func stampCreate(db *gorm.DB) {
	scope, ok := tenantGuard(db)
	if !ok {
		return
	}
	if scope.ReadOnly {
		_ = db.AddError(tenant.ErrReadOnlyScope)
		return
	}

	field := db.Statement.Schema.LookUpField(tenantColumn)
	if field == nil {
		return
	}

	value := reflect.Indirect(reflect.ValueOf(db.Statement.Dest))
	switch value.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if !stampOne(db, field, value.Index(i), scope.BandID) {
				return
			}
		}
	case reflect.Struct:
		stampOne(db, field, value, scope.BandID)
	}
}

// stampOne sets or validates band_id on a single record. It returns false once
// an error has been recorded so the caller stops early.
func stampOne(db *gorm.DB, field *schema.Field, record reflect.Value, bandID int64) bool {
	current, isZero := field.ValueOf(db.Statement.Context, record)
	if !isZero {
		if existing, ok := toInt64(current); ok && existing != 0 && existing != bandID {
			_ = db.AddError(tenant.ErrScopeMismatch)
			return false
		}
	}
	if err := field.Set(db.Statement.Context, record, bandID); err != nil {
		_ = db.AddError(err)
		return false
	}
	return true
}

// tenantGuard reports whether the statement needs band scoping and returns the
// scope to apply. It records an error and returns false when a band-scoped
// table is touched without a usable scope.
func tenantGuard(db *gorm.DB) (tenant.Scope, bool) {
	if db.Error != nil || db.Statement == nil || db.Statement.Schema == nil {
		return tenant.Scope{}, false
	}
	if !isTenantScoped(db.Statement.Schema.ModelType) {
		return tenant.Scope{}, false
	}

	scope, ok := tenant.FromContext(db.Statement.Context)
	if !ok {
		_ = db.AddError(tenant.ErrMissingScope)
		return tenant.Scope{}, false
	}
	if scope.CrossBand {
		// An explicit platform-level operation; no filter, but writes still
		// have to stamp a band, which callers do themselves in that case.
		return tenant.Scope{}, false
	}
	if scope.BandID == 0 {
		_ = db.AddError(tenant.ErrMissingScope)
		return tenant.Scope{}, false
	}
	return scope, true
}

// isTenantScoped reports whether a model embeds models.Tenant.
func isTenantScoped(modelType reflect.Type) bool {
	for modelType != nil && (modelType.Kind() == reflect.Ptr || modelType.Kind() == reflect.Slice || modelType.Kind() == reflect.Array) {
		modelType = modelType.Elem()
	}
	if modelType == nil || modelType.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		if field.Anonymous && field.Type == tenantEmbedType {
			return true
		}
	}
	return false
}

var tenantEmbedType = reflect.TypeOf(models.Tenant{})

func addBandFilter(db *gorm.DB, bandID int64) {
	db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{
		clause.Eq{
			Column: clause.Column{Table: clause.CurrentTable, Name: tenantColumn},
			Value:  bandID,
		},
	}})
}

func toInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case *int64:
		if v == nil {
			return 0, false
		}
		return *v, true
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Int || rv.Kind() == reflect.Int64 {
		return rv.Int(), true
	}
	return 0, false
}
