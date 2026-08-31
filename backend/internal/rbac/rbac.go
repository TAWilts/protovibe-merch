// Package rbac holds the role model and the capability matrix.
//
// It is a direct port of user_capabilities() from the Flask original
// (_old/app.py:1458). The capabilities are shipped to the Vue frontend so it
// can render the right navigation, but they are only a display convenience:
// every route enforces the same rights independently on the server.
package rbac

import "github.com/tawilts/protovibe-merch/backend/internal/models"

// RoleLabels are the German names shown in the UI.
var RoleLabels = map[models.Role]string{
	models.RoleSeller:       "Seller",
	models.RoleMember:       "Member",
	models.RoleManager:      "Manager",
	models.RoleBandAdmin:    "Band-Admin",
	models.RoleSupportAdmin: "Support-Admin",
	models.RoleSystemAdmin:  "System-Admin",
}

// Label returns the display name of a role.
func Label(role models.Role) string {
	if label, ok := RoleLabels[role]; ok {
		return label
	}
	return "Unbekannte Rolle"
}

// Capabilities is what a signed-in account may do. It mirrors the original's
// capability dictionary field for field.
type Capabilities struct {
	Role      models.Role `json:"role"`
	RoleLabel string      `json:"role_label"`

	IsBandAdmin     bool `json:"is_band_admin"`
	IsSupportAdmin  bool `json:"is_support_admin"`
	IsSystemAdmin   bool `json:"is_system_admin"`
	IsPlatformStaff bool `json:"is_platform_staff"`

	CanAccessBandWorkflows   bool `json:"can_access_band_workflows"`
	CanAccessMemberWorkflows bool `json:"can_access_member_workflows"`
	CanManagePurchases       bool `json:"can_manage_purchases"`
	CanManageBandFinances    bool `json:"can_manage_band_finances"`
	CanManageArticles        bool `json:"can_manage_articles"`
	CanManageSlideshow       bool `json:"can_manage_slideshow"`
	CanAccessBandAdmin       bool `json:"can_access_band_administration"`
	CanAccessSystemAdmin     bool `json:"can_access_system_administration"`
	CanManagePlatformStaff   bool `json:"can_manage_platform_staff"`
	CanManageUpdates         bool `json:"can_manage_updates"`

	MFARequired                bool `json:"mfa_required"`
	MFAEnabled                 bool `json:"mfa_enabled"`
	SensitiveActionMFARequired bool `json:"sensitive_action_mfa_required"`
}

// For computes the capabilities of a user.
func For(user *models.User) Capabilities {
	if user == nil {
		return Capabilities{}
	}
	role := user.Role

	mfaRequired := role.IsPlatformRole()

	return Capabilities{
		Role:      role,
		RoleLabel: Label(role),

		IsBandAdmin:     role == models.RoleBandAdmin,
		IsSupportAdmin:  role == models.RoleSupportAdmin,
		IsSystemAdmin:   role == models.RoleSystemAdmin,
		IsPlatformStaff: role.IsPlatformRole(),

		CanAccessBandWorkflows:   role.IsBandRole(),
		CanAccessMemberWorkflows: role.AtLeast(models.RoleMember),
		CanManagePurchases:       role.AtLeast(models.RoleManager),
		CanManageBandFinances:    role.AtLeast(models.RoleManager),
		CanManageArticles:        role.AtLeast(models.RoleManager),
		CanManageSlideshow:       role.AtLeast(models.RoleManager),
		CanAccessBandAdmin:       role == models.RoleBandAdmin,
		CanAccessSystemAdmin:     role.IsPlatformRole(),
		CanManagePlatformStaff:   role == models.RoleSystemAdmin,
		// Deployment controls stay with the band admin; platform roles gain no
		// implicit access to a band's data or its update workflow.
		CanManageUpdates: role == models.RoleBandAdmin,

		MFARequired:                mfaRequired,
		MFAEnabled:                 user.MFAEnabled,
		SensitiveActionMFARequired: mfaRequired || user.MFAEnabled,
	}
}

// POSRestrictedPrefixes are the API paths blocked while a session runs in POS
// mode. The list is enforced on the server, exactly as in the original, so a
// tampered client cannot reach purchases or administration from a device left
// on a merch table.
var POSRestrictedPrefixes = []string{
	"/api/v1/articles",
	"/api/v1/purchases",
	"/api/v1/purchase-receipts",
	"/api/v1/band-finances",
	"/api/v1/balances",
	"/api/v1/band-admin",
	"/api/v1/platform",
	"/api/v1/updates",
	"/api/v1/exports",
	"/api/v1/imports",
}

// PlatformStaffAllowedPrefixes are the only paths platform accounts may use
// without a live support-access grant. Everything else is band data.
var PlatformStaffAllowedPrefixes = []string{
	"/api/v1/platform",
	"/api/v1/auth",
	"/api/v1/me",
	"/api/v1/profile",
	"/api/v1/mfa",
	"/api/v1/account",
	"/api/v1/session",
	// Deployment information rather than band data.
	"/api/v1/version",
	"/api/v1/announcement",
}
