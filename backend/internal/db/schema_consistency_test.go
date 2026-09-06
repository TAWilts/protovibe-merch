package db_test

import (
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// TestEveryBandTableIsGuarded is the structural safety net for the tenant
// design: every table that carries a NOT NULL band_id must have a model that
// embeds models.Tenant, otherwise the GORM callback would not guard it and a
// query could silently span all bands.
//
// It reads the live schema rather than the Go structs, so a table added in a
// migration without a matching model is caught too.
func TestEveryBandTableIsGuarded(t *testing.T) {
	gdb := openTestDB(t)

	type column struct {
		Table    string `gorm:"column:TABLE_NAME"`
		Nullable string `gorm:"column:IS_NULLABLE"`
	}
	var columns []column
	err := gdb.Raw(`
		SELECT TABLE_NAME, IS_NULLABLE
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND COLUMN_NAME = 'band_id'
	`).Scan(&columns).Error
	if err != nil {
		t.Fatalf("read information_schema: %v", err)
	}
	if len(columns) == 0 {
		t.Fatal("no band_id columns found; the schema did not migrate")
	}

	guarded := map[string]bool{}
	for _, model := range models.AllModels() {
		if !embedsTenant(reflect.TypeOf(model)) {
			continue
		}
		tabler, ok := model.(interface{ TableName() string })
		if !ok {
			t.Fatalf("%T embeds Tenant but has no TableName", model)
		}
		guarded[tabler.TableName()] = true
	}

	for _, col := range columns {
		if col.Nullable == "YES" || models.ControlPlaneTables[col.Table] {
			continue
		}
		if !guarded[col.Table] {
			t.Errorf("table %q has a NOT NULL band_id but no model embedding models.Tenant; "+
				"either embed it or add the table to models.ControlPlaneTables with a reason",
				col.Table)
		}
	}
}

// TestAllModelsAreRegistered catches a model that exists but was never added
// to AllModels, which would make the guard test above blind to it.
func TestAllModelsAreRegistered(t *testing.T) {
	gdb := openTestDB(t)

	var tables []string
	if err := gdb.Raw(`
		SELECT TABLE_NAME FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'
	`).Scan(&tables).Error; err != nil {
		t.Fatalf("read information_schema: %v", err)
	}

	registered := map[string]bool{}
	for _, model := range models.AllModels() {
		if tabler, ok := model.(interface{ TableName() string }); ok {
			registered[tabler.TableName()] = true
		}
	}

	for _, table := range tables {
		if table == "schema_migrations" {
			continue
		}
		if !registered[table] {
			t.Errorf("table %q has no entry in models.AllModels()", table)
		}
	}
}

// TestEveryModelFieldHasItsColumn catches a model whose field maps to a column
// name the migration does not use.
//
// GORM derives the column from the field name unless a `column:` tag says
// otherwise, so a migration that renames a column — as backup_runs.trigger_kind
// had to, because TRIGGER is reserved in MariaDB — breaks every INSERT on that
// table with "Unknown column", and only at runtime.
func TestEveryModelFieldHasItsColumn(t *testing.T) {
	gdb := openTestDB(t)

	type column struct {
		Table string `gorm:"column:TABLE_NAME"`
		Name  string `gorm:"column:COLUMN_NAME"`
	}
	var columns []column
	if err := gdb.Raw(`
		SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE()
	`).Scan(&columns).Error; err != nil {
		t.Fatalf("read information_schema: %v", err)
	}

	existing := map[string]map[string]bool{}
	for _, col := range columns {
		if existing[col.Table] == nil {
			existing[col.Table] = map[string]bool{}
		}
		existing[col.Table][col.Name] = true
	}

	for _, model := range models.AllModels() {
		stmt := &gorm.Statement{DB: gdb}
		if err := stmt.Parse(model); err != nil {
			t.Fatalf("parse %T: %v", model, err)
		}
		table := stmt.Schema.Table
		if existing[table] == nil {
			t.Errorf("model %T maps to table %q, which the migration does not create", model, table)
			continue
		}
		for _, field := range stmt.Schema.Fields {
			// Fields without a DBName are relations, not stored columns.
			if field.DBName == "" || field.IgnoreMigration {
				continue
			}
			if !existing[table][field.DBName] {
				t.Errorf("%T.%s maps to column %q, which %q does not have; "+
					"add a gorm:\"column:...\" tag or fix the migration",
					model, field.Name, field.DBName, table)
			}
		}
	}
}

func embedsTenant(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	tenantType := reflect.TypeOf(models.Tenant{})
	for i := 0; i < t.NumField(); i++ {
		if f := t.Field(i); f.Anonymous && f.Type == tenantType {
			return true
		}
	}
	return false
}

// TestBooleanFieldsCarryNoGormDefault guards against a subtle and expensive
// bug: GORM omits a field from an INSERT when its value is the zero value and
// the model declares a `default`. For a boolean that silently turns `false`
// into the column default — which once stored an unpaid sale as paid.
//
// The column defaults stay in the migration for rows written outside the ORM.
// The models must always send the value they hold.
func TestBooleanFieldsCarryNoGormDefault(t *testing.T) {
	boolType := reflect.TypeOf(true)

	var walk func(t reflect.Type, owner string)
	walk = func(structType reflect.Type, owner string) {
		for i := 0; i < structType.NumField(); i++ {
			field := structType.Field(i)
			if field.Anonymous && field.Type.Kind() == reflect.Struct {
				walk(field.Type, owner)
				continue
			}
			if field.Type != boolType {
				continue
			}
			if tag := field.Tag.Get("gorm"); strings.Contains(tag, "default:") {
				t.Errorf("%s.%s is a bool with a gorm default (%q); "+
					"remove it or a false value will silently fall back to the column default",
					owner, field.Name, tag)
			}
		}
	}

	for _, model := range models.AllModels() {
		modelType := reflect.TypeOf(model)
		for modelType.Kind() == reflect.Ptr {
			modelType = modelType.Elem()
		}
		walk(modelType, modelType.Name())
	}
}
