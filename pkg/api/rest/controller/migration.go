package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	"github.com/cloud-barista/cm-centipede/pkg/api/rest/model"
	"github.com/cloud-barista/cm-centipede/pkg/connsec"
	migrationpkg "github.com/cloud-barista/cm-centipede/pkg/core/migration"
	"github.com/cloud-barista/cm-centipede/pkg/core/plan"
	"github.com/cloud-barista/cm-centipede/pkg/dao"
)

// CreateMigration godoc
//
//	@Summary		Create and start a migration
//	@Description	Creates a Migration record from the given plan and immediately starts execution asynchronously.
//	@Tags			migration
//	@Accept			json
//	@Produce		json
//	@Param			body	body		model.CreateMigrationReq				true	"Migration creation request"
//	@Success		201		{object}	model.ApiResponse[model.Migration]
//	@Failure		400		{object}	model.ApiResponse[any]
//	@Failure		500		{object}	model.ApiResponse[any]
//	@Router			/centipede/migration [post]
func CreateMigration(c echo.Context) error {
	var req model.CreateMigrationReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse("invalid request body: "+err.Error()))
	}
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse("name is required"))
	}
	// Rejected here rather than defaulted silently: a misspelt "keeep" that ran
	// as "cleanup" would drop the very database it was meant to preserve.
	switch req.DBMSOnFailure {
	case "", model.DBMSOnFailureCleanup, model.DBMSOnFailureKeep:
	default:
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse(
			fmt.Sprintf("dbmsOnFailure must be %q or %q (got %q)",
				model.DBMSOnFailureCleanup, model.DBMSOnFailureKeep, req.DBMSOnFailure)))
	}

	// The plan arrives as POST /plans/target answered it, so every check below is
	// about a plan that was edited, hand-written, or issued elsewhere. Execution
	// is asynchronous: anything not caught here surfaces as a half-finished
	// migration rather than as a failed request.

	// Structure first. A ref whose sub-struct does not match its source is
	// invisible to the decryptability check — there are no fields to decrypt —
	// so checking that first would report the wrong problem.
	if err := connsec.ForEachRef(&req.Plan, func(label string, ref *commonmodel.ConnectionRef) error {
		if msg := validateConnectionRef(*ref); msg != "" {
			return fmt.Errorf("%s: %s", label, msg)
		}
		return nil
	}); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse("plan "+err.Error()))
	}

	// Two entries writing to the same place is how a multi-entry plan loses data:
	// they run one after another, so the second overwrites what the first wrote.
	// The plan layer refuses to build one, which leaves this the way such a plan
	// still arrives.
	if msg := plan.DuplicateDestination(req.Plan); msg != "" {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse("plan: "+msg))
	}

	// "/" → "/" copies the source host's system files over the target's. The
	// plan layer refuses to build one, so this is again the edited or
	// hand-written plan.
	if msg := plan.RootToRoot(req.Plan); msg != "" {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse("plan: "+msg))
	}

	// Entry names have to be distinct before anything is written under them:
	// they are what a log line, a validation detail and a partial retry identify
	// an entry by, and two entries sharing one put all three on top of each other.
	if dup := plan.DuplicateEntryID(req.Plan); dup != "" {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse(
			fmt.Sprintf("plan: planEntryId %q is carried by more than one entry", dup)))
	}

	// Then whether this server can actually open what it was handed.
	if err := connsec.VerifyPlanDecryptable(req.Plan); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse(
			"plan: this server cannot decrypt the credentials it carries — the plan was issued "+
				"by another cm-centipede instance, or before the encryption passphrase changed. "+
				"Re-run POST /centipede/plans/target to get one this server can use: "+err.Error()))
	}

	// An edited or hand-written plan may name none of its entries. Naming them
	// before the record is stored means execution, logging and retry all read one
	// thing, and the plan the caller can fetch back says what its logs point at.
	plan.AssignMissingEntryIDs(&req.Plan)

	m := &model.Migration{
		ID:            uuid.New().String(),
		Name:          req.Name,
		Description:   req.Description,
		Plan:          req.Plan,
		DBMSOnFailure: req.DBMSOnFailure,
		Status:        "pending",
	}
	if err := dao.CreateMigration(m); err != nil {
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("create migration: "+err.Error()))
	}

	go migrationpkg.DefaultExecutor.Execute(m.ID)

	// The record is on its way out, not back into the database, so the plan it
	// carries is masked like any other read. The caller already holds the plan
	// it sent; echoing the credentials back only puts them somewhere else.
	//
	// Masked in place: the row is already written and the executor reloads it by
	// ID, so nothing downstream reads m.Plan again.
	if err := connsec.MaskPlan(&m.Plan); err != nil {
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("mask plan: "+err.Error()))
	}
	return c.JSON(http.StatusCreated, model.SuccessResponse(*m))
}

// defaultAllLastDays bounds GET /centipede/migration/all when the caller named
// no date filter. The paginated endpoint needs no such default: pageSize
// already bounds it.
//
// A constant rather than configuration: how many days are worth returning
// depends on the deployment's data volume, so this may well belong next to
// rateLimit in the conf api: section later, but nothing needs it there yet.
const defaultAllLastDays = 7

// ListMigration godoc
//
//	@Summary		List migrations (paginated)
//	@Description	Returns a paginated list of migration summaries ordered by created_at DESC.
//	@Description	The response carries planSummary (unit counts per category) in place of the plan itself.
//	@Description	Date filters compare against created_at and are interpreted in the server's local time zone.
//	@Description	dateFrom and dateTo may each be used alone for an open-ended range; dateTo includes its own day.
//	@Tags			migration
//	@Produce		json
//	@Param			status		query		string	false	"Filter by status (pending|running|completed|failed|cancelled)"
//	@Param			dateFrom	query		string	false	"Inclusive lower bound on created_at (yyyy-mm-dd)"
//	@Param			dateTo		query		string	false	"Inclusive upper bound on created_at (yyyy-mm-dd)"
//	@Param			lastDays	query		int		false	"Last N calendar days including today (0-3650; 0 means no limit). Cannot be combined with dateFrom or dateTo"
//	@Param			page		query		int		false	"Page number (default 1)"
//	@Param			pageSize	query		int		false	"Page size (default 20, max 100)"
//	@Success		200			{object}	model.ApiResponse[model.ListResponse[model.MigrationSummary]]
//	@Failure		400			{object}	model.ApiResponse[any]
//	@Failure		500			{object}	model.ApiResponse[any]
//	@Router			/centipede/migration [get]
func ListMigration(c echo.Context) error {
	f, err := model.ParseMigrationListFilter(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse(err.Error()))
	}
	page, pageSize := model.ParsePageParams(c)

	items, total, err := dao.ListMigration(f, page, pageSize)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("list migration: "+err.Error()))
	}

	return c.JSON(http.StatusOK, model.SuccessResponse(model.ListResponse[model.MigrationSummary]{
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Items:    items,
	}))
}

// ListAllMigration godoc
//
//	@Summary		List all migrations without pagination
//	@Description	Returns every matching migration summary ordered by created_at DESC.
//	@Description	The response carries planSummary (unit counts per category) in place of the plan itself.
//	@Description	When none of dateFrom, dateTo and lastDays is given, the last 7 calendar days are applied and the response message says so; pass lastDays=0 to lift the limit.
//	@Description	Date filters compare against created_at and are interpreted in the server's local time zone.
//	@Tags			migration
//	@Produce		json
//	@Param			status		query		string	false	"Filter by status (pending|running|completed|failed|cancelled)"
//	@Param			dateFrom	query		string	false	"Inclusive lower bound on created_at (yyyy-mm-dd)"
//	@Param			dateTo		query		string	false	"Inclusive upper bound on created_at (yyyy-mm-dd)"
//	@Param			lastDays	query		int		false	"Last N calendar days including today (0-3650; 0 means no limit, default 7). Cannot be combined with dateFrom or dateTo"
//	@Success		200			{object}	model.ApiResponse[[]model.MigrationSummary]
//	@Failure		400			{object}	model.ApiResponse[any]
//	@Failure		500			{object}	model.ApiResponse[any]
//	@Router			/centipede/migration/all [get]
func ListAllMigration(c echo.Context) error {
	f, err := model.ParseMigrationListFilter(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse(err.Error()))
	}
	// The one asymmetry between the two list endpoints, kept at the call site
	// rather than inside the parser so that it is visible here.
	defaulted := f.ApplyDefaultWindow(defaultAllLastDays)

	items, err := dao.ListAllMigration(f)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("list all migration: "+err.Error()))
	}

	if defaulted {
		// Said out loud because a silent default leaves the caller unable to
		// tell "no migrations" from "none in the last 7 days".
		return c.JSON(http.StatusOK, model.SuccessResponseWithMessage(items, fmt.Sprintf(
			"no date filter given; defaulted to the last %d days (use lastDays=0 for all)", defaultAllLastDays)))
	}
	return c.JSON(http.StatusOK, model.SuccessResponse(items))
}

// GetMigration godoc
//
//	@Summary		Get a single migration
//	@Description	Returns the Migration record for the given ID.
//	@Tags			migration
//	@Produce		json
//	@Param			migrationId	path		string	true	"Migration ID"
//	@Success		200			{object}	model.ApiResponse[model.Migration]
//	@Failure		404			{object}	model.ApiResponse[any]
//	@Failure		500			{object}	model.ApiResponse[any]
//	@Router			/centipede/migration/{migrationId} [get]
func GetMigration(c echo.Context) error {
	id := c.Param("migrationId")

	m, err := dao.GetMigration(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.JSON(http.StatusNotFound, model.SimpleErrorResponse("migration not found: "+id))
		}
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("get migration: "+err.Error()))
	}

	// Masked rather than left as ciphertext: nothing feeds this response back
	// into the API, so there is nothing to round-trip, and a value that cannot
	// be replayed beats one that can. dao hands back a fresh record each call,
	// so masking it in place affects nothing else.
	if err := connsec.MaskPlan(&m.Plan); err != nil {
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("mask plan: "+err.Error()))
	}
	return c.JSON(http.StatusOK, model.SuccessResponse(*m))
}

// DeleteMigration godoc
//
//	@Summary		Delete a migration
//	@Description	Deletes a migration record. Returns 409 if the migration is currently running — cancel it first.
//	@Tags			migration
//	@Produce		json
//	@Param			migrationId	path	string	true	"Migration ID"
//	@Success		204
//	@Failure		404	{object}	model.ApiResponse[any]
//	@Failure		409	{object}	model.ApiResponse[any]
//	@Failure		500	{object}	model.ApiResponse[any]
//	@Router			/centipede/migration/{migrationId} [delete]
func DeleteMigration(c echo.Context) error {
	id := c.Param("migrationId")

	if err := dao.DeleteMigration(id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.JSON(http.StatusNotFound, model.SimpleErrorResponse("migration not found: "+id))
		}
		if errors.Is(err, dao.ErrMigrationRunning) {
			return c.JSON(http.StatusConflict, model.SimpleErrorResponse("cannot delete a running migration; cancel it first"))
		}
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("delete migration: "+err.Error()))
	}

	return c.NoContent(http.StatusNoContent)
}

// CancelMigration godoc
//
//	@Summary		Cancel a running migration
//	@Description	Signals the running migration to stop after the current item finishes. Returns 404 if the migration is not currently running.
//	@Tags			migration
//	@Produce		json
//	@Param			migrationId	path		string	true	"Migration ID"
//	@Success		200			{object}	model.ApiResponse[model.Migration]
//	@Failure		404			{object}	model.ApiResponse[any]
//	@Failure		500			{object}	model.ApiResponse[any]
//	@Router			/centipede/migration/{migrationId}/cancel [post]
func CancelMigration(c echo.Context) error {
	id := c.Param("migrationId")

	if err := migrationpkg.DefaultExecutor.Cancel(id); err != nil {
		return c.JSON(http.StatusNotFound, model.SimpleErrorResponse("migration is not running: "+id))
	}

	m, err := dao.GetMigration(id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("get migration after cancel: "+err.Error()))
	}

	if err := connsec.MaskPlan(&m.Plan); err != nil {
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("mask plan: "+err.Error()))
	}
	return c.JSON(http.StatusOK, model.SuccessResponse(*m))
}

// RetryMigration godoc
//
//	@Summary		Retry a failed or cancelled migration
//	@Description	Creates a new Migration that re-runs items from the original according to retryMode. DBMS destination tables are DROP-ped before re-execution.
//	@Tags			migration
//	@Accept			json
//	@Produce		json
//	@Param			migrationId	path		string			true	"Original Migration ID"
//	@Param			body		body		model.RetryReq	false	"Retry options (retryMode: partial|full, default: partial)"
//	@Success		201			{object}	model.ApiResponse[model.RetryResponse]
//	@Failure		400			{object}	model.ApiResponse[any]
//	@Failure		404			{object}	model.ApiResponse[any]
//	@Failure		409			{object}	model.ApiResponse[any]
//	@Failure		500			{object}	model.ApiResponse[any]
//	@Router			/centipede/migration/{migrationId}/retry [post]
func RetryMigration(c echo.Context) error {
	id := c.Param("migrationId")

	var req model.RetryReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse("invalid request body: "+err.Error()))
	}

	newMig, err := migrationpkg.DefaultExecutor.Retry(id, req.RetryMode)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.JSON(http.StatusNotFound, model.SimpleErrorResponse("migration not found: "+id))
		}
		msg := err.Error()
		if strings.Contains(msg, "invalid retryMode") {
			return c.JSON(http.StatusBadRequest, model.SimpleErrorResponse(msg))
		}
		if strings.Contains(msg, "must be failed or cancelled") {
			return c.JSON(http.StatusConflict, model.SimpleErrorResponse(msg))
		}
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("retry migration: "+msg))
	}

	// NewMigration carries the plan it was retried with. Masked for the same
	// reason as every other read: the record is already stored and the retry
	// goroutine reloads it by ID.
	if err := connsec.MaskPlan(&newMig.Plan); err != nil {
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("mask plan: "+err.Error()))
	}
	return c.JSON(http.StatusCreated, model.SuccessResponse(model.RetryResponse{
		OriginalMigrationID: id,
		NewMigration:        *newMig,
	}))
}

// ListMigrationLogs godoc
//
//	@Summary		List logs for a migration
//	@Description	Returns paginated MigrationLog entries for the given migration ID, optionally filtered by status.
//	@Tags			migration
//	@Produce		json
//	@Param			migrationId	path		string	true	"Migration ID"
//	@Param			status		query		string	false	"Filter by status (success|failed|skipped)"
//	@Param			page		query		int		false	"Page number (default 1)"
//	@Param			pageSize	query		int		false	"Page size (default 20, max 100)"
//	@Success		200			{object}	model.ApiResponse[model.ListResponse[model.MigrationLog]]
//	@Failure		500			{object}	model.ApiResponse[any]
//	@Router			/centipede/migration/{migrationId}/logs [get]
func ListMigrationLogs(c echo.Context) error {
	id := c.Param("migrationId")
	status := c.QueryParam("status")
	page, pageSize := model.ParsePageParams(c)

	items, total, err := dao.ListMigrationLog(id, status, page, pageSize)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, model.SimpleErrorResponse("list migration logs: "+err.Error()))
	}

	return c.JSON(http.StatusOK, model.SuccessResponse(model.ListResponse[model.MigrationLog]{
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Items:    items,
	}))
}
