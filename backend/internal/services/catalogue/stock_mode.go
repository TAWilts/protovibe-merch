package catalogue

// StockMode is the user-facing interpretation of the persisted assortment
// flags. It is derived on demand; there is deliberately no stock_mode column.
type StockMode string

const (
	StockModeStocked      StockMode = "stocked"
	StockModeOnDemand     StockMode = "on_demand"
	StockModeClearance    StockMode = "clearance"
	StockModePaused       StockMode = "paused"
	StockModeDiscontinued StockMode = "discontinued"
)

// StockModeForFields is the single backend mapping used by reports and
// exports. targetStock == 0 is distinct from a missing target.
func StockModeForFields(isOffered, noReorder bool, targetStock *int) StockMode {
	switch {
	case !isOffered && noReorder:
		return StockModeDiscontinued
	case !isOffered:
		return StockModePaused
	case noReorder:
		return StockModeClearance
	case targetStock != nil && *targetStock == 0:
		return StockModeOnDemand
	default:
		return StockModeStocked
	}
}

// StockModeGermanLabel keeps the German CSV vocabulary aligned across every
// server-rendered export.
func StockModeGermanLabel(mode StockMode) string {
	switch mode {
	case StockModeOnDemand:
		return "Auf Bestellung"
	case StockModeClearance:
		return "Abverkauf"
	case StockModePaused:
		return "Pausiert"
	case StockModeDiscontinued:
		return "Aus Sortiment"
	default:
		return "Lagerartikel"
	}
}
