package rbac_test

import (
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/rbac"
)

// TestCapabilityMatrix pins the exact capability set of every role against the
// Flask original (_old/app.py:1458). A change here is a change in who may do
// what, so it must be deliberate rather than a side effect.
func TestCapabilityMatrix(t *testing.T) {
	cases := []struct {
		role models.Role
		want rbac.Capabilities
	}{
		{
			role: models.RoleSeller,
			want: rbac.Capabilities{
				CanAccessBandWorkflows: true,
			},
		},
		{
			role: models.RoleMember,
			want: rbac.Capabilities{
				CanAccessBandWorkflows:   true,
				CanAccessMemberWorkflows: true,
			},
		},
		{
			role: models.RoleManager,
			want: rbac.Capabilities{
				CanAccessBandWorkflows:   true,
				CanAccessMemberWorkflows: true,
				CanManagePurchases:       true,
				CanManageBandFinances:    true,
				CanManageArticles:        true,
				CanManageSlideshow:       true,
			},
		},
		{
			role: models.RoleBandAdmin,
			want: rbac.Capabilities{
				IsBandAdmin:              true,
				CanAccessBandWorkflows:   true,
				CanAccessMemberWorkflows: true,
				CanManagePurchases:       true,
				CanManageBandFinances:    true,
				CanManageArticles:        true,
				CanManageSlideshow:       true,
				CanAccessBandAdmin:       true,
				CanManageUpdates:         true,
			},
		},
		{
			role: models.RoleSupportAdmin,
			want: rbac.Capabilities{
				IsSupportAdmin:             true,
				IsPlatformStaff:            true,
				CanAccessSystemAdmin:       true,
				MFARequired:                true,
				SensitiveActionMFARequired: true,
			},
		},
		{
			role: models.RoleSystemAdmin,
			want: rbac.Capabilities{
				IsSystemAdmin:              true,
				IsPlatformStaff:            true,
				CanAccessSystemAdmin:       true,
				CanManagePlatformStaff:     true,
				MFARequired:                true,
				SensitiveActionMFARequired: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			want := tc.want
			want.Role = tc.role
			want.RoleLabel = rbac.Label(tc.role)

			got := rbac.For(&models.User{Role: tc.role})
			if got != want {
				t.Errorf("capabilities mismatch\n got: %+v\nwant: %+v", got, want)
			}
		})
	}
}

// TestPlatformStaffHaveNoBandAccess is the invariant the whole tenant boundary
// rests on: a support or system admin never gains band workflow rights from
// their role alone.
func TestPlatformStaffHaveNoBandAccess(t *testing.T) {
	for _, role := range []models.Role{models.RoleSupportAdmin, models.RoleSystemAdmin} {
		caps := rbac.For(&models.User{Role: role})
		if caps.CanAccessBandWorkflows || caps.CanAccessMemberWorkflows ||
			caps.CanManageArticles || caps.CanManagePurchases ||
			caps.CanManageBandFinances || caps.CanAccessBandAdmin {
			t.Errorf("%s has band capabilities: %+v", role, caps)
		}
	}
}

// TestEnablingMFAMakesSensitiveActionsRequireIt matches the original's
// sensitive_action_mfa_required, which is true as soon as a band user opts in.
func TestEnablingMFAMakesSensitiveActionsRequireIt(t *testing.T) {
	caps := rbac.For(&models.User{Role: models.RoleBandAdmin, MFAEnabled: true})
	if !caps.SensitiveActionMFARequired {
		t.Fatal("a band admin with MFA enabled must confirm sensitive actions with it")
	}
	if caps.MFARequired {
		t.Fatal("MFA stays optional for band roles")
	}
}

// TestRoleLevels pins the cumulative band ordering.
func TestRoleLevels(t *testing.T) {
	if !models.RoleManager.AtLeast(models.RoleMember) {
		t.Error("manager must satisfy member")
	}
	if models.RoleMember.AtLeast(models.RoleManager) {
		t.Error("member must not satisfy manager")
	}
	if models.RoleSystemAdmin.AtLeast(models.RoleSeller) {
		t.Error("platform roles must never satisfy a band role requirement")
	}
	if models.RoleBandAdmin.AtLeast(models.RoleSupportAdmin) {
		t.Error("a band admin must never satisfy a platform role requirement")
	}
}
