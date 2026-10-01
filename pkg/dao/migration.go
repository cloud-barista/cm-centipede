package dao

import (
	"errors"
	"fmt"

	"github.com/cloud-barista/cm-centipede/pkg/api/rest/model"
	"github.com/cloud-barista/cm-centipede/pkg/connsec"
	"github.com/cloud-barista/cm-centipede/pkg/db"
	"gorm.io/gorm"
)

// ErrMigrationRunning is returned when an operation is blocked because the
// migration is currently in the "running" state (e.g. delete while running).
var ErrMigrationRunning = errors.New("migration is running")

// ─── Migration ───────────────────────────────────────────────────────────────

// CreateMigration persists a new Migration.
//
// Inline connection credentials and the beetleDb password in the plan are
// encrypted before writing. The call is idempotent in practice: a plan that
// arrived from POST /plans/target is already ciphertext and is stored as it
// came, while a hand-written plaintext plan is sealed here.
func CreateMigration(m *model.Migration) error {
	if err := connsec.EncryptPlan(&m.Plan); err != nil {
		return fmt.Errorf("encrypt plan: %w", err)
	}
	return db.DB.Create(m).Error
}

// GetMigration returns the Migration with the given ID, its plan still
// encrypted.
//
// Nothing is decrypted here. The migration resolvers decrypt one ref at a time
// as they build each transfer, so a plan decrypted this early would only mean
// plaintext credentials sitting in every caller — including the ones whose
// answer goes straight into an HTTP response.
func GetMigration(id string) (*model.Migration, error) {
	var m model.Migration
	if err := db.DB.First(&m, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// applyMigrationFilter adds the status and created_at conditions of f to q.
// Both list queries go through it so the two endpoints cannot drift apart in
// what a filter means.
func applyMigrationFilter(q *gorm.DB, f model.MigrationListFilter) *gorm.DB {
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	// The date bounds are bound as yyyy-mm-dd strings, not as time.Time.
	// created_at is stored as server-local wall-clock text
	// ("2026-06-30 17:21:35.564542139+09:00"), so the comparison SQLite does is
	// lexicographic on that text. A time.Time bound normalised to UTC compares
	// against a different prefix and matches nothing — with no error to show for
	// it. Passing the date through as text keeps both sides on one axis, and,
	// unlike DATE(created_at), leaves the column bare so the created_at index
	// still applies.
	if f.DateFrom != "" {
		q = q.Where("created_at >= ?", f.DateFrom)
	}
	if f.DateToExcl != "" {
		q = q.Where("created_at < ?", f.DateToExcl)
	}
	return q
}

// newMigrationSummaries projects rows into list summaries.
//
// Nothing is decrypted here: a summary counts the plan's units instead of
// carrying the plan, and the counts survive encryption untouched. That is what
// keeps one unreadable row from failing the whole listing.
func newMigrationSummaries(items []model.Migration) []model.MigrationSummary {
	out := make([]model.MigrationSummary, len(items))
	for i := range items {
		out[i] = model.NewMigrationSummary(&items[i])
	}
	return out
}

// ListMigration returns a paginated list of migration summaries matching f,
// ordered by created_at DESC.
func ListMigration(f model.MigrationListFilter, page, pageSize int) ([]model.MigrationSummary, int64, error) {
	q := applyMigrationFilter(db.DB.Model(&model.Migration{}), f)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var items []model.Migration
	offset := (page - 1) * pageSize
	if err := q.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return newMigrationSummaries(items), total, nil
}

// ListAllMigration returns every migration summary matching f, ordered by
// created_at DESC, without pagination. The caller is responsible for bounding
// the result — see MigrationListFilter.ApplyDefaultWindow.
func ListAllMigration(f model.MigrationListFilter) ([]model.MigrationSummary, error) {
	q := applyMigrationFilter(db.DB.Model(&model.Migration{}), f)

	var items []model.Migration
	if err := q.Order("created_at DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return newMigrationSummaries(items), nil
}

// UpdateMigrationStatus updates only the status and progress fields, leaving the
// plan column untouched. Use this during execution to avoid re-encrypting the plan
// on every progress tick.
func UpdateMigrationStatus(m *model.Migration) error {
	return db.DB.Model(&model.Migration{}).Where("id = ?", m.ID).Updates(map[string]interface{}{
		"status":            m.Status,
		"status_message":    m.StatusMessage,
		"total_items":       m.TotalItems,
		"processed_items":   m.ProcessedItems,
		"failed_items":      m.FailedItems,
		"transferred_bytes": m.TransferredBytes,
		"current_object":    m.CurrentObject,
		"started_at":        m.StartedAt,
		"completed_at":      m.CompletedAt,
	}).Error
}

// UpdateMigrationValidation updates only the validation fields (status, message, details),
// leaving plan, status, and progress columns untouched.
// Use this after a validation run completes to persist results without re-encrypting the plan.
func UpdateMigrationValidation(m *model.Migration) error {
	return db.DB.Model(&model.Migration{}).Where("id = ?", m.ID).
		Select("validation_status", "validation_message", "validation_details").
		Updates(m).Error
}

// DeleteMigration deletes the Migration with the given ID.
// Returns ErrMigrationRunning when status is "running".
// Returns gorm.ErrRecordNotFound when no row matches.
func DeleteMigration(id string) error {
	var m model.Migration
	if err := db.DB.Select("id", "status").First(&m, "id = ?", id).Error; err != nil {
		return err
	}
	if m.Status == "running" {
		return ErrMigrationRunning
	}
	result := db.DB.Delete(&model.Migration{}, "id = ?", id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ─── MigrationLog ────────────────────────────────────────────────────────────

// CreateMigrationLog persists a single MigrationLog entry.
func CreateMigrationLog(entry *model.MigrationLog) error {
	return db.DB.Create(entry).Error
}

// ListAllMigrationLog returns every log for migrationID without pagination,
// optionally filtered by status ("success" | "failed" | "skipped").
func ListAllMigrationLog(migrationID, status string) ([]model.MigrationLog, error) {
	q := db.DB.Model(&model.MigrationLog{}).Where("migration_id = ?", migrationID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var items []model.MigrationLog
	if err := q.Order("created_at ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListMigrationLog returns a paginated list of logs for migrationID,
// optionally filtered by status ("success" | "failed" | "skipped").
func ListMigrationLog(migrationID, status string, page, pageSize int) ([]model.MigrationLog, int64, error) {
	q := db.DB.Model(&model.MigrationLog{}).Where("migration_id = ?", migrationID)
	if status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var items []model.MigrationLog
	offset := (page - 1) * pageSize
	if err := q.Order("created_at ASC").Offset(offset).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
