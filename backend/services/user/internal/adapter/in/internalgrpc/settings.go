package internalgrpc

import (
	"context"
	"errors"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	application "github.com/v0hmly/marketmesh/services/user/internal/application/settings"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/settings"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SettingsUseCase interface {
	Get(context.Context, identity.Principal) (settings.Settings, error)
	Update(context.Context, identity.Principal, application.Command) (settings.Settings, error)
}

// EnableSettings is called before serving requests, independently of addresses.
func (h *Handler) EnableSettings(useCase SettingsUseCase) error {
	if useCase == nil {
		return errors.New("settings grpc: use case required")
	}
	h.settings = useCase
	return nil
}
func settingsError(e error) error {
	switch {
	case errors.Is(e, settings.ErrInvalid):
		return status.Error(codes.InvalidArgument, "invalid settings")
	case errors.Is(e, settings.ErrConflict):
		return status.Error(codes.Aborted, "settings version conflict")
	default:
		return mapError(e)
	}
}
func themeFromWire(theme userv1.Theme) (settings.Theme, error) {
	switch theme {
	case userv1.Theme_THEME_SYSTEM:
		return settings.System, nil
	case userv1.Theme_THEME_LIGHT:
		return settings.Light, nil
	case userv1.Theme_THEME_DARK:
		return settings.Dark, nil
	default:
		return "", settings.ErrInvalid
	}
}
func wireSettings(s settings.Settings) *userv1.AccountSettings {
	theme := userv1.Theme_THEME_UNSPECIFIED
	switch s.Theme {
	case settings.System:
		theme = userv1.Theme_THEME_SYSTEM
	case settings.Light:
		theme = userv1.Theme_THEME_LIGHT
	case settings.Dark:
		theme = userv1.Theme_THEME_DARK
	}
	return &userv1.AccountSettings{SubjectId: s.SubjectID.Bytes(), Version: s.Version, Theme: theme}
}
func (h *Handler) settingsPrincipal(ctx context.Context, method string) (identity.Principal, error) {
	noStore(ctx)
	if h.settings == nil {
		return identity.Principal{}, status.Error(codes.Unimplemented, "method unavailable")
	}
	return h.authenticate(ctx, method)
}
func (h *Handler) GetSettings(ctx context.Context, r *userv1.GetSettingsRequest) (*userv1.GetSettingsResponse, error) {
	p, e := h.settingsPrincipal(ctx, userv1.UserService_GetSettings_FullMethodName)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	s, e := h.settings.Get(ctx, p)
	if e != nil {
		return nil, settingsError(e)
	}
	return &userv1.GetSettingsResponse{Settings: wireSettings(s)}, nil
}
func (h *Handler) UpdateSettings(ctx context.Context, r *userv1.UpdateSettingsRequest) (*userv1.UpdateSettingsResponse, error) {
	p, e := h.settingsPrincipal(ctx, userv1.UserService_UpdateSettings_FullMethodName)
	if e != nil {
		return nil, e
	}
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	theme, e := themeFromWire(r.GetTheme())
	if e != nil {
		return nil, settingsError(e)
	}
	s, e := h.settings.Update(ctx, p, application.Command{Theme: theme, ExpectedVersion: r.GetExpectedVersion()})
	if e != nil {
		return nil, settingsError(e)
	}
	return &userv1.UpdateSettingsResponse{Settings: wireSettings(s)}, nil
}
