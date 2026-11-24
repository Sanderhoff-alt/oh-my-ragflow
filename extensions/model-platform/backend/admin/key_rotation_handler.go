package admin

import (
	"errors"
	"net/http"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/reconciler"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// keyRotationResponse is the outcome of a tenant key rotation. It never carries the key itself.
type keyRotationResponse struct {
	TenantID string `json:"tenant_id"`
	// KeyAlias is the gateway label of the new key.
	KeyAlias string `json:"key_alias"`
	// NewKeyFingerprint is the first 8 hex digits of the new key's sha256, enough to tell keys apart.
	NewKeyFingerprint string    `json:"new_key_fingerprint"`
	RotatedAt         time.Time `json:"rotated_at"`
	RevokedKeys       int       `json:"revoked_keys"`
	// OldKeysRevoked is false while a superseded key may still be valid at the gateway. The platform keeps
	// retrying in the background, and calling the endpoint again resumes the revocation immediately.
	OldKeysRevoked bool `json:"old_keys_revoked"`
}

// RotateTenantKeyHandler replaces the gateway virtual key of one tenant and revokes the old one.
// A nil rotator means the gateway is not configured.
func RotateTenantKeyHandler(rotator *reconciler.KeyRotator, customDB ...*gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db := resolveDB(customDB...)
		if db == nil || rotator == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"code": 503, "message": "key rotation is unavailable: gateway or database not configured"})
			return
		}

		operator := operatorID(c)
		if operator == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "an authenticated operator is required"})
			return
		}

		res, err := rotator.Rotate(c.Request.Context(), db, c.Param("tenant_id"), operator)
		if err != nil {
			status, message := rotationErrorResponse(err)
			c.JSON(status, gin.H{"code": status, "message": message})
			return
		}

		status := http.StatusOK
		if !res.Converged {
			status = http.StatusAccepted
		}
		c.JSON(status, gin.H{"code": status, "data": keyRotationResponse{
			TenantID:          res.TenantID,
			KeyAlias:          res.KeyAlias,
			NewKeyFingerprint: res.KeyHash[:8],
			RotatedAt:         res.RotatedAt,
			RevokedKeys:       res.RevokedKeys,
			OldKeysRevoked:    res.Converged,
		}})
	}
}

// operatorID identifies the authenticated administrator for the audit trail.
func operatorID(c *gin.Context) string {
	if id := c.GetString("user_id"); id != "" {
		return id
	}
	return c.GetString("email")
}

func rotationErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, reconciler.ErrInvalidTenantID):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, entity.ErrRecordNotFound):
		return http.StatusNotFound, "tenant is not managed by the model platform"
	case errors.Is(err, reconciler.ErrTenantNotReady), errors.Is(err, reconciler.ErrRotationConflict):
		return http.StatusConflict, err.Error()
	case errors.Is(err, reconciler.ErrGatewayFailure):
		return http.StatusBadGateway, "gateway unavailable: the current key was kept"
	default:
		return http.StatusInternalServerError, "failed to rotate the tenant key"
	}
}
