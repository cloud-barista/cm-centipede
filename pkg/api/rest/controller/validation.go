package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/cloud-barista/cm-centipede/pkg/api/rest/model"
	validationpkg "github.com/cloud-barista/cm-centipede/pkg/core/validation"
	"github.com/cloud-barista/cm-centipede/pkg/dao"
)

// StartValidation godoc
//
//	@Summary		Start validation for a completed migration
//	@Description	Validates that the destination data matches the source after a completed migration. Returns 400 if migration is not completed or the mode is unknown, 409 if validation is already running.
//	@Description	The optional body's mode decides how filesystem items are compared: "quick" (default) compares size and modification time only, "checksum" hashes every migrated file on both hosts. Object storage and databases are compared one way whatever it says. While the validation runs, GET reports its progress in validationMessage.
//	@Tags			validation
//	@Accept			json
//	@Produce		json
//	@Param			migrationId	path		string							true	"Migration ID"
//	@Param			request		body		model.StartValidationRequest	false	"Validation options"
//	@Success		202			{object}	model.ApiResponse[model.SimpleMessageResponse]
//	@Failure		400			{object}	model.ApiResponse[any]
//	@Failure		404			{object}	model.ApiResponse[any]
//	@Failure		409			{object}	model.ApiResponse[any]
//	@Failure		500			{object}	model.ApiResponse[any]
//	@Router			/centipede/migration/{migrationId}/validation [post]
func StartValidation(c echo.Context) error {
	id := c.Param("migrationId")

	// The body is optional: none at all is the default mode.
	var req model.StartValidationRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse("invalid request body: "+err.Error()))
	}
	mode := req.Mode
	if mode == "" {
		mode = validationpkg.ModeQuick
	}
	if !validationpkg.ValidMode(mode) {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse(
			fmt.Sprintf("unknown validation mode %q: want %q or %q", req.Mode,
				validationpkg.ModeQuick, validationpkg.ModeChecksum)))
	}

	m, err := dao.GetMigration(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.JSON(http.StatusNotFound, model.SimpleErrorResponse("migration not found: "+id))
		}
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("get migration: "+err.Error()))
	}

	if m.Status != "completed" {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse(
			fmt.Sprintf("validation requires migration status=completed, current status=%s", m.Status),
		))
	}
	if m.ValidationStatus == "running" {
		return c.JSON(http.StatusConflict, model.SimpleErrorResponse("validation is already running for migration: "+id))
	}

	now := time.Now()
	m.ValidationStatus = "running"
	m.ValidationMessage = ""
	m.ValidationDetails = nil
	m.ValidationMode = mode
	m.ValidationStartedAt = &now
	if err := dao.UpdateMigrationValidation(m); err != nil {
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("update validation status: "+err.Error()))
	}

	go runValidation(id, mode)

	return c.JSON(http.StatusAccepted, model.SuccessResponseWithMessage(
		model.SimpleMessageResponse{Message: "validation started"},
		"validation started",
	))
}

// GetValidation godoc
//
//	@Summary		Get validation result for a migration
//	@Description	Returns the current validation status, summary message, and per-item detail list for the given migration.
//	@Tags			validation
//	@Produce		json
//	@Param			migrationId	path		string	true	"Migration ID"
//	@Success		200			{object}	model.ApiResponse[model.ValidationResultResponse]
//	@Failure		404			{object}	model.ApiResponse[any]
//	@Failure		500			{object}	model.ApiResponse[any]
//	@Router			/centipede/migration/{migrationId}/validation [get]
func GetValidation(c echo.Context) error {
	id := c.Param("migrationId")

	m, err := dao.GetMigration(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.JSON(http.StatusNotFound, model.SimpleErrorResponse("migration not found: "+id))
		}
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("get migration: "+err.Error()))
	}

	status := m.ValidationStatus
	if status == "" {
		status = "not_run"
	}

	return c.JSON(http.StatusOK, model.SuccessResponse(model.ValidationResultResponse{
		ValidationStatus:  status,
		ValidationMessage: m.ValidationMessage,
		Details:           m.ValidationDetails,
		Mode:              m.ValidationMode,
		StartedAt:         m.ValidationStartedAt,
	}))
}

// progressInterval is the shortest gap between two writes of a running
// validation's progress. A checksum run reports every file it hashes; the
// record only has to be fresh enough for a screen polling it.
const progressInterval = 2 * time.Second

// validationProgress writes a running validation's progress line to its record,
// at most once per progressInterval. It is called from several goroutines at
// once, and stop() ends it before the result is written, so a late progress
// line cannot land on top of the result.
type validationProgress struct {
	mu      sync.Mutex
	m       *model.Migration
	last    time.Time
	stopped bool
}

func (p *validationProgress) report(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || time.Since(p.last) < progressInterval {
		return
	}
	p.last = time.Now()
	p.m.ValidationMessage = message
	if err := dao.UpdateMigrationValidation(p.m); err != nil {
		log.Warn().Str("migrationID", p.m.ID).Err(err).Msg("runValidation: update progress failed")
	}
}

func (p *validationProgress) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
}

// runValidation orchestrates ValidateFileSystem, ValidateObjectStorage, and ValidateDBMS
// for all plan items, then persists the aggregated result into the Migration record.
// Must be launched as a goroutine: go runValidation(migrationID, mode)
//
// mode is how filesystem items are compared; see validationpkg.Options.
func runValidation(migrationID, mode string) {
	m, err := dao.GetMigration(migrationID)
	if err != nil {
		log.Error().Str("migrationID", migrationID).Err(err).Msg("runValidation: load migration failed")
		return
	}

	var allDetails []model.ValidationDetailItem
	prop := m.Plan.TargetDataMigrationModel
	ctx := context.Background()

	progress := &validationProgress{m: m}
	total := len(prop.FileSystems) + len(prop.ObjectStorages) + len(prop.Databases)
	entry := 0
	// optsFor leads each progress line with the plan entry it belongs to — when
	// there are several. A plan holds one entry per source connection, so a
	// group migration's hosts can share a path, and "2/3" is what tells them
	// apart; with one entry the counter says nothing. The kind is left out: the
	// path or database name and the unit counted already say it.
	optsFor := func() validationpkg.Options {
		entry++
		prefix := ""
		if total > 1 {
			prefix = fmt.Sprintf("%d/%d · ", entry, total)
		}
		return validationpkg.Options{
			Mode:     mode,
			Progress: func(message string) { progress.report(prefix + message) },
		}
	}

	// The plan entry is stamped here rather than inside pkg/core/validation: a
	// validator is handed one entry and never sees the plan it sits in, the same
	// reason the executor stamps its logs on the way out. Without it a plan of
	// several entries reports every one of them under the same ItemPath.
	stamp := func(id string, details []model.ValidationDetailItem) []model.ValidationDetailItem {
		for i := range details {
			details[i].PlanEntryID = id
		}
		return details
	}

	// ── Filesystem ───────────────────────────────────────────────────────────
	for _, fs := range prop.FileSystems {
		details, err := validationpkg.ValidateFileSystem(ctx, fs, optsFor())
		if err != nil {
			allDetails = append(allDetails, model.ValidationDetailItem{
				PlanEntryID: fs.PlanEntryID,
				ItemPath:    "filesystem",
				Status:      "failed",
				Message:     fmt.Sprintf("validation error: %v", err),
			})
			continue
		}
		allDetails = append(allDetails, stamp(fs.PlanEntryID, details)...)
	}

	// ── Object Storage ───────────────────────────────────────────────────────
	for _, oss := range prop.ObjectStorages {
		details, err := validationpkg.ValidateObjectStorage(ctx, oss, optsFor())
		if err != nil {
			allDetails = append(allDetails, model.ValidationDetailItem{
				PlanEntryID: oss.PlanEntryID,
				ItemPath:    "objectstorage",
				Status:      "failed",
				Message:     fmt.Sprintf("validation error: %v", err),
			})
			continue
		}
		allDetails = append(allDetails, stamp(oss.PlanEntryID, details)...)
	}

	// ── DBMS ─────────────────────────────────────────────────────────────────
	for _, db := range prop.Databases {
		details, err := validationpkg.ValidateDBMS(ctx, db, optsFor())
		if err != nil {
			allDetails = append(allDetails, model.ValidationDetailItem{
				PlanEntryID: db.PlanEntryID,
				ItemPath:    fmt.Sprintf("%s/%s", string(db.DBType), "database"),
				Status:      "failed",
				Message:     fmt.Sprintf("validation error: %v", err),
			})
			continue
		}
		allDetails = append(allDetails, stamp(db.PlanEntryID, details)...)
	}

	// ── Determine overall status ─────────────────────────────────────────────
	// A warning does not fail the validation: a character-set difference can be
	// a managed service imposing its own default, which is not on its own
	// evidence that the migration went wrong. It is still reported.
	overallStatus := "passed"
	failedCount := 0
	warningCount := 0
	for _, d := range allDetails {
		switch d.Status {
		case "failed":
			failedCount++
			overallStatus = "failed"
		case "warning":
			warningCount++
		}
	}

	var message string
	switch overallStatus {
	case "passed":
		message = "all items validated successfully"
		if warningCount > 0 {
			message = fmt.Sprintf("all items validated successfully with %d warning(s)", warningCount)
		}
	case "failed":
		message = fmt.Sprintf("%d item(s) failed validation", failedCount)
		if warningCount > 0 {
			message = fmt.Sprintf("%d item(s) failed validation, %d warning(s)", failedCount, warningCount)
		}
	}

	progress.stop()
	m.ValidationStatus = overallStatus
	m.ValidationMessage = message
	m.ValidationDetails = allDetails

	if err := dao.UpdateMigrationValidation(m); err != nil {
		log.Error().Str("migrationID", migrationID).Err(err).Msg("runValidation: persist result failed")
		return
	}
	log.Info().
		Str("migrationID", migrationID).
		Str("validationStatus", overallStatus).
		Int("failedItems", failedCount).
		Msg("validation completed")
}
