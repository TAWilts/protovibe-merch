package bandfinance

import (
	"testing"
	"time"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func TestNextOccurrenceKeepsMonthEndAnchor(t *testing.T) {
	start := models.NewDate(2027, time.January, 31)
	feb := nextOccurrence(start, start, 1, models.RecurrenceMonth)
	if got := feb.String(); got != "2027-02-28" {
		t.Fatalf("February occurrence = %s", got)
	}
	mar := nextOccurrence(start, feb, 1, models.RecurrenceMonth)
	if got := mar.String(); got != "2027-03-31" {
		t.Fatalf("March occurrence drifted to %s", got)
	}
}

func TestNextOccurrenceClampsLeapDayYearly(t *testing.T) {
	start := models.NewDate(2028, time.February, 29)
	next := nextOccurrence(start, start, 1, models.RecurrenceYear)
	if got := next.String(); got != "2029-02-28" {
		t.Fatalf("yearly occurrence = %s", got)
	}
}
