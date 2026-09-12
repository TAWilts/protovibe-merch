package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func TestBandFinanceAccountHoldersAreTenantScopedAndListed(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	manager := h.signInAs(band, models.RoleManager)
	holder := h.makeUser(&band.ID, models.RoleSeller, "ein-langes-passwort")
	otherBand := h.makeBand()
	otherHolder := h.makeUser(&otherBand.ID, models.RoleMember, "ein-langes-passwort")
	inactive := h.makeUser(&band.ID, models.RoleMember, "ein-langes-passwort")
	deleted := h.makeUser(&band.ID, models.RoleMember, "ein-langes-passwort")
	openHistorical := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "expense", "transaction_on": "2026-09-10",
		"category": "Equipment", "description": "Bleibt zugeordnet", "amount_cents": 800,
		"account_holder_user_id": inactive.ID, "is_settled": false,
	})
	if openHistorical.Status != http.StatusCreated {
		t.Fatalf("create historical holder entry: %d %v", openHistorical.Status, openHistorical.Body)
	}
	if err := h.db.WithContext(h.ctx()).Model(inactive).Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate user: %v", err)
	}
	preserved := h.do(http.MethodPatch, "/api/v1/band-finances/"+itoa(int64(openHistorical.Body["id"].(float64))), map[string]any{
		"transaction_type": "expense", "transaction_on": "2026-09-11",
		"category": "Equipment", "description": "Historische Zuordnung", "amount_cents": 900,
		"account_holder_user_id": inactive.ID,
	})
	if preserved.Status != http.StatusOK || preserved.Body["account_holder_user_id"] != float64(inactive.ID) || preserved.Body["account_holder_username"] != inactive.Username {
		t.Fatalf("editing must preserve a now-inactive holder: %d %v", preserved.Status, preserved.Body)
	}
	openDeleted := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "income", "transaction_on": "2026-09-10",
		"category": "Gage", "description": "Gelöschtes Konto", "amount_cents": 700,
		"account_holder_user_id": deleted.ID, "is_settled": false,
	})
	if openDeleted.Status != http.StatusCreated {
		t.Fatalf("create deleted holder entry: %d %v", openDeleted.Status, openDeleted.Body)
	}
	if err := h.db.WithContext(h.ctx()).Delete(deleted).Error; err != nil {
		t.Fatalf("delete historical holder: %v", err)
	}
	preservedDeleted := h.do(http.MethodPatch, "/api/v1/band-finances/"+itoa(int64(openDeleted.Body["id"].(float64))), map[string]any{
		"transaction_type": "income", "transaction_on": "2026-09-11",
		"category": "Gage", "description": "Historisches Konto", "amount_cents": 750,
		"account_holder_user_id": deleted.ID,
	})
	if preservedDeleted.Status != http.StatusOK || preservedDeleted.Body["account_holder_username"] != deleted.Username {
		t.Fatalf("editing must preserve a deleted holder snapshot: %d %v", preservedDeleted.Status, preservedDeleted.Body)
	}

	created := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "expense", "transaction_on": "2026-09-10",
		"category": "Equipment", "description": "Privat bezahlt", "amount_cents": 2500,
		"account_holder_user_id": holder.ID,
	})
	if created.Status != http.StatusCreated || created.Body["account_holder_user_id"] != float64(holder.ID) || created.Body["account_holder_username"] != holder.Username {
		t.Fatalf("valid holder was not stored: %d %v", created.Status, created.Body)
	}

	bandCash := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
		"transaction_type": "income", "transaction_on": "2026-09-10",
		"category": "Gage", "description": "Bandkasse", "amount_cents": 1000,
	})
	if bandCash.Status != http.StatusCreated || bandCash.Body["account_holder_user_id"] != nil || bandCash.Body["account_holder_username"] != "" {
		t.Fatalf("omitted holder must mean band cash: %d %v", bandCash.Status, bandCash.Body)
	}

	for name, userID := range map[string]int64{"other band": otherHolder.ID, "inactive": inactive.ID} {
		res := h.do(http.MethodPost, "/api/v1/band-finances", map[string]any{
			"transaction_type": "expense", "transaction_on": "2026-09-10",
			"category": "Sonstiges", "description": name, "amount_cents": 100,
			"account_holder_user_id": userID,
		})
		if res.Status != http.StatusBadRequest || res.Body["code"] != "invalid_account_holder" {
			t.Fatalf("%s holder must be rejected: %d %v", name, res.Status, res.Body)
		}
	}

	ledger := h.do(http.MethodGet, "/api/v1/band-finances", nil)
	if ledger.Status != http.StatusOK {
		t.Fatalf("ledger: %d %v", ledger.Status, ledger.Body)
	}
	listed := map[int64]bool{}
	for _, raw := range jsonList(ledger.Body, "account_holders") {
		listed[int64(jsonObject(raw)["id"].(float64))] = true
	}
	if !listed[manager.ID] || !listed[holder.ID] || listed[inactive.ID] || listed[deleted.ID] || listed[otherHolder.ID] {
		t.Fatalf("assignable holders must contain active users of this band only: %v", ledger.Body["account_holders"])
	}
}

func TestAccountHolderBalancesUseCurrentNameAndKeepDeletedHistory(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	alice := h.makeUser(&band.ID, models.RoleMember, "ein-langes-passwort")
	bob := h.makeUser(&band.ID, models.RoleMember, "ein-langes-passwort")

	book := func(kind, description string, amount int64, holderID *int64, settled bool) int64 {
		payload := map[string]any{
			"transaction_type": kind, "transaction_on": "2026-09-10",
			"category": "Sonstiges", "description": description, "amount_cents": amount,
			"is_settled": settled,
		}
		if holderID != nil {
			payload["account_holder_user_id"] = *holderID
		}
		res := h.do(http.MethodPost, "/api/v1/band-finances", payload)
		if res.Status != http.StatusCreated {
			t.Fatalf("book %s: %d %v", description, res.Status, res.Body)
		}
		return int64(res.Body["id"].(float64))
	}

	book("income", "Bandgage", 1000, nil, true)
	book("expense", "Alice Ausgabe", 3000, &alice.ID, true)
	book("income", "Alice Einnahme", 500, &alice.ID, true)
	book("income", "Bob Einnahme", 4000, &bob.ID, true)
	book("expense", "Offen", 900, &alice.ID, false)
	cancelledID := book("expense", "Storniert", 700, &alice.ID, true)
	if res := h.do(http.MethodPost, "/api/v1/band-finances/"+itoa(cancelledID)+"/cancel", nil); res.Status != http.StatusNoContent {
		t.Fatalf("cancel: %d %v", res.Status, res.Body)
	}

	if err := h.db.WithContext(h.ctx()).Model(alice).Update("username", "Alice Neu").Error; err != nil {
		t.Fatalf("rename alice: %v", err)
	}
	if err := h.db.WithContext(h.ctx()).Delete(bob).Error; err != nil {
		t.Fatalf("delete bob: %v", err)
	}

	balances := h.do(http.MethodGet, "/api/v1/balances?from=2026-09-01&to=2026-09-30", nil)
	if balances.Status != http.StatusOK {
		t.Fatalf("balances: %d %v", balances.Status, balances.Body)
	}
	totals := jsonList(balances.Body, "account_holder_totals")
	if len(totals) != 3 {
		t.Fatalf("expected band cash and two users: %v", totals)
	}
	bandTotal := jsonObject(totals[0])
	if bandTotal["account_holder_user_id"] != nil || bandTotal["income_cents"] != float64(1000) || bandTotal["difference_cents"] != float64(-1000) {
		t.Fatalf("band cash total is wrong or not first: %v", bandTotal)
	}
	aliceTotal := jsonObject(totals[1])
	if aliceTotal["account_holder_username"] != "Alice Neu" || aliceTotal["income_cents"] != float64(500) || aliceTotal["expense_cents"] != float64(3000) || aliceTotal["difference_cents"] != float64(2500) {
		t.Fatalf("alice total must use current name and exclude open/cancelled entries: %v", aliceTotal)
	}
	bobTotal := jsonObject(totals[2])
	if bobTotal["account_holder_username"] != bob.Username || bobTotal["difference_cents"] != float64(-4000) {
		t.Fatalf("deleted holder must retain the booking snapshot: %v", bobTotal)
	}
}

func TestRecurringBandFinanceCopiesAccountHolder(t *testing.T) {
	h := newHarness(t)
	band := h.makeBand()
	h.signInAs(band, models.RoleManager)
	holder := h.makeUser(&band.ID, models.RoleSeller, "ein-langes-passwort")

	created := h.do(http.MethodPost, "/api/v1/band-finances/recurring", map[string]any{
		"transaction_type": "expense", "start_on": time.Now().UTC().Format("2006-01-02"),
		"category": "Software / Abos", "description": "Tool-Abo", "amount_cents": 1200,
		"account_holder_user_id": holder.ID, "is_settled": true,
		"interval_value": 1, "interval_unit": "month",
	})
	if created.Status != http.StatusCreated || created.Body["account_holder_user_id"] != float64(holder.ID) {
		t.Fatalf("create recurring entry: %d %v", created.Status, created.Body)
	}

	ledger := h.do(http.MethodGet, "/api/v1/band-finances", nil)
	found := false
	for _, raw := range jsonList(ledger.Body, "entries") {
		entry := jsonObject(raw)
		if entry["description"] == "Tool-Abo" {
			found = entry["account_holder_user_id"] == float64(holder.ID) && entry["account_holder_username"] == holder.Username
		}
	}
	if !found {
		t.Fatalf("materialised entry did not copy holder: %v", ledger.Body["entries"])
	}
}
