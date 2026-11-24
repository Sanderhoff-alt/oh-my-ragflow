package entity

import (
	"fmt"
	"strings"

	rentity "ragflow/internal/entity"
)

// DefaultSlot holds the composite reference and tenant_model id of one default model slot.
type DefaultSlot struct {
	ModelID       string
	TenantModelID string
}

// ModelSlotSpec describes one platform-managed model type and its corresponding user_tenant columns.
// This table is the single source of truth across publisher validation, reconciler defaults,
// StickyGuard, and the preference observation middleware.
//
// OCR (ModelTypeOCR = 64) is deliberately omitted because OCR runs locally as a CV/layout pipeline
// rather than through the LiteLLM gateway.
type ModelSlotSpec struct {
	Type           rentity.ModelType
	Name           string
	IDColumn       string
	TenantIDColumn string
	NullableID     bool
	Read           func(*rentity.UserTenant) DefaultSlot
}

func derefSlotString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ManagedModelSlots lists every model type that the model platform can manage through the gateway,
// in deterministic order.
var ManagedModelSlots = []ModelSlotSpec{
	{
		Type:           rentity.ModelTypeChat,
		Name:           "chat",
		IDColumn:       "llm_id",
		TenantIDColumn: "tenant_llm_id",
		Read: func(ut *rentity.UserTenant) DefaultSlot {
			return DefaultSlot{ModelID: ut.LLMID, TenantModelID: derefSlotString(ut.TenantLLMID)}
		},
	},
	{
		Type:           rentity.ModelTypeEmbedding,
		Name:           "embedding",
		IDColumn:       "embd_id",
		TenantIDColumn: "tenant_embd_id",
		Read: func(ut *rentity.UserTenant) DefaultSlot {
			return DefaultSlot{ModelID: ut.EmbdID, TenantModelID: derefSlotString(ut.TenantEmbdID)}
		},
	},
	{
		Type:           rentity.ModelTypeSpeech2Text,
		Name:           "asr",
		IDColumn:       "asr_id",
		TenantIDColumn: "tenant_asr_id",
		Read: func(ut *rentity.UserTenant) DefaultSlot {
			return DefaultSlot{ModelID: ut.ASRID, TenantModelID: derefSlotString(ut.TenantASRID)}
		},
	},
	{
		Type:           rentity.ModelTypeImage2Text,
		Name:           "vision",
		IDColumn:       "img2txt_id",
		TenantIDColumn: "tenant_img2txt_id",
		Read: func(ut *rentity.UserTenant) DefaultSlot {
			return DefaultSlot{ModelID: ut.Img2TxtID, TenantModelID: derefSlotString(ut.TenantImg2TxtID)}
		},
	},
	{
		Type:           rentity.ModelTypeRerank,
		Name:           "rerank",
		IDColumn:       "rerank_id",
		TenantIDColumn: "tenant_rerank_id",
		Read: func(ut *rentity.UserTenant) DefaultSlot {
			return DefaultSlot{ModelID: ut.RerankID, TenantModelID: derefSlotString(ut.TenantRerankID)}
		},
	},
	{
		Type:           rentity.ModelTypeTTS,
		Name:           "tts",
		IDColumn:       "tts_id",
		TenantIDColumn: "tenant_tts_id",
		NullableID:     true,
		Read: func(ut *rentity.UserTenant) DefaultSlot {
			return DefaultSlot{ModelID: derefSlotString(ut.TTSID), TenantModelID: derefSlotString(ut.TenantTTSID)}
		},
	},
	{
		Type:           rentity.ModelTypeDecision,
		Name:           "decision",
		IDColumn:       "decision_id",
		TenantIDColumn: "tenant_decision_id",
		NullableID:     true,
		Read: func(ut *rentity.UserTenant) DefaultSlot {
			return DefaultSlot{ModelID: derefSlotString(ut.DecisionID), TenantModelID: derefSlotString(ut.TenantDecisionID)}
		},
	},
}

var managedModelSlotByType = func() map[int]ModelSlotSpec {
	m := make(map[int]ModelSlotSpec, len(ManagedModelSlots))
	for _, spec := range ManagedModelSlots {
		m[int(spec.Type)] = spec
	}
	return m
}()

// LookupModelSlot returns the slot specification for a ModelType bit flag, if managed by the platform.
func LookupModelSlot(modelType int) (ModelSlotSpec, bool) {
	spec, ok := managedModelSlotByType[modelType]
	return spec, ok
}

// IsManagedModelType reports whether modelType is a single managed ModelType bit flag.
func IsManagedModelType(modelType int) bool {
	_, ok := managedModelSlotByType[modelType]
	return ok
}

// ManagedModelTypesDescription formats the supported model types for validation messages.
func ManagedModelTypesDescription() string {
	parts := make([]string, 0, len(ManagedModelSlots))
	for _, spec := range ManagedModelSlots {
		parts = append(parts, fmt.Sprintf("%s (%d)", spec.Name, int(spec.Type)))
	}
	return strings.Join(parts, ", ")
}

// ReadUserTenantSlots returns the current default model slot for every managed model type.
func ReadUserTenantSlots(ut *rentity.UserTenant) map[int]DefaultSlot {
	slots := make(map[int]DefaultSlot, len(ManagedModelSlots))
	if ut == nil {
		return slots
	}
	for _, spec := range ManagedModelSlots {
		slots[int(spec.Type)] = spec.Read(ut)
	}
	return slots
}

// ClearedIDValue returns the column value used when clearing a default model slot on user_tenant.
// Nullable slots (tts_id, decision_id) are cleared to NULL; NOT NULL string slots are cleared to "".
func (s ModelSlotSpec) ClearedIDValue() interface{} {
	if s.NullableID {
		return nil
	}
	return ""
}
