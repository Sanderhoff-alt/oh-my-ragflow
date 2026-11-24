package reconciler

import (
	"reflect"
	"testing"

	"ragflow/extensions/model-platform/backend/entity"
	rentity "ragflow/internal/entity"
)

func TestInitialWhitelist_FollowsAllowedModels(t *testing.T) {
	chat := entity.PlatformManagedModel{ModelName: "gpt-4o-mini", ModelType: int(rentity.ModelTypeChat)}
	embedding := entity.PlatformManagedModel{ModelName: "text-embedding-3-small", ModelType: int(rentity.ModelTypeEmbedding)}
	catalog := []entity.PlatformManagedModel{chat, embedding}

	got := initialWhitelist(catalog, nil)
	want := []string{"platform/chat", "platform/embedding", "gpt-4o-mini", "text-embedding-3-small"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("an empty allowlist permits every alias, got %v", got)
	}

	got = initialWhitelist(catalog, []string{"gpt-4o-mini", "text-embedding-3-small"})
	want = []string{"gpt-4o-mini", "text-embedding-3-small"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("aliases outside the allowlist must not be issued, got %v", got)
	}

	got = initialWhitelist(catalog, []string{"platform/chat", "gpt-4o-mini"})
	want = []string{"platform/chat", "gpt-4o-mini", "text-embedding-3-small"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a permitted alias is kept, got %v", got)
	}

	if got = initialWhitelist(nil, nil); !reflect.DeepEqual(got, []string{RevokedModelPlaceholder}) {
		t.Errorf("an empty catalog must yield the deny-all placeholder, got %v", got)
	}
}
