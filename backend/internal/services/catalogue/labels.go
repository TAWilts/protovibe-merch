package catalogue

import (
	"context"
	"sort"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

// Label describes a variant in human terms.
type Label struct {
	ArticleName  string `json:"article_name"`
	VariantLabel string `json:"variant_label"`
	// OptionValues is the compact, group-name-free representation used where
	// space is scarce, for example "Schwarz/M" in an EPC transfer reference.
	OptionValues []string `json:"-"`
	// OptionPositions preserves the configured value order (for example
	// S, M, L, XL). It is intentionally not exposed; consumers use it only
	// when their default order must follow the catalogue rather than the
	// alphabetically rendered label.
	OptionPositions []int `json:"-"`
}

// VariantLabels renders "Farbe: Schwarz · Größe: M" for every variant of the
// scoped band.
//
// The labels come from the live catalogue rather than from a snapshot stored
// on the booking, which is what makes renaming an option apply retroactively
// to old receipts — the behaviour the band explicitly asked for.
//
// Retired options are included on purpose: a receipt from last season must
// stay readable after its size was withdrawn.
func (s *Service) VariantLabels(ctx context.Context) (map[int64]Label, error) {
	type variantRow struct {
		ID             int64
		ArticleName    string
		OptionValueIDs models.JSONInt64Slice
	}
	var variants []variantRow
	err := s.db.WithContext(ctx).Model(&models.Variant{}).
		Select("variants.id, variants.option_value_ids, articles.name AS article_name").
		Joins("JOIN articles ON articles.id = variants.article_id").
		Scan(&variants).Error
	if err != nil {
		return nil, err
	}
	if len(variants) == 0 {
		return map[int64]Label{}, nil
	}

	type valueRow struct {
		ID        int64
		Value     string
		GroupName string
		GroupPos  int
		ValuePos  int
	}
	var values []valueRow
	err = s.db.WithContext(ctx).Model(&models.OptionValue{}).
		Select(`option_values.id, option_values.value, option_values.position AS value_pos,
			option_groups.name AS group_name, option_groups.position AS group_pos`).
		Joins("JOIN option_groups ON option_groups.id = option_values.option_group_id").
		Scan(&values).Error
	if err != nil {
		return nil, err
	}

	byID := make(map[int64]valueRow, len(values))
	for _, value := range values {
		byID[value.ID] = value
	}

	labels := make(map[int64]Label, len(variants))
	for _, variant := range variants {
		parts := make([]valueRow, 0, len(variant.OptionValueIDs))
		for _, id := range variant.OptionValueIDs {
			if value, ok := byID[id]; ok {
				parts = append(parts, value)
			}
		}
		// Shown in the order the band arranged the columns, not in ID order.
		sort.SliceStable(parts, func(i, j int) bool { return parts[i].GroupPos < parts[j].GroupPos })

		text := ""
		for i, part := range parts {
			if i > 0 {
				text += " · "
			}
			text += part.GroupName + ": " + part.Value
		}
		positions := make([]int, 0, len(parts)*2)
		optionValues := make([]string, 0, len(parts))
		for _, part := range parts {
			positions = append(positions, part.GroupPos, part.ValuePos)
			optionValues = append(optionValues, part.Value)
		}
		labels[variant.ID] = Label{
			ArticleName: variant.ArticleName, VariantLabel: text,
			OptionValues: optionValues, OptionPositions: positions,
		}
	}
	return labels, nil
}
