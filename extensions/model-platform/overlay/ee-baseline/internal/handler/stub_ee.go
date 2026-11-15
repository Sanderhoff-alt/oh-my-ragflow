//go:build ee

package handler

import (
	"errors"
	"net/http"

	"ragflow/internal/common"
	"ragflow/internal/entity"

	"github.com/gin-gonic/gin"
)

// billingQuotaUser resolves the caller that the plan quota applies to.
func billingQuotaUser(c *gin.Context) (*entity.User, common.ErrorCode, error) {
	user, code, message := GetUser(c)
	if code != common.CodeSuccess {
		return nil, code, errors.New(message)
	}
	return user, code, nil
}

// billingEnforceApps admits every new application: the community edition has
// no plan quota.
func billingEnforceApps(_ *gin.Context, _ string) bool { return true }

// billingEnforceStorage admits every upload: the community edition has no
// storage quota.
func billingEnforceStorage(_ *gin.Context, _ string, _ int64) bool { return true }

// isOAuthLoginNoRoute reports the OAuth login URL with an empty channel: the
// Python API resolves it to a Werkzeug MethodNotAllowed, and the community
// edition has no OAuth routes, so HandleNoRoute has to reproduce that body.
func isOAuthLoginNoRoute(c *gin.Context) bool {
	return c.Request.Method == http.MethodGet && c.Request.URL.Path == "/api/v1/auth/login/"
}

// issueUserAccessToken mints the access token returned after a successful
// login.
func issueUserAccessToken(user *entity.User, secretKey string) (string, error) {
	if user == nil || user.AccessToken == nil {
		return "", errors.New("user access token is missing")
	}
	return common.DumpAccessToken(*user.AccessToken, "", secretKey)
}

// eeNotImplemented answers an enterprise-only endpoint in the community
// edition. The route stays registered so clients see "not implemented" rather
// than "not found".
func eeNotImplemented(c *gin.Context, feature string) {
	common.RespondError(c, common.CodeNotImplemented, feature+" not implemented")
}

func (h *SystemHandler) GetEnableAdmin(c *gin.Context) { eeNotImplemented(c, "EnableAdmin") }
func (h *SystemHandler) GetCores(c *gin.Context)       { eeNotImplemented(c, "Get cores") }
func (h *SystemHandler) SetCores(c *gin.Context)       { eeNotImplemented(c, "Set cores") }
func (h *SystemHandler) GetMemory(c *gin.Context)      { eeNotImplemented(c, "Get memory") }
func (h *SystemHandler) SetMemory(c *gin.Context)      { eeNotImplemented(c, "Set memory") }
func (h *SystemHandler) GetConcurrency(c *gin.Context) { eeNotImplemented(c, "Get concurrency") }
func (h *SystemHandler) SetConcurrency(c *gin.Context) { eeNotImplemented(c, "Set concurrency") }
func (h *SystemHandler) GetQuota(c *gin.Context)       { eeNotImplemented(c, "Get quota") }

func (h *UserHandler) IsAdmin(c *gin.Context) { eeNotImplemented(c, "IsAdmin") }
func (h *UserHandler) GetMeta(c *gin.Context) { eeNotImplemented(c, "GetMeta") }

// GetLoginChannelsDeprecated lists the OAuth authentication channels.
func (h *UserHandler) GetLoginChannelsDeprecated(c *gin.Context) {
	channels, code, err := h.userService.GetLoginChannels()
	if err != nil {
		common.RespondError(c, code, "Load channels failure, error: "+err.Error())
		return
	}

	common.RespondSuccessList(c, channels, "success")
}
func (h *ProviderHandler) CreateModelGroup(c *gin.Context) { eeNotImplemented(c, "CreateModelGroup") }
func (h *ProviderHandler) DeleteModelGroup(c *gin.Context) { eeNotImplemented(c, "DeleteModelGroup") }
func (h *ProviderHandler) ShowModelGroup(c *gin.Context)   { eeNotImplemented(c, "ShowModelGroup") }
func (h *ProviderHandler) AddModelToGroup(c *gin.Context)  { eeNotImplemented(c, "AddModelToGroup") }
func (h *ProviderHandler) RemoveModelFromGroup(c *gin.Context) {
	eeNotImplemented(c, "RemoveModelFromGroup")
}

func (h *StatsHandler) GetModelStats(c *gin.Context) { eeNotImplemented(c, "GetModelStats") }
func (h *StatsHandler) GetGroupStats(c *gin.Context) { eeNotImplemented(c, "GetGroupStats") }
