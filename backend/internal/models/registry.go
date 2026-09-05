package models

// AllModels lists every entity. It is the source for the schema-consistency
// test and for any future maintenance job that has to walk all tables.
//
// Adding a model here is not optional: the consistency test fails when a table
// with a NOT NULL band_id column has no entry, which is how a forgotten
// tenant guard is caught before it can leak data.
func AllModels() []any {
	return []any{
		// Control plane — deliberately not band-scoped.
		&Band{}, &User{}, &Session{}, &PendingAuth{}, &PasswordResetChallenge{},
		&SupportAccessGrant{}, &PlatformSettings{}, &BackupRun{}, &AuditLog{},
		&BandRegistrationRequest{}, &TelemetryDaily{},

		// Band data — every one of these embeds Tenant.
		&Article{}, &OptionGroup{}, &OptionValue{}, &Variant{},
		&VariantPhoto{}, &SlideshowExtraPhoto{}, &SlideshowSettings{},
		&Sale{}, &SaleEvent{}, &SaleEventState{}, &SyncEvent{},
		&PaymentQRSettings{}, &PaymentQRIntent{},
		&Purchase{}, &PurchaseReceiptAttachment{},
		&BandTransaction{}, &BandTransactionAttachment{},
		&RecurringBandTransaction{}, &RecurringBandTransactionRun{},
		&AdminMessage{},
	}
}

// ControlPlaneTables are the tables the tenant callback deliberately does not
// guard, because platform staff legitimately operate across bands there.
//
// Their protection is handler-level authorisation instead. Any table not
// listed here that carries a NOT NULL band_id must embed Tenant.
var ControlPlaneTables = map[string]bool{
	"bands":                      true,
	"users":                      true,
	"sessions":                   true,
	"pending_auth":               true,
	"support_access_grants":      true,
	"platform_settings":          true,
	"backup_runs":                true,
	"audit_log":                  true,
	"band_registration_requests": true,
	"telemetry_daily":             true,
	"schema_migrations":          true,
}
