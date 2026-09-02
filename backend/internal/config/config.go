// Package config loads the entire runtime configuration from the environment.
//
// Unlike the Flask original there is no database unlock passphrase and no
// encryption envelope: the database is protected by MariaDB's own transport
// security plus host-level volume encryption, so every value needed to boot
// can come from the environment or a secret store.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved application configuration.
type Config struct {
	// Server
	Addr           string
	AppVersion     string
	Environment    string // "development" | "production"
	TrustedProxies []string

	// Database
	DatabaseDSN    string
	DBMaxOpenConns int
	DBMaxIdleConns int
	DBConnMaxLife  time.Duration

	// Security
	SecretKey           string
	SessionTTL          time.Duration
	SessionIdleTTL      time.Duration
	ProfileReauthWindow time.Duration
	AccountSetupCodeTTL time.Duration
	MFAIssuer           string
	CookieSecure        bool
	CookieDomain        string

	// Bootstrap of the very first platform account.
	BootstrapAdminUsername string
	BootstrapAdminPassword string

	// Storage for invoices, product photos and band attachments.
	StorageRoot string

	// Backups
	BackupRoot          string
	BackupRetentionDays int
	BackupCronFull      string // empty disables the scheduled full dump
	BackupCronPerBand   string
	MysqldumpPath       string

	// Outgoing mail (support inbox notifications, support-access requests).
	SMTPEnabled  bool
	SMTPHost     string
	SMTPPort     int
	SMTPSecurity string // "ssl" | "starttls" | "none"
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPTimeout  time.Duration

	// GitHub release check shown in the admin center.
	UpdateCheckRepository string
	UpdateCheckToken      string
	UpdateCheckTimeout    time.Duration
	UpdateCheckCacheTTL   time.Duration

	// Presentation defaults.
	DisplayTimezone           *time.Location
	PublicBaseURL             string
	PublicRegistrationEnabled bool
}

// placeholderValues are the literal strings shipped in .env.example. Booting
// with one of them in a security-relevant slot is always a misconfiguration.
var placeholderValues = map[string]bool{
	"replace-this-with-a-long-random-secret":   true,
	"replace-this-with-a-long-unique-password": true,
	"changeme": true,
}

// Load reads the configuration from the process environment and validates it.
func Load() (*Config, error) {
	c := &Config{
		Addr:        env("LISTEN_ADDR", ":8000"),
		AppVersion:  env("APP_VERSION", "v0.0.0"),
		Environment: env("ENVIRONMENT", "production"),

		DatabaseDSN:    os.Getenv("DATABASE_DSN"),
		DBMaxOpenConns: envInt("DB_MAX_OPEN_CONNS", 25),
		DBMaxIdleConns: envInt("DB_MAX_IDLE_CONNS", 5),
		DBConnMaxLife:  envDuration("DB_CONN_MAX_LIFETIME_SECONDS", 30*time.Minute),

		SecretKey:           os.Getenv("SECRET_KEY"),
		SessionTTL:          envDuration("SESSION_TTL_SECONDS", 30*24*time.Hour),
		SessionIdleTTL:      envDuration("SESSION_IDLE_TTL_SECONDS", 14*24*time.Hour),
		ProfileReauthWindow: envDuration("PROFILE_REAUTH_SECONDS", 600*time.Second),
		AccountSetupCodeTTL: time.Duration(envInt("ACCOUNT_SETUP_CODE_DAYS", 14)) * 24 * time.Hour,
		MFAIssuer:           env("MFA_ISSUER", "Protovibe Merch Manager"),
		CookieSecure:        envBool("COOKIE_SECURE", true),
		CookieDomain:        os.Getenv("COOKIE_DOMAIN"),

		BootstrapAdminUsername: env("BOOTSTRAP_ADMIN_USERNAME", "admin"),
		BootstrapAdminPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),

		StorageRoot: env("STORAGE_ROOT", "./data/storage"),

		BackupRoot:          env("BACKUP_ROOT", "./data/backups"),
		BackupRetentionDays: envInt("BACKUP_RETENTION_DAYS", 90),
		BackupCronFull:      env("BACKUP_CRON_FULL", "0 3 * * *"),
		BackupCronPerBand:   env("BACKUP_CRON_PER_BAND", "30 3 * * *"),
		MysqldumpPath:       env("MYSQLDUMP_PATH", "mariadb-dump"),

		SMTPEnabled:  envBool("EMAIL_NOTIFICATIONS_ENABLED", false),
		SMTPHost:     os.Getenv("SMTP_HOST"),
		SMTPPort:     envInt("SMTP_PORT", 465),
		SMTPSecurity: strings.ToLower(env("SMTP_SECURITY", "ssl")),
		SMTPUsername: os.Getenv("SMTP_USERNAME"),
		SMTPPassword: os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:     os.Getenv("SMTP_FROM"),
		SMTPTimeout:  envDuration("SMTP_TIMEOUT_SECONDS", 8*time.Second),

		UpdateCheckRepository: env("UPDATE_CHECK_REPOSITORY", "TAWilts/protovibe-merch"),
		UpdateCheckToken:      os.Getenv("UPDATE_CHECK_TOKEN"),
		UpdateCheckTimeout:    envDuration("UPDATE_CHECK_TIMEOUT_SECONDS", 3*time.Second),
		UpdateCheckCacheTTL:   envDuration("UPDATE_CHECK_CACHE_SECONDS", 6*time.Hour),

		PublicBaseURL:             strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8000"), "/"),
		PublicRegistrationEnabled: envBool("PUBLIC_REGISTRATION_ENABLED", false),
	}

	if raw := os.Getenv("TRUSTED_PROXIES"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				c.TrustedProxies = append(c.TrustedProxies, p)
			}
		}
	}

	loc, err := time.LoadLocation(env("DISPLAY_TIMEZONE", "Europe/Berlin"))
	if err != nil {
		return nil, fmt.Errorf("DISPLAY_TIMEZONE: %w", err)
	}
	c.DisplayTimezone = loc

	if c.SMTPFrom == "" {
		c.SMTPFrom = c.SMTPUsername
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// IsDevelopment reports whether relaxed local defaults apply. It never
// weakens authentication, only transport and logging expectations.
func (c *Config) IsDevelopment() bool { return c.Environment == "development" }

func (c *Config) validate() error {
	if c.DatabaseDSN == "" {
		return fmt.Errorf("DATABASE_DSN is required")
	}
	if len(c.SecretKey) < 32 {
		return fmt.Errorf("SECRET_KEY must be at least 32 characters")
	}
	if placeholderValues[c.SecretKey] {
		return fmt.Errorf("SECRET_KEY still holds the .env.example placeholder")
	}
	if c.BootstrapAdminPassword != "" {
		if placeholderValues[c.BootstrapAdminPassword] {
			return fmt.Errorf("BOOTSTRAP_ADMIN_PASSWORD still holds the .env.example placeholder")
		}
		if len(c.BootstrapAdminPassword) < 12 {
			return fmt.Errorf("BOOTSTRAP_ADMIN_PASSWORD must be at least 12 characters")
		}
	}
	switch c.SMTPSecurity {
	case "ssl", "starttls", "none":
	default:
		return fmt.Errorf("SMTP_SECURITY must be ssl, starttls or none")
	}
	if c.SMTPEnabled && c.SMTPHost == "" {
		return fmt.Errorf("SMTP_HOST is required when EMAIL_NOTIFICATIONS_ENABLED is true")
	}
	if _, err := url.Parse(c.PublicBaseURL); err != nil {
		return fmt.Errorf("PUBLIC_BASE_URL: %w", err)
	}
	if c.BackupRetentionDays < 1 {
		return fmt.Errorf("BACKUP_RETENTION_DAYS must be at least 1")
	}
	return nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

// envDuration reads a whole number of seconds, keeping the .env format
// identical to the original project's *_SECONDS variables.
func envDuration(key string, fallback time.Duration) time.Duration {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && v > 0 {
		return time.Duration(v) * time.Second
	}
	return fallback
}
