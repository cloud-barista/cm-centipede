package controller

import (
	"errors"
	"fmt"
	"net/http"

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
//	@Description	Validates that the destination data matches the source after a completed migration. Returns 400 if migration is not completed, 409 if validation is already running.
//	@Tags			validation
//	@Produce		json
//	@Param			migrationId	path		string	true	"Migration ID"
//	@Success		202			{object}	model.ApiResponse[model.SimpleMessageResponse]
//	@Failure		400			{object}	model.ApiResponse[any]
//	@Failure		404			{object}	model.ApiResponse[any]
//	@Failure		409			{object}	model.ApiResponse[any]
//	@Failure		500			{object}	model.ApiResponse[any]
//	@Router			/centipede/migration/{migrationId}/validation [post]
func StartValidation(c echo.Context) error {
	id := c.Param("migrationId")

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

	m.ValidationStatus = "running"
	m.ValidationMessage = ""
	m.ValidationDetails = nil
	if err := dao.UpdateMigrationValidation(m); err != nil {
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("update validation status: "+err.Error()))
	}

	go runValidation(id)

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
	}))
}

// runValidation orchestrates ValidateFileSystem, ValidateObjectStorage, and ValidateDBMS
// for all plan items, then persists the aggregated result into the Migration record.
// Must be launched as a goroutine: go runValidation(migrationID)
func runValidation(migrationID string) {
	m, err := dao.GetMigration(migrationID)
	if err != nil {
		log.Error().Str("migrationID", migrationID).Err(err).Msg("runValidation: load migration failed")
		return
	}

	var allDetails []model.ValidationDetailItem
	prop := m.Plan.TargetDataMigrationModel

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
		details, err := validationpkg.ValidateFileSystem(fs)
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
		details, err := validationpkg.ValidateObjectStorage(oss)
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
		details, err := validationpkg.ValidateDBMS(db)
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
