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
	CanCreateBandFinances    bool `json:"can_create_band_finances"`
	CanManageBandFinances    bool `json:"can_manage_band_finances"`
	CanManageArticles        bool `json:"can_manage_articles"`
	CanManageSlideshow       bool `json:"can_manage_slideshow"`
	CanUsePackingList        bool `json:"can_use_packing_list"`
	CanManagePackingList     bool `json:"can_manage_packing_list"`
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
		CanCreateBandFinances:    role.AtLeast(models.RoleMember),
		CanManageBandFinances:    role.AtLeast(models.RoleManager),
		CanManageArticles:        role.AtLeast(models.RoleManager),
		CanManageSlideshow:       role.AtLeast(models.RoleManager),
		CanUsePackingList:        role.IsBandRole(),
		CanManagePackingList:     role.AtLeast(models.RoleMember),
		CanAccessBandAdmin:       role == models.RoleBandAdmin,
		CanAccessSystemAdmin:     role.IsPlatformRole(),
		CanManagePlatformStaff:   role == models.RoleSystemAdmin,
		// Updates deploy the shared instance and therefore belong to the
		// system administrator, not to an individual band's administration.
		CanManageUpdates: role == models.RoleSystemAdmin,

		MFARequired:                mfaRequired,
		MFAEnabled:                 user.MFAEnabled,
		SensitiveActionMFARequired: mfaRequired || user.MFAEnabled,
	}
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
	// Anonymous onboarding remains public when a platform session cookie is
	// present; it never reads band-scoped data.
	"/api/v1/public",
	// Deployment information rather than band data.
	"/api/v1/version",
	"/api/v1/announcement",
}
