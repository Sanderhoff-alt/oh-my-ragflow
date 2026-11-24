package guard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/entity"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	// PlatformInstanceName is the standard instance name assigned to platform-managed model instances.
	PlatformInstanceName = "platform"

	// ErrMsgManagedProviderForbidden is the exact error message returned when a tenant tries to tamper with platform-managed models.
	ErrMsgManagedProviderForbidden = "Platform-managed providers and models cannot be modified or deleted by tenants."

	// DefaultMaxMutationBodyBytes is the fallback maximum allowed request body size for provider mutations (1MB).
	DefaultMaxMutationBodyBytes = 1 << 20

	// MaxBatchInstanceTargets defines the maximum number of instances that can be targeted in a single batch mutation (100).
	MaxBatchInstanceTargets = 100
)

// ManagedProviderGuardMiddleware inspects incoming requests and blocks any tenant attempts
// to alter or delete platform-managed model providers and instances.
func ManagedProviderGuardMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		path := c.Request.URL.Path

		if !isProviderRoute(c, path) || !isProviderMutationMethod(method, path) {
			c.Next()
			return
		}

		if isPlatformPath(path) {
			blockManagedMutation(c)
			return
		}

		if !inspectMutationBody(c, path) {
			return
		}

		if isInstanceCreateRoute(c, path) {
			c.Next()
			return
		}

		if isInstanceRoute(c, path) {
			evalInstanceRoute(c)
			return
		}

		if prov := extractProviderTarget(c); prov != "" {
			if isManagedProviderTarget(prov, resolveRequestTenantID(c)) {
				blockManagedMutation(c)
				return
			}
		}

		c.Next()
	}
}

// isProviderRoute checks if the request targets registered provider endpoints rather than unrelated features (like dataset artifacts).
func isProviderRoute(c *gin.Context, path string) bool {
	if isExcludedNonProviderRoute(path) {
		return false
	}
	if fullPath := c.FullPath(); fullPath != "" {
		if isExcludedNonProviderRoute(fullPath) {
			return false
		}
		return matchesProviderTemplate(fullPath)
	}
	return matchesProviderTemplate(path)
}

// isExcludedNonProviderRoute checks if the path clearly belongs to non-provider features.
func isExcludedNonProviderRoute(p string) bool {
	lower := strings.ToLower(p)
	return strings.Contains(lower, "/dataset") || strings.Contains(lower, "/artifacts")
}

// matchesProviderTemplate asserts whether a path matches registered provider endpoint patterns.
func matchesProviderTemplate(p string) bool {
	lower := strings.ToLower(p)
	return strings.Contains(lower, "/providers") ||
		strings.Contains(lower, "/setting/model/provider") ||
		strings.Contains(lower, "/admin/model/provider")
}

// isProviderMutationMethod checks if the HTTP method represents a mutating operation or instance/model creation.
func isProviderMutationMethod(method, path string) bool {
	if method == http.MethodDelete || method == http.MethodPut || method == http.MethodPatch {
		return true
	}
	if method == http.MethodPost {
		return isInstanceCreatePath(path) || isModelCreatePath(path)
	}
	return false
}

// isInstanceCreatePath checks if the path targets instance creation.
func isInstanceCreatePath(path string) bool {
	trimmed := strings.TrimRight(path, "/")
	return strings.HasSuffix(trimmed, "/instances")
}

// isModelCreatePath checks if the path targets model creation under an instance.
func isModelCreatePath(path string) bool {
	trimmed := strings.TrimRight(path, "/")
	return strings.HasSuffix(trimmed, "/models")
}

// isInstanceCreateRoute checks if the request is an instance creation route.
func isInstanceCreateRoute(c *gin.Context, path string) bool {
	return c.Request.Method == http.MethodPost && isInstanceCreatePath(path)
}

// isInstanceRenameRoute checks if the request is an instance update/rename operation.
func isInstanceRenameRoute(c *gin.Context, path string) bool {
	if c.Request.Method != http.MethodPut {
		return false
	}
	if fullPath := c.FullPath(); fullPath != "" {
		trimmed := strings.Trim(fullPath, "/")
		return strings.HasSuffix(trimmed, "instances/:instance_id_or_name") || strings.HasSuffix(trimmed, "instances/:instance")
	}
	segs := stripAPIPrefix(strings.Split(strings.Trim(strings.ToLower(path), "/"), "/"))
	return len(segs) == 4 && segs[0] == "providers" && segs[2] == "instances"
}

// isModelRoute checks if the path targets model-level operations under an instance.
func isModelRoute(path string) bool {
	lower := strings.ToLower(path)
	if idx := strings.Index(lower, "?"); idx != -1 {
		lower = lower[:idx]
	}
	for _, seg := range strings.Split(strings.Trim(lower, "/"), "/") {
		if seg == "models" || seg == "model" {
			return true
		}
	}
	return false
}

// isInstancePayloadRoute checks if the request body carries instance creation or renaming payload.
func isInstancePayloadRoute(c *gin.Context, path string) bool {
	return isInstanceCreateRoute(c, path) || isInstanceRenameRoute(c, path)
}

// hasSoleManagedBodyTarget checks if an endpoint without route provider targets a managed resource via body.
func hasSoleManagedBodyTarget(c *gin.Context, rawMap map[string]interface{}) bool {
	return extractProviderTarget(c) == "" && hasManagedSingleBodyTarget(c, rawMap)
}

// getMaxMutationBodyBytes returns the non-multipart request body limit in bytes,
// matching upstream RAGFlow's RequestBodyLimit policy (defaulting to 1 MiB or configured via MAX_JSON_CONTENT_LENGTH).
func getMaxMutationBodyBytes() int64 {
	if limit := common.GetMaxJSONRequestBodyBytes(); limit > 0 {
		return limit
	}
	return DefaultMaxMutationBodyBytes
}

// decodeMutationJSON decodes the request body as JSON and enforces no trailing non-whitespace data.
func decodeMutationJSON(bodyBytes []byte) (map[string]interface{}, bool) {
	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.UseNumber()
	var rawMap map[string]interface{}
	if err := decoder.Decode(&rawMap); err != nil {
		return nil, false
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return rawMap, true
}

// inspectMutationBody inspects the request body for batch drops or mutation payloads.
func inspectMutationBody(c *gin.Context, path string) bool {
	bodyBytes, ok := readBoundedBody(c)
	if !ok || len(bodyBytes) == 0 {
		return ok
	}
	rawMap, valid := decodeMutationJSON(bodyBytes)
	if !valid {
		blockManagedMutation(c)
		return false
	}
	if isBatchDropRoute(c, path) {
		handleBatchDropBody(c, bodyBytes, rawMap)
		return false
	}
	if isInstancePayloadRoute(c, path) {
		return checkInstanceMutationPayload(c, rawMap)
	}
	if hasSoleManagedBodyTarget(c, rawMap) {
		blockManagedMutation(c)
		return false
	}
	return true
}

// checkInstanceMutationPayload verifies that instance mutation payloads do not rename or create instances with reserved platform names.
func checkInstanceMutationPayload(c *gin.Context, rawMap map[string]interface{}) bool {
	name, ok := resolveProposedInstanceName(rawMap)
	if !ok || isReservedPlatformName(name) {
		blockManagedMutation(c)
		return false
	}
	return true
}

// resolveProposedInstanceName extracts the proposed instance name, rejecting conflicting case-insensitive duplicate spellings.
func resolveProposedInstanceName(rawMap map[string]interface{}) (string, bool) {
	instVariants := collectInstanceNameVariants(rawMap)
	if len(instVariants) > 1 {
		return "", false
	}
	if len(instVariants) == 1 {
		return instVariants[0], true
	}
	nameVariants := collectVariantsByKey(rawMap, "name")
	if len(nameVariants) > 1 {
		return "", false
	}
	if len(nameVariants) == 1 {
		return nameVariants[0], true
	}
	return "", true
}

// collectInstanceNameVariants finds all values for keys matching instance_name case-insensitively.
func collectInstanceNameVariants(rawMap map[string]interface{}) []string {
	var results []string
	for k, v := range rawMap {
		if strings.EqualFold(k, "instance_name") {
			if s, ok := v.(string); ok {
				trimmed := strings.TrimSpace(s)
				if trimmed != "" {
					results = append(results, trimmed)
				}
			}
		}
	}
	return results
}

// collectVariantsByKey finds all string values for keys matching targetKey case-insensitively.
func collectVariantsByKey(rawMap map[string]interface{}, targetKey string) []string {
	var results []string
	for k, v := range rawMap {
		if strings.EqualFold(k, targetKey) {
			if s, ok := v.(string); ok {
				trimmed := strings.TrimSpace(s)
				if trimmed != "" {
					results = append(results, trimmed)
				}
			}
		}
	}
	return results
}

// isReservedPlatformName checks whether a name attempts to claim the reserved platform name.
func isReservedPlatformName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), PlatformInstanceName)
}

// hasManagedSingleBodyTarget checks if the unique body-extracted target is platform-managed.
func hasManagedSingleBodyTarget(c *gin.Context, rawMap map[string]interface{}) bool {
	target := extractSingleBodyTarget(rawMap)
	if target == "" {
		return false
	}
	tenantID := resolveRequestTenantID(c)
	return isManagedProviderTarget(target, tenantID) || isManagedInstanceTarget(target, tenantID, "")
}

// extractSingleBodyTarget extracts the unique target resource identifier from non-batch mutation bodies.
func extractSingleBodyTarget(rawMap map[string]interface{}) string {
	for _, key := range []string{"provider_name", "providerName", "name", "provider", "instance"} {
		if val, ok := rawMap[key].(string); ok {
			if trimmed := strings.TrimSpace(val); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// evalInstanceRoute evaluates access permissions on instance-targeted endpoints.
func evalInstanceRoute(c *gin.Context) {
	inst := extractInstanceTarget(c)
	if inst == "" {
		c.Next()
		return
	}
	tenantID := resolveRequestTenantID(c)
	providerParam := extractProviderTarget(c)
	if isManagedInstanceTarget(inst, tenantID, providerParam) {
		blockManagedMutation(c)
		return
	}
	c.Next()
}

// readBoundedBody reads request bodies up to getMaxMutationBodyBytes using http.MaxBytesReader and restores the stream.
func readBoundedBody(c *gin.Context) ([]byte, bool) {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return nil, true
	}
	maxBytes := getMaxMutationBodyBytes()
	reader := http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	bodyBytes, err := io.ReadAll(reader)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"code":    http.StatusRequestEntityTooLarge,
				"message": "Request entity too large",
			})
			return nil, false
		}
		blockManagedMutation(c)
		return nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	return bytes.TrimSpace(bodyBytes), true
}

// handleBatchDropBody processes batch drop requests with anti-DoS and managed instance checks.
func handleBatchDropBody(c *gin.Context, body []byte, rawMap map[string]interface{}) {
	if hasDuplicateInstancesJSONKey(body) {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"code":    http.StatusBadRequest,
			"message": "Duplicate instances key in request body",
		})
		return
	}

	targets, ok := extractBatchTargets(c, rawMap)
	if !ok {
		return
	}

	tenantID := resolveRequestTenantID(c)
	providerParam := extractProviderTarget(c)
	if hasAnyManagedInstance(targets, tenantID, providerParam) {
		blockManagedMutation(c)
		return
	}

	c.Next()
}

// extractBatchTargets parses the instances array, rejecting duplicates and bounds violations.
func extractBatchTargets(c *gin.Context, rawMap map[string]interface{}) ([]string, bool) {
	var instancesFound bool
	var targets []string

	for k, v := range rawMap {
		if !strings.EqualFold(k, "instances") {
			continue
		}
		if instancesFound {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code":    http.StatusBadRequest,
				"message": "Duplicate instances key in request body",
			})
			return nil, false
		}
		instancesFound = true

		rawInsts, ok := v.([]interface{})
		if !ok {
			continue
		}
		if len(rawInsts) > MaxBatchInstanceTargets {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code":    http.StatusBadRequest,
				"message": "Batch instance targets exceed limit",
			})
			return nil, false
		}
		for _, item := range rawInsts {
			if inst, ok := item.(string); ok {
				trimmed := strings.TrimSpace(inst)
				if trimmed != "" {
					targets = append(targets, trimmed)
				}
			}
		}
	}
	return targets, true
}

// extractParamOrQuery retrieves the first non-empty value matching parameter or query keys.
func extractParamOrQuery(c *gin.Context, paramKeys []string, queryKeys []string) string {
	for _, k := range paramKeys {
		if v := strings.TrimSpace(c.Param(k)); v != "" {
			return v
		}
	}
	for _, k := range queryKeys {
		if v := strings.TrimSpace(c.Query(k)); v != "" {
			return v
		}
	}
	return ""
}

// extractProviderTarget resolves provider identification from request parameters and queries.
func extractProviderTarget(c *gin.Context) string {
	return extractParamOrQuery(c,
		[]string{"provider_id_or_name", "provider", "provider_name", "name"},
		[]string{"name", "provider_name", "provider"},
	)
}

// extractInstanceTarget resolves instance identification from request parameters and queries.
func extractInstanceTarget(c *gin.Context) string {
	return extractParamOrQuery(c,
		[]string{"instance_id_or_name", "instance", "instance_name"},
		[]string{"instance_name", "instance"},
	)
}

// isManagedInstanceTarget verifies if a given instance name or ID is platform-managed.
func isManagedInstanceTarget(name string, tenantID string, providerParam string) bool {
	if strings.EqualFold(name, PlatformInstanceName) {
		return true
	}
	return dao.DB != nil && isManagedInstance(name, tenantID, providerParam)
}

// isManagedProviderTarget verifies if a given provider is platform-managed for the tenant.
func isManagedProviderTarget(name string, tenantID string) bool {
	if strings.EqualFold(name, PlatformInstanceName) {
		return true
	}
	return dao.DB != nil && isManagedProvider(name, tenantID)
}

// isPlatformPath checks if path segments explicitly target platform-managed instances or providers.
func isPlatformPath(path string) bool {
	lowerPath := strings.ToLower(path)
	if idx := strings.Index(lowerPath, "?"); idx != -1 {
		lowerPath = lowerPath[:idx]
	}
	segments := strings.Split(strings.Trim(lowerPath, "/"), "/")
	return isPlatformProviderRoute(segments) || isPlatformSettingRoute(segments)
}

// isPlatformProviderRoute asserts whether provider routes target the platform provider or instance.
func isPlatformProviderRoute(segments []string) bool {
	segs := stripAPIPrefix(segments)
	if len(segs) < 2 || segs[0] != "providers" {
		return false
	}
	if segs[1] == PlatformInstanceName {
		return true
	}
	return len(segs) >= 4 && segs[2] == "instances" && segs[3] == PlatformInstanceName
}

// isPlatformSettingRoute asserts whether settings/admin routes target the platform provider.
func isPlatformSettingRoute(segments []string) bool {
	for i, seg := range segments {
		if (seg == "setting" || seg == "admin") && i+3 < len(segments) {
			if segments[i+1] == "model" && segments[i+2] == "provider" && segments[i+3] == PlatformInstanceName {
				return true
			}
		}
	}
	return false
}

// stripAPIPrefix strips leading "api/v1" segments if present.
func stripAPIPrefix(segments []string) []string {
	if len(segments) >= 3 && segments[0] == "api" && segments[1] == "v1" {
		return segments[2:]
	}
	return segments
}

// isInstanceRoute checks if the request explicitly targets an instance-level resource rather than a provider.
func isInstanceRoute(c *gin.Context, path string) bool {
	if hasBoundInstanceParam(c) {
		return true
	}
	if fullPath := c.FullPath(); fullPath != "" {
		return routeTemplateHasInstance(fullPath)
	}
	return fallbackPathHasInstance(path)
}

// hasBoundInstanceParam checks if path parameters carry instance identifiers.
func hasBoundInstanceParam(c *gin.Context) bool {
	return c.Param("instance_id_or_name") != "" ||
		c.Param("instance") != "" ||
		c.Param("instance_name") != ""
}

// routeTemplateHasInstance inspects the Gin route template for instance segments.
func routeTemplateHasInstance(fullPath string) bool {
	for _, seg := range strings.Split(fullPath, "/") {
		if strings.HasPrefix(seg, ":") || strings.HasPrefix(seg, "*") {
			continue
		}
		lower := strings.ToLower(seg)
		if lower == "instance" || lower == "instances" {
			return true
		}
	}
	return false
}

// fallbackPathHasInstance inspects the raw URL path for instance segments past the provider identifier.
func fallbackPathHasInstance(path string) bool {
	lower := strings.ToLower(path)
	if idx := strings.Index(lower, "?"); idx != -1 {
		lower = lower[:idx]
	}
	for i, seg := range strings.Split(strings.Trim(lower, "/"), "/") {
		if (seg == "instance" || seg == "instances") && i > 3 {
			return true
		}
	}
	return false
}

// isBatchDropRoute checks if the route is the batch instance drop endpoint (/.../instances).
func isBatchDropRoute(c *gin.Context, path string) bool {
	if c.Request.Method != http.MethodDelete {
		return false
	}
	if fullPath := c.FullPath(); fullPath != "" {
		return strings.HasSuffix(strings.TrimRight(fullPath, "/"), "/instances")
	}
	return strings.HasSuffix(strings.TrimRight(path, "/"), "/instances")
}

// hasDuplicateInstancesJSONKey checks if the raw JSON payload contains multiple keys
// matching "instances" case-insensitively, including identical keys collapsed by Go maps.
func hasDuplicateInstancesJSONKey(body []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	if !isTopLevelObject(dec) {
		return false
	}
	instancesCount := 0
	for dec.More() {
		keyStr, err := decodeNextObjectEntry(dec)
		if err != nil {
			return false
		}
		if strings.EqualFold(keyStr, "instances") {
			instancesCount++
			if instancesCount > 1 {
				return true
			}
		}
	}
	return false
}

// isTopLevelObject verifies whether the JSON reader starts with an opening curly brace.
func isTopLevelObject(dec *json.Decoder) bool {
	t, err := dec.Token()
	if err != nil {
		return false
	}
	delim, ok := t.(json.Delim)
	return ok && delim == '{'
}

// decodeNextObjectEntry reads the next object key and consumes its associated value.
func decodeNextObjectEntry(dec *json.Decoder) (string, error) {
	t, err := dec.Token()
	if err != nil {
		return "", err
	}
	keyStr, _ := t.(string)
	var rawVal json.RawMessage
	err = dec.Decode(&rawVal)
	return keyStr, err
}

// hasAnyManagedInstance checks if any instance in targets is platform-managed,
// restricting name matches to the authenticated tenant's resolved provider via subquery.
func hasAnyManagedInstance(targets []string, tenantID string, providerParam string) bool {
	cleanTargets, hasPlatform := sanitizeTargets(targets)
	if hasPlatform {
		return true
	}
	if len(cleanTargets) == 0 || dao.DB == nil {
		return false
	}

	query := dao.DB.Select("id", "instance_name", "extra")
	if tenantID != "" {
		query = query.Where("id IN ? OR (instance_name IN ? AND provider_id IN (?))",
			cleanTargets, cleanTargets, providerIDSubquery(tenantID, providerParam))
	} else {
		query = query.Where("id IN ? OR instance_name IN ?", cleanTargets, cleanTargets)
	}

	var instances []entity.TenantModelInstance
	if err := query.Find(&instances).Error; err != nil {
		return !errors.Is(err, gorm.ErrRecordNotFound)
	}
	for _, inst := range instances {
		if isInstanceRecordManaged(inst) {
			return true
		}
	}
	return false
}

// providerIDSubquery constructs a GORM subquery selecting tenant provider IDs.
func providerIDSubquery(tenantID string, providerParam string) *gorm.DB {
	sub := dao.DB.Model(&entity.TenantModelProvider{}).Select("id").Where("tenant_id = ?", tenantID)
	if providerParam != "" {
		sub = sub.Where("id = ? OR provider_name = ?", providerParam, providerParam)
	}
	return sub
}

// sanitizeTargets trims targets and detects presence of the platform instance name.
func sanitizeTargets(targets []string) ([]string, bool) {
	var clean []string
	for _, name := range targets {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if strings.EqualFold(trimmed, PlatformInstanceName) {
			return nil, true
		}
		clean = append(clean, trimmed)
	}
	return clean, false
}

// isManagedInstance checks if the instance record in database carries is_managed == true or instance_name == "platform",
// matching primary key first, then scoping name queries to the resolved provider.
func isManagedInstance(instanceIDOrName string, tenantID string, providerParam string) bool {
	instanceIDOrName = strings.TrimSpace(instanceIDOrName)
	if dao.DB == nil || instanceIDOrName == "" {
		return false
	}
	if strings.EqualFold(instanceIDOrName, PlatformInstanceName) {
		return true
	}
	if managed, found, err := lookupManagedInstanceByID(instanceIDOrName); found || err != nil {
		return managed || err != nil
	}
	return lookupManagedInstanceByName(instanceIDOrName, tenantID, providerParam)
}

// lookupManagedInstanceByID executes exact primary-key matching against instance entities.
func lookupManagedInstanceByID(id string) (bool, bool, error) {
	var instance entity.TenantModelInstance
	err := dao.DB.Select("id", "instance_name", "extra").Where("id = ?", id).First(&instance).Error
	if err == nil {
		return isInstanceRecordManaged(instance), true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, false, nil
	}
	return false, false, err
}

// lookupManagedInstanceByName evaluates name-based instance matches scoped by provider via subquery.
func lookupManagedInstanceByName(name string, tenantID string, providerParam string) bool {
	q := dao.DB.Select("id", "instance_name", "extra").Where("instance_name = ?", name)
	if tenantID != "" {
		q = q.Where("provider_id IN (?)", providerIDSubquery(tenantID, providerParam))
	}
	var candidates []entity.TenantModelInstance
	if err := q.Find(&candidates).Error; err != nil {
		return !errors.Is(err, gorm.ErrRecordNotFound)
	}
	for _, c := range candidates {
		if isInstanceRecordManaged(c) {
			return true
		}
	}
	return false
}

// isManagedProvider checks if the provider or any of its instances are marked as platform-managed.
func isManagedProvider(providerIDOrName string, tenantID string) bool {
	providerIDOrName = strings.TrimSpace(providerIDOrName)
	if dao.DB == nil || providerIDOrName == "" {
		return false
	}
	if strings.EqualFold(providerIDOrName, PlatformInstanceName) {
		return true
	}
	ids, err := findMatchingProviderIDs(providerIDOrName, tenantID)
	if err != nil {
		return !errors.Is(err, gorm.ErrRecordNotFound)
	}
	if len(ids) == 0 {
		return false
	}
	return hasManagedInstancesUnderProviders(ids)
}

// findMatchingProviderIDs queries provider IDs matching name/ID and scoped by tenantID.
func findMatchingProviderIDs(name string, tenantID string) ([]string, error) {
	query := dao.DB.Model(&entity.TenantModelProvider{}).
		Select("id").
		Where("id = ? OR provider_name = ?", name, name)
	if tenantID != "" {
		query = query.Where("tenant_id = ?", tenantID)
	}
	var ids []string
	err := query.Pluck("id", &ids).Error
	return ids, err
}

// hasManagedInstancesUnderProviders checks whether any instance under providerIDs is managed.
func hasManagedInstancesUnderProviders(providerIDs []string) bool {
	var instances []entity.TenantModelInstance
	err := dao.DB.Model(&entity.TenantModelInstance{}).
		Select("instance_name", "extra").
		Where("provider_id IN ?", providerIDs).
		Find(&instances).Error
	if err != nil {
		return !errors.Is(err, gorm.ErrRecordNotFound)
	}
	for _, inst := range instances {
		if isInstanceRecordManaged(inst) {
			return true
		}
	}
	return false
}

// isInstanceRecordManaged evaluates whether a database instance is platform-managed.
func isInstanceRecordManaged(inst entity.TenantModelInstance) bool {
	if strings.EqualFold(inst.InstanceName, PlatformInstanceName) {
		return true
	}
	return isManagedExtraJSON(inst.Extra)
}

// isManagedExtraJSON extracts and validates the top-level is_managed flag from extra JSON.
// It accepts both string ("true") and boolean (true) representations.
func isManagedExtraJSON(extra string) bool {
	if extra == "" {
		return false
	}
	var extraMap map[string]interface{}
	if err := json.Unmarshal([]byte(extra), &extraMap); err == nil {
		val := extraMap["is_managed"]
		if s, ok := val.(string); ok && strings.EqualFold(strings.TrimSpace(s), "true") {
			return true
		}
		if b, ok := val.(bool); ok && b {
			return true
		}
	}
	return false
}

func blockManagedMutation(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"code":    403,
		"message": ErrMsgManagedProviderForbidden,
	})
}
