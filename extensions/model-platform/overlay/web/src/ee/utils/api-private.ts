/**
 * Enterprise customization overlay for api.ts.
 * Deep-merged over upstream apiEndpoints in api.ts.
 */
const apiAdminPrefix = '/api/v1/admin';

export default {
  adminModelPlatformHealth: `${apiAdminPrefix}/model-platform/health`,
  adminModelPlatformModels: `${apiAdminPrefix}/model-platform/models`,
  adminModelPlatformUsage: `${apiAdminPrefix}/model-platform/usage`,
};
