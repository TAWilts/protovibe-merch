package auth

import (
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func TestPlatformMFABypassedOnlyForPlatformAccounts(t *testing.T) {
	service := &Service{localDevMode: true}

	for _, role := range []models.Role{models.RoleSupportAdmin, models.RoleSystemAdmin} {
		if !service.PlatformMFABypassed(&models.User{Role: role}) {
			t.Fatalf("expected LOCAL_DEV_MODE to bypass MFA for %s", role)
		}
	}
	for _, role := range []models.Role{models.RoleSeller, models.RoleMember, models.RoleManager, models.RoleBandAdmin} {
		if service.PlatformMFABypassed(&models.User{Role: role}) {
			t.Fatalf("LOCAL_DEV_MODE must not bypass MFA for band role %s", role)
		}
	}

	service.localDevMode = false
	if service.PlatformMFABypassed(&models.User{Role: models.RoleSystemAdmin}) {
		t.Fatal("platform MFA must remain required when LOCAL_DEV_MODE is false")
	}
}
