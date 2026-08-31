// Package export produces the CSV and ZIP downloads.
//
// The format is deliberately unchanged from the original: UTF-8 with a byte
// order mark and semicolons as separators, because that is what opens cleanly
// in a German Excel without an import dialog. Amounts use a decimal comma for
// the same reason.
package export

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"sort"

	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
	"github.com/tawilts/protovibe-merch/backend/internal/services/money"
)

// Kind names one export sheet. The values are the German file names the
// original used, so a band's existing filing habits still work.
type Kind string

const (
	KindArticles  Kind = "artikel"
	KindSales     Kind = "verkaeufe"
	KindPurchases Kind = "einkaeufe"
	KindInventory Kind = "bestand"
)

// AllKinds is the set bundled into the ZIP.
var AllKinds = []Kind{KindArticles, KindSales, KindPurchases, KindInventory}

// Valid reports whether a requested export exists.
func (k Kind) Valid() bool {
	for _, known := range AllKinds {
		if known == k {
			return true
		}
	}
	return false
}

// deliveryStatusLabels mirror DELIVERY_STATUS_LABELS in the original.
var deliveryStatusLabels = map[models.DeliveryStatus]string{
	models.DeliveryPending:       "Noch nicht versendet",
	models.DeliveryShipped:       "Versendet",
	models.DeliveryReceived:      "Erhalten",
	models.DeliveryNotApplicable: "Nicht relevant",
}

// Sheet is one rendered export.
type Sheet struct {
	Name   string
	Header []string
	Rows   [][]string
}

// Service builds exports straight from the database rather than from a
// rendered table, so a filtered view can never silently truncate an export.
type Service struct {
	db        *gorm.DB
	catalogue *catalogue.Service
}

// NewService builds the export service.
func NewService(database *gorm.DB) *Service {
	return &Service{db: database, catalogue: catalogue.NewService(database)}
}

// yesNo renders a boolean the way the original's exports do.
func yesNo(value bool) string {
	if value {
		return "ja"
	}
	return "nein"
}

// Build renders one sheet.
func (s *Service) Build(ctx context.Context, kind Kind) (*Sheet, error) {
	switch kind {
	case KindArticles:
		return s.articleSheet(ctx)
	case KindSales:
		return s.salesSheet(ctx)
	case KindPurchases:
		return s.purchaseSheet(ctx)
	case KindInventory:
		return s.inventorySheet(ctx)
	default:
		return nil, fmt.Errorf("export: unknown kind %q", kind)
	}
}

// WriteCSV renders a sheet as UTF-8 CSV with a BOM and semicolon separators.
func WriteCSV(w io.Writer, sheet *Sheet) error {
	// The byte order mark is what makes Excel read the file as UTF-8 instead
	// of the local code page, which is the difference between "Größe" and
	// "GrÃ¶ÃŸe" in the band's spreadsheet.
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}

	writer := csv.NewWriter(w)
	writer.Comma = ';'

	if err := writer.Write(sheet.Header); err != nil {
		return err
	}
	if err := writer.WriteAll(sheet.Rows); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}

// WriteZIP bundles every sheet into one archive.
func (s *Service) WriteZIP(ctx context.Context, w io.Writer) error {
	archive := zip.NewWriter(w)

	for _, kind := range AllKinds {
		sheet, err := s.Build(ctx, kind)
		if err != nil {
			return err
		}
		entry, err := archive.Create(sheet.Name + ".csv")
		if err != nil {
			return err
		}
		var buffer bytes.Buffer
		if err := WriteCSV(&buffer, sheet); err != nil {
			return err
		}
		if _, err := entry.Write(buffer.Bytes()); err != nil {
			return err
		}
	}
	return archive.Close()
}

// variantContext is everything the exports need to describe a variant.
type variantContext struct {
	ArticleID                 int64
	ArticleName               string
	ArticleIsOffered          bool
	OptionText                string
	SalePriceCents            int64
	DefaultPurchasePriceCents int64
	MinimumStock              *int
	IsOffered                 bool
	NoReorder                 bool
	IsActive                  bool
}

// variantContexts resolves every variant with its article and option labels.
//
// It does not reuse catalogue.VariantLabels because the exports also need the
// prices, thresholds and assortment flags of each variant, and fetching those
// separately would double the number of queries for no benefit.
func (s *Service) variantContexts(ctx context.Context) (map[int64]variantContext, []int64, error) {
	type variantRow struct {
		ID                        int64
		ArticleID                 int64
		ArticleName               string
		ArticleIsOffered          bool
		OptionValueIDs            models.JSONInt64Slice
		SalePriceCents            int64
		DefaultPurchasePriceCents int64
		MinimumStock              *int
		IsOffered                 bool
		NoReorder                 bool
		IsActive                  bool
	}

	var variants []variantRow
	err := s.db.WithContext(ctx).Model(&models.Variant{}).
		Select(`variants.id, variants.article_id, variants.option_value_ids,
			variants.sale_price_cents, variants.default_purchase_price_cents,
			variants.minimum_stock, variants.is_offered, variants.no_reorder, variants.is_active,
			articles.name AS article_name, articles.is_offered AS article_is_offered`).
		Joins("JOIN articles ON articles.id = variants.article_id").
		Order("articles.name, variants.id").
		Scan(&variants).Error
	if err != nil {
		return nil, nil, err
	}

	type valueRow struct {
		ID        int64
		Value     string
		GroupName string
		GroupPos  int
	}
	var values []valueRow
	err = s.db.WithContext(ctx).Model(&models.OptionValue{}).
		Select(`option_values.id, option_values.value,
			option_groups.name AS group_name, option_groups.position AS group_pos`).
		Joins("JOIN option_groups ON option_groups.id = option_values.option_group_id").
		Scan(&values).Error
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[int64]valueRow, len(values))
	for _, value := range values {
		byID[value.ID] = value
	}

	contexts := make(map[int64]variantContext, len(variants))
	order := make([]int64, 0, len(variants))

	for _, variant := range variants {
		parts := make([]valueRow, 0, len(variant.OptionValueIDs))
		for _, id := range variant.OptionValueIDs {
			if value, ok := byID[id]; ok {
				parts = append(parts, value)
			}
		}
		sort.SliceStable(parts, func(i, j int) bool { return parts[i].GroupPos < parts[j].GroupPos })

		text := ""
		for i, part := range parts {
			if i > 0 {
				text += " · "
			}
			text += part.GroupName + ": " + part.Value
		}

		contexts[variant.ID] = variantContext{
			ArticleID:                 variant.ArticleID,
			ArticleName:               variant.ArticleName,
			ArticleIsOffered:          variant.ArticleIsOffered,
			OptionText:                text,
			SalePriceCents:            variant.SalePriceCents,
			DefaultPurchasePriceCents: variant.DefaultPurchasePriceCents,
			MinimumStock:              variant.MinimumStock,
			IsOffered:                 variant.IsOffered,
			NoReorder:                 variant.NoReorder,
			IsActive:                  variant.IsActive,
		}
		order = append(order, variant.ID)
	}
	return contexts, order, nil
}

func minimumStockText(minimum *int) string {
	if minimum == nil {
		return ""
	}
	return fmt.Sprintf("%d", *minimum)
}

func (s *Service) articleSheet(ctx context.Context) (*Sheet, error) {
	contexts, order, err := s.variantContexts(ctx)
	if err != nil {
		return nil, err
	}
	stock, err := s.catalogue.StockMap(ctx)
	if err != nil {
		return nil, err
	}

	rows := make([][]string, 0, len(order))
	for _, variantID := range order {
		entry := contexts[variantID]
		position := stock[variantID]

		status := "aktiv"
		if !entry.IsActive {
			status = "inaktiv"
		}
		rows = append(rows, []string{
			fmt.Sprintf("%d", entry.ArticleID),
			entry.ArticleName,
			fmt.Sprintf("%d", variantID),
			entry.OptionText,
			fmt.Sprintf("%d", position.OnHand),
			minimumStockText(entry.MinimumStock),
			yesNo(catalogue.IsAtOrBelowMinimum(position.OnHand, entry.MinimumStock)),
			money.FormatCSV(entry.SalePriceCents),
			money.FormatCSV(entry.DefaultPurchasePriceCents),
			yesNo(!entry.NoReorder),
			yesNo(entry.ArticleIsOffered && entry.IsOffered),
			status,
		})
	}

	return &Sheet{
		Name: string(KindArticles),
		Header: []string{
			"Artikel-ID", "Artikel", "Varianten-ID", "Optionen", "Bestand", "Mindestbestand",
			"Mindestbestandswarnung", "Verkaufspreis", "Standard-Einkaufspreis",
			"Nachbestellen", "Angeboten", "Status",
		},
		Rows: rows,
	}, nil
}

func (s *Service) salesSheet(ctx context.Context) (*Sheet, error) {
	contexts, _, err := s.variantContexts(ctx)
	if err != nil {
		return nil, err
	}

	var sales []models.Sale
	if err := s.db.WithContext(ctx).Order("sold_on, id").Find(&sales).Error; err != nil {
		return nil, err
	}

	rows := make([][]string, 0, len(sales))
	for _, sale := range sales {
		entry := contexts[sale.VariantID]
		given := ""
		if sale.AmountGivenCents != nil {
			given = money.FormatCSV(*sale.AmountGivenCents)
		}
		rows = append(rows, []string{
			sale.ReceiptID,
			sale.SoldOn.String(),
			entry.ArticleName,
			entry.OptionText,
			fmt.Sprintf("%d", sale.Quantity),
			money.FormatCSV(sale.UnitPriceCents),
			money.FormatCSV(sale.AmountDueCents),
			given,
			money.FormatCSV(sale.DonationCents),
			sale.PaymentMethod,
			yesNo(sale.IsPaid),
			yesNo(sale.IsReceived),
			deliveryStatusLabels[sale.DeliveryStatus],
			yesNo(sale.IsCancelled),
			sale.CustomerName,
			sale.CustomerAddress,
			sale.EventName,
			sale.SoldBy,
			sale.Comment,
		})
	}

	return &Sheet{
		Name: string(KindSales),
		Header: []string{
			"Beleg-ID", "Datum", "Artikel", "Optionen", "Stück", "Preis/Stück", "Betrag",
			"Gegeben", "Spende", "Bezahlart", "Bezahlt", "Artikel erhalten", "Versandstatus",
			"Storniert", "Kundenname", "Adresse", "Veranstaltung", "Verkauft von", "Kommentar",
		},
		Rows: rows,
	}, nil
}

func (s *Service) purchaseSheet(ctx context.Context) (*Sheet, error) {
	contexts, _, err := s.variantContexts(ctx)
	if err != nil {
		return nil, err
	}

	var purchases []models.Purchase
	if err := s.db.WithContext(ctx).Order("purchased_on, id").Find(&purchases).Error; err != nil {
		return nil, err
	}

	rows := make([][]string, 0, len(purchases))
	for _, purchase := range purchases {
		entry := contexts[purchase.VariantID]
		rows = append(rows, []string{
			purchase.ReceiptID,
			purchase.PurchasedOn.String(),
			entry.ArticleName,
			entry.OptionText,
			fmt.Sprintf("%d", purchase.Quantity),
			money.FormatCSV(purchase.UnitCostCents),
			money.FormatCSV(int64(purchase.Quantity) * purchase.UnitCostCents),
			purchase.Supplier,
			purchase.InvoiceReference,
			purchase.Comment,
		})
	}

	return &Sheet{
		Name: string(KindPurchases),
		Header: []string{
			"Beleg-ID", "Datum", "Artikel", "Optionen", "Stück", "Preis/Stück", "Gesamt",
			"Lieferant", "Rechnung", "Kommentar",
		},
		Rows: rows,
	}, nil
}

func (s *Service) inventorySheet(ctx context.Context) (*Sheet, error) {
	contexts, order, err := s.variantContexts(ctx)
	if err != nil {
		return nil, err
	}
	stock, err := s.catalogue.StockMap(ctx)
	if err != nil {
		return nil, err
	}

	rows := make([][]string, 0, len(order))
	for _, variantID := range order {
		entry := contexts[variantID]
		position := stock[variantID]
		rows = append(rows, []string{
			entry.ArticleName,
			entry.OptionText,
			fmt.Sprintf("%d", position.Purchased),
			fmt.Sprintf("%d", position.Sold),
			fmt.Sprintf("%d", position.OnHand),
			minimumStockText(entry.MinimumStock),
			yesNo(catalogue.IsAtOrBelowMinimum(position.OnHand, entry.MinimumStock)),
			yesNo(!entry.NoReorder),
			yesNo(entry.ArticleIsOffered && entry.IsOffered),
		})
	}

	return &Sheet{
		Name: string(KindInventory),
		Header: []string{
			"Artikel", "Optionen", "Gekauft", "Verkauft", "Aktueller Bestand", "Mindestbestand",
			"Mindestbestandswarnung", "Nachbestellen", "Angeboten",
		},
		Rows: rows,
	}, nil
}
