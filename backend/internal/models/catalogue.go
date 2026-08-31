package models

import "time"

// Article is a product such as "Geometry Shirt". Its options are free-form:
// a band can define Farbe and Größe, another Schnitt and Material.
//
// Two independent flags carry over from the original and must not be merged:
// IsActive means the article exists at all, IsOffered means it is part of the
// current sales assortment. Withdrawing an article from sale never touches its
// history, stock or future purchases.
type Article struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	Name                      string `gorm:"size:200;not null" json:"name"`
	DefaultSalePriceCents     int64  `gorm:"not null;default:0" json:"default_sale_price_cents"`
	DefaultPurchasePriceCents int64  `gorm:"not null;default:0" json:"default_purchase_price_cents"`

	IsOffered bool `gorm:"not null" json:"is_offered"`
	IsActive  bool `gorm:"not null" json:"is_active"`

	Timestamps

	OptionGroups []OptionGroup `gorm:"foreignKey:ArticleID" json:"option_groups,omitempty"`
	Variants     []Variant     `gorm:"foreignKey:ArticleID" json:"variants,omitempty"`
}

func (Article) TableName() string { return "articles" }

// OptionGroup is one option column of an article, for example "Farbe".
type OptionGroup struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	ArticleID int64  `gorm:"not null;index" json:"article_id"`
	Name      string `gorm:"size:120;not null" json:"name"`
	Position  int    `gorm:"not null;default:0" json:"position"`
	// Deleting an option is a deactivation, never a removal: historic receipts
	// must keep resolving their option names, including after a rename.
	IsActive bool `gorm:"not null" json:"is_active"`

	Timestamps

	Values []OptionValue `gorm:"foreignKey:OptionGroupID" json:"values,omitempty"`
}

func (OptionGroup) TableName() string { return "option_groups" }

// OptionValue is one selectable value of an option group, for example "Schwarz".
type OptionValue struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	OptionGroupID int64  `gorm:"not null;index" json:"option_group_id"`
	Value         string `gorm:"size:120;not null" json:"value"`
	Position      int    `gorm:"not null;default:0" json:"position"`
	IsActive      bool   `gorm:"not null" json:"is_active"`

	Timestamps
}

func (OptionValue) TableName() string { return "option_values" }

// Variant is one concrete combination of option values, for example
// "Geometry Shirt — Farbe: Schwarz · Größe: M".
type Variant struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	ArticleID int64 `gorm:"not null;index" json:"article_id"`
	// OptionValueIDs is the ordered combination; CombinationKey is the sorted
	// IDs joined by "|" and is what makes the variant unique within an article.
	OptionValueIDs JSONInt64Slice `gorm:"type:json;not null" json:"option_value_ids"`
	CombinationKey string         `gorm:"size:255;not null" json:"combination_key"`

	SalePriceCents            int64 `gorm:"not null;default:0" json:"sale_price_cents"`
	DefaultPurchasePriceCents int64 `gorm:"not null;default:0" json:"default_purchase_price_cents"`

	// MinimumStock nil means no warning is configured. An explicit 0 stays
	// meaningful: warn only once the variant is actually sold out.
	MinimumStock *int `json:"minimum_stock"`

	IsOffered bool `gorm:"not null" json:"is_offered"`
	NoReorder bool `gorm:"not null" json:"no_reorder"`
	IsActive  bool `gorm:"not null" json:"is_active"`

	Timestamps

	Photos []VariantPhoto `gorm:"foreignKey:VariantID" json:"photos,omitempty"`
}

func (Variant) TableName() string { return "variants" }

// VariantPhoto is a product picture. Image bytes live in the file store; only
// the opaque managed filename is kept here, so dumps stay small.
type VariantPhoto struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	VariantID        int64  `gorm:"not null;index" json:"variant_id"`
	FilePath         string `gorm:"size:255;uniqueIndex;not null" json:"-"`
	OriginalFilename string `gorm:"size:255;not null" json:"original_filename"`
	Position         int    `gorm:"not null;default:0" json:"position"`

	// Product photos are part of the shop-display slideshow unless a manager
	// opts one out; the price overlay is configurable per picture.
	IncludeInSlideshow bool `gorm:"not null" json:"include_in_slideshow"`
	ShowPrice          bool `gorm:"not null" json:"show_price"`

	SizeBytes int64     `gorm:"not null;default:0" json:"size_bytes"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	Actor
}

func (VariantPhoto) TableName() string { return "variant_photos" }

// SlideshowExtraPhoto is a display picture without a product relation, for
// example a price overview or band artwork.
type SlideshowExtraPhoto struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	FilePath         string `gorm:"size:255;uniqueIndex;not null" json:"-"`
	OriginalFilename string `gorm:"size:255;not null" json:"original_filename"`
	Position         int    `gorm:"not null;default:0" json:"position"`

	IncludeInSlideshow bool `gorm:"not null" json:"include_in_slideshow"`
	ShowPrice          bool `gorm:"not null" json:"show_price"`

	SizeBytes int64     `gorm:"not null;default:0" json:"size_bytes"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	Actor
}

func (SlideshowExtraPhoto) TableName() string { return "slideshow_extra_photos" }

// SlideshowSettings is one row per band. A missing row resolves to the safe
// default of showing prices.
type SlideshowSettings struct {
	ID int64 `gorm:"primaryKey" json:"id"`
	Tenant

	CollageShowPrices bool      `gorm:"not null" json:"collage_show_prices"`
	UpdatedAt         time.Time `gorm:"not null" json:"updated_at"`
}

func (SlideshowSettings) TableName() string { return "slideshow_settings" }
