package publisher

import (
	"context"
	"errors"
	"testing"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/reconciler"
	rentity "ragflow/internal/entity"
)

const (
	gptRef      = "gpt-4o-mini@platform@OpenAI-API-Compatible"
	deepseekRef = "deepseek-v3@platform@OpenAI-API-Compatible"
)

func TestSetDefault_MovesPlatformDefaultUsersAndKeepsExplicitChoices(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	chat := int(rentity.ModelTypeChat)
	seedTenant(t, db, "t1", entity.StatusReady)
	db.Create(&entity.PlatformManagedModel{ID: "m-old", ModelName: "gpt-4o-mini", ModelType: chat, IsDefault: true, Status: entity.ManagedModelStatusActive})
	w := newTestWorker(db, gw)
	model := publishAndConverge(t, db, gw, w)

	sticky := reconciler.NewStickyGuard()
	ctx := context.Background()
	for _, u := range []string{"u1", "u2"} {
		db.Create(&rentity.UserTenant{ID: "ut-" + u, UserID: u, TenantID: "t1", Role: "normal", LLMID: gptRef})
	}
	if err := sticky.RecordPlatformDefault(ctx, db, "t1", "u1", chat, gptRef); err != nil {
		t.Fatalf("RecordPlatformDefault failed: %v", err)
	}
	if err := sticky.SetUserExplicitPreference(ctx, db, "t1", "u2", chat, "my-own@custom@Provider", ""); err != nil {
		t.Fatalf("SetUserExplicitPreference failed: %v", err)
	}

	res, err := SetDefault(ctx, db, model.ID)
	if err != nil || res.Total != 1 {
		t.Fatalf("expected a rollout for the Ready tenant, got %+v (%v)", res, err)
	}
	w.RunOnce(ctx)

	var u1, u2 rentity.UserTenant
	db.Where("user_id = ?", "u1").First(&u1)
	db.Where("user_id = ?", "u2").First(&u2)
	tm := tenantRow(t, db, "t1", "deepseek-v3")
	if u1.LLMID != deepseekRef || u1.TenantLLMID == nil || *u1.TenantLLMID != tm.ID {
		t.Errorf("a platform_default user must follow the new default with the tenant's own model id, got %+v", u1)
	}
	if u2.LLMID != "my-own@custom@Provider" {
		t.Errorf("a user who chose a model must be preserved, got %q", u2.LLMID)
	}

	var oldDefault, newDefault entity.PlatformManagedModel
	db.Where("id = ?", "m-old").First(&oldDefault)
	db.Where("id = ?", model.ID).First(&newDefault)
	if oldDefault.IsDefault || !newDefault.IsDefault {
		t.Errorf("exactly one default per type is expected, got old=%v new=%v", oldDefault.IsDefault, newDefault.IsDefault)
	}
	if p, _ := GetProgress(ctx, db, res.TaskID); p.UpgradedUsers != 1 || p.PreservedUsers != 1 || p.Status != TaskStatusCompleted {
		t.Errorf("expected the task to report one upgraded and one preserved user, got %+v", p)
	}
}

func TestSetDefault_OnboardsTenantsThatLackTheModelFirst(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	seedTenant(t, db, "late", entity.StatusReady)
	db.Create(&entity.PlatformManagedModel{ID: "m1", ModelName: "deepseek-v3", ModelType: int(rentity.ModelTypeChat), Status: entity.ManagedModelStatusActive})
	db.Create(&rentity.UserTenant{ID: "ut-u1", UserID: "u1", TenantID: "late", Role: "normal"})

	if _, err := SetDefault(context.Background(), db, "m1"); err != nil {
		t.Fatalf("SetDefault failed: %v", err)
	}
	newTestWorker(db, gw).RunOnce(context.Background())

	tm := tenantRow(t, db, "late", "deepseek-v3")
	var u1 rentity.UserTenant
	db.Where("user_id = ?", "u1").First(&u1)
	if tm.Status != "active" || u1.LLMID != deepseekRef {
		t.Errorf("a tenant without the model must receive it before its users are moved, row=%+v llm=%q", tm, u1.LLMID)
	}
}

func TestSetDefault_RequiresAnActiveModel(t *testing.T) {
	db := setupPublishDB(t)
	db.Create(&entity.PlatformManagedModel{ID: "m1", ModelName: "deepseek-v3", ModelType: int(rentity.ModelTypeChat), Status: entity.ManagedModelStatusRetired})

	if _, err := SetDefault(context.Background(), db, "m1"); !errors.Is(err, ErrModelNotActive) {
		t.Errorf("expected ErrModelNotActive, got %v", err)
	}
	if _, err := SetDefault(context.Background(), db, "missing"); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("expected ErrModelNotFound, got %v", err)
	}
	var model entity.PlatformManagedModel
	db.Where("id = ?", "m1").First(&model)
	if model.IsDefault {
		t.Error("a refused request must not change the default")
	}
}
