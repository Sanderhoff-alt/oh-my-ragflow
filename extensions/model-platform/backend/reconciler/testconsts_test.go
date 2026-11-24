package reconciler

import "ragflow/extensions/model-platform/backend/testenv"

// The baseline test catalog replaces the constants production code used to hardcode.
const (
	DefaultChatModelName      = testenv.ChatModel
	DefaultEmbeddingModelName = testenv.EmbeddingModel
)
