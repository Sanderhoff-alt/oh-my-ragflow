package publisher

import (
	"context"
	"time"

	"ragflow/extensions/model-platform/backend/entity"

	"gorm.io/gorm"
)

// Task status values derived from the items of a publish task.
const (
	TaskStatusRunning       = "running"
	TaskStatusCompleted     = "completed"
	TaskStatusPartialFailed = "partial_failed"
)

// Progress summarizes a publish task. Pending includes items currently being applied. The user counts are only
// non-zero for a set-default task: platform_default users moved to the model and user_explicit users kept.
type Progress struct {
	TaskID         string    `json:"task_id"`
	ModelName      string    `json:"model_name"`
	ModelType      int       `json:"model_type"`
	Status         string    `json:"status"`
	Total          int64     `json:"total"`
	Succeeded      int64     `json:"succeeded"`
	Pending        int64     `json:"pending"`
	Failed         int64     `json:"failed"`
	UpgradedUsers  int64     `json:"upgraded_users_count"`
	PreservedUsers int64     `json:"preserved_users_count"`
	CreatedAt      time.Time `json:"created_at"`
}

// FailedItem describes a tenant whose rollout exhausted its retries.
type FailedItem struct {
	TenantID   string `json:"tenant_id"`
	RetryCount int    `json:"retry_count"`
	LastError  string `json:"last_error"`
}

func loadTask(ctx context.Context, db *gorm.DB, taskID string) (*entity.ModelPublishTask, error) {
	var tasks []entity.ModelPublishTask
	if err := db.WithContext(ctx).Where("id = ?", taskID).Limit(1).Find(&tasks).Error; err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, ErrTaskNotFound
	}
	return &tasks[0], nil
}

// GetProgress counts the items of a task by status and derives the task status.
func GetProgress(ctx context.Context, db *gorm.DB, taskID string) (*Progress, error) {
	task, err := loadTask(ctx, db, taskID)
	if err != nil {
		return nil, err
	}

	var rows []struct {
		Status string
		Count  int64
	}
	if err := db.WithContext(ctx).Model(&entity.ModelPublishItem{}).
		Select("status, COUNT(*) AS count").Where("task_id = ?", taskID).Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}

	p := &Progress{TaskID: task.ID, ModelName: task.ModelName, ModelType: task.ModelType, CreatedAt: task.CreatedAt}
	var userCounts struct {
		UpgradedUsers  int64
		PreservedUsers int64
	}
	if err := db.WithContext(ctx).Model(&entity.ModelPublishItem{}).
		Select("COALESCE(SUM(upgraded_count), 0) AS upgraded_users, COALESCE(SUM(preserved_count), 0) AS preserved_users").
		Where("task_id = ?", taskID).Scan(&userCounts).Error; err != nil {
		return nil, err
	}
	p.UpgradedUsers, p.PreservedUsers = userCounts.UpgradedUsers, userCounts.PreservedUsers
	for _, r := range rows {
		p.Total += r.Count
		switch entity.PublishItemStatus(r.Status) {
		case entity.PublishItemSucceeded:
			p.Succeeded += r.Count
		case entity.PublishItemFailed:
			p.Failed += r.Count
		default:
			p.Pending += r.Count
		}
	}

	switch {
	case p.Pending > 0:
		p.Status = TaskStatusRunning
	case p.Failed > 0:
		p.Status = TaskStatusPartialFailed
	default:
		p.Status = TaskStatusCompleted
	}
	return p, nil
}

// ListFailedItems returns the failed tenants of a task, one page at a time.
func ListFailedItems(ctx context.Context, db *gorm.DB, taskID string, page, pageSize int) ([]FailedItem, int64, error) {
	if _, err := loadTask(ctx, db, taskID); err != nil {
		return nil, 0, err
	}

	query := db.WithContext(ctx).Model(&entity.ModelPublishItem{}).
		Where("task_id = ? AND status = ?", taskID, entity.PublishItemFailed)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	items := make([]FailedItem, 0)
	if err := query.Select("tenant_id, retry_count, last_error").Order("tenant_id ASC").
		Limit(pageSize).Offset((page - 1) * pageSize).Scan(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// RetryFailed moves every failed item of a task back to pending and returns how many were reset.
func RetryFailed(ctx context.Context, db *gorm.DB, taskID string) (int64, error) {
	if _, err := loadTask(ctx, db, taskID); err != nil {
		return 0, err
	}

	res := db.WithContext(ctx).Model(&entity.ModelPublishItem{}).
		Where("task_id = ? AND status = ?", taskID, entity.PublishItemFailed).
		Updates(map[string]interface{}{
			"status":        entity.PublishItemPending,
			"retry_count":   0,
			"next_retry_at": nil,
			"version":       gorm.Expr("version + 1"),
			"updated_at":    time.Now(),
		})
	return res.RowsAffected, res.Error
}
