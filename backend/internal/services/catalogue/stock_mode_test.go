package catalogue_test

import (
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
)

func TestStockModeForFields(t *testing.T) {
	zero, ten := 0, 10
	tests := []struct {
		name      string
		offered   bool
		noReorder bool
		target    *int
		want      catalogue.StockMode
	}{
		{"stocked with target", true, false, &ten, catalogue.StockModeStocked},
		{"stocked without target", true, false, nil, catalogue.StockModeStocked},
		{"on demand", true, false, &zero, catalogue.StockModeOnDemand},
		{"clearance", true, true, &ten, catalogue.StockModeClearance},
		{"paused", false, false, &ten, catalogue.StockModePaused},
		{"discontinued", false, true, nil, catalogue.StockModeDiscontinued},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := catalogue.StockModeForFields(tt.offered, tt.noReorder, tt.target); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
