package user

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	qrcode "github.com/skip2/go-qrcode"
)

type TwoFactorStatus struct {
	Enabled           bool  `json:"enabled"`
	RecoveryCodesLeft int64 `json:"recovery_codes_left"`
	TOTPAvailable     bool  `json:"totp_available"` // false: the server has no security.encryption_key
}

type TOTPSetupResponse struct {
	Secret string `json:"secret"` // to type into the app
	URI    string `json:"uri"`    // otpauth:// URI
	// QR is the URI as a QR code: rows of '1' (dark) and '0', quiet zone
	// included, for the UI to draw (no image, so the CSP stays strict).
	QR []string `json:"qr"`
}

type CodeRequest struct {
	Code string `json:"code" validate:"required"`
}

type RecoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"` // shown once
}

type MessageResponse struct {
	Message string `json:"message"`
}

func setupTwoFactorRoutes(c Controller) {
	tags := []string{"Two-factor authentication"}
	fiberoapi.Get(c.FiberOapi, "/2fa", c.TwoFactorStatus, fiberoapi.OpenAPIOptions{
		Summary: "Two-factor authentication status", OperationID: "user.2fa.status", Tags: tags,
	})
	fiberoapi.Post(c.FiberOapi, "/2fa/totp", c.SetupTOTP, fiberoapi.OpenAPIOptions{
		Summary: "Start adding an authenticator app", OperationID: "user.2fa.totp.setup", Tags: tags,
		Description: "Sudo (see /auth/sudo). Returns a new secret to scan or type; two-factor authentication turns on once /2fa/totp/confirm gets a valid code.",
	})
	fiberoapi.Post(c.FiberOapi, "/2fa/totp/confirm", c.ConfirmTOTP, fiberoapi.OpenAPIOptions{
		Summary: "Turn on two-factor authentication", OperationID: "user.2fa.totp.confirm", Tags: tags,
		Description: "With the app's current code. Returns 10 single-use recovery codes, shown only this once.",
	})
	fiberoapi.Delete(c.FiberOapi, "/2fa", c.DisableTwoFactor, fiberoapi.OpenAPIOptions{
		Summary: "Turn off two-factor authentication", OperationID: "user.2fa.disable", Tags: tags, Description: "Sudo.",
	})
	fiberoapi.Post(c.FiberOapi, "/2fa/recovery-codes", c.RegenerateRecoveryCodes, fiberoapi.OpenAPIOptions{
		Summary: "New recovery codes", OperationID: "user.2fa.recovery_codes", Tags: tags,
		Description: "Sudo. Replaces the recovery codes; the old ones stop working.",
	})
}

func (c *Controller) TwoFactorStatus(ctx *fiber.Ctx, _ struct{}) (*TwoFactorStatus, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	st, err := c.TwoFactor.Status(auth.UserID)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &TwoFactorStatus{Enabled: st.Enabled, RecoveryCodesLeft: st.RecoveryCodesLeft, TOTPAvailable: st.TOTPAvailable}, nil
}

func (c *Controller) SetupTOTP(ctx *fiber.Ctx, _ struct{}) (*TOTPSetupResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	setup, err := c.TwoFactor.SetupTOTP(auth)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	qr, err := qrcode.New(setup.URI, qrcode.Medium)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	rows := make([]string, 0, 64)
	for _, row := range qr.Bitmap() {
		var b strings.Builder
		for _, dark := range row {
			if dark {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		rows = append(rows, b.String())
	}
	return &TOTPSetupResponse{Secret: setup.Secret, URI: setup.URI, QR: rows}, nil
}

func (c *Controller) ConfirmTOTP(ctx *fiber.Ctx, req CodeRequest) (*RecoveryCodesResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	codes, err := c.TwoFactor.ConfirmTOTP(auth.UserID, req.Code)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &RecoveryCodesResponse{RecoveryCodes: codes}, nil
}

func (c *Controller) DisableTwoFactor(ctx *fiber.Ctx, _ struct{}) (*MessageResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	if err := c.TwoFactor.Disable(auth); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "two-factor authentication turned off"}, nil
}

func (c *Controller) RegenerateRecoveryCodes(ctx *fiber.Ctx, _ struct{}) (*RecoveryCodesResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	codes, err := c.TwoFactor.RegenerateRecoveryCodes(auth)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &RecoveryCodesResponse{RecoveryCodes: codes}, nil
}
