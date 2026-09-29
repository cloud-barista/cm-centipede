package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	targetmodel "github.com/cloud-barista/cm-centipede/dmdl/target-model"
)

// ── Migration GORM models ────────────────────────────────────────────────────

// Migration represents a migration execution unit persisted to SQLite.
//
// Status values: "pending" | "running" | "completed" | "failed" | "cancelled"
// ValidationStatus values: "passed" | "failed" | "not_run" | "running"
type Migration struct {
	ID          string                               `gorm:"primaryKey"                                         json:"id"`
	Name        string                               `gorm:"index:,column:name,unique;type:text collate nocase" json:"name"        validate:"required"`
	Description string                               `gorm:"column:description"                                 json:"description"`
	Plan        targetmodel.TargetDataMigrationModel `gorm:"column:plan;serializer:json"                        json:"plan"        validate:"required"`

	// DBMSOnFailure is carried on the record rather than only on the request
	// because execution is asynchronous and a retry re-reads the row: the policy
	// has to outlive the call that set it. Empty means DBMSOnFailureCleanup.
	DBMSOnFailure string `gorm:"column:dbms_on_failure" json:"dbmsOnFailure,omitempty"`

	Status        string `gorm:"column:status"         json:"status"`
	StatusMessage string `gorm:"column:status_message" json:"statusMessage,omitempty"`

	TotalItems       int64 `gorm:"column:total_items"       json:"totalItems"`
	ProcessedItems   int64 `gorm:"column:processed_items"   json:"processedItems"`
	FailedItems      int64 `gorm:"column:failed_items"      json:"failedItems"`
	TransferredBytes int64 `gorm:"column:transferred_bytes" json:"transferredBytes"`

	// CurrentObject is the file, object or table being moved right now — one level
	// below ItemPath, which names the folder, bucket or database. Empty when
	// nothing is in flight.
	CurrentObject string `gorm:"column:current_object" json:"currentObject,omitempty"`

	ValidationStatus  string                 `gorm:"column:validation_status"              json:"validationStatus,omitempty"`
	ValidationMessage string                 `gorm:"column:validation_message"             json:"validationMessage,omitempty"`
	ValidationDetails []ValidationDetailItem `gorm:"column:validation_details;serializer:json" json:"validationDetails,omitempty"`

	StartedAt   *time.Time `gorm:"column:started_at"         json:"startedAt,omitempty"`
	CompletedAt *time.Time `gorm:"column:completed_at"       json:"completedAt,omitempty"`
	// CreatedAt is indexed because it is both the list ordering key and the only
	// column the date filters compare against: without the index every list call
	// is a full scan plus a sort.
	CreatedAt time.Time `gorm:"column:created_at;index" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at"       json:"updatedAt"`
}

// MigrationLog records the result of a single item migration within a migration.
//
// Status values: "success" | "failed" | "skipped"
// RollbackStatus values: "success" | "failed" | "skipped" | "" (non-DBMS, or no rollback performed)
type MigrationLog struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"  json:"id"`
	MigrationID string `gorm:"column:migration_id;index" json:"migrationId"`

	// PlanEntryID names the plan entry this item came from
	// (targetmodel.Migration*Model.PlanEntryID). ItemPath alone does not identify
	// it: two entries of one plan legitimately carry the same path — one source
	// fanned out to two destinations, or two sources that scanned the same
	// directory — and their log lines were indistinguishable.
	//
	// It is the only thing about the entry kept here. Which host the item was
	// read from, where it was written and under what rules are all in the plan,
	// which GET /centipede/migration/{id} returns beside these logs; copying any
	// of it into every row would not save the reader that lookup.
	//
	// Not indexed: nothing queries by it. One migration's logs are read whole, by
	// the list endpoint and by the retry that groups them in memory, so an index
	// here would only be paid for on every insert.
	//
	// Empty on rows written before this field existed.
	PlanEntryID string `gorm:"column:plan_entry_id"      json:"planEntryId,omitempty"`

	// ItemPath: absolute file path (SSH) | object key (ObjectStorage) | table/collection name (DBMS)
	ItemPath   string `gorm:"column:item_path"          json:"itemPath"`
	Status     string `gorm:"column:status"             json:"status"`
	ErrorMsg   string `gorm:"column:error_msg"          json:"errorMsg,omitempty"`
	SizeBytes  int64  `gorm:"column:size_bytes"         json:"sizeBytes,omitempty"`
	DurationMs int64  `gorm:"column:duration_ms"        json:"durationMs"`

	// FailedObjectKind and FailedObjectName name the object transx-ex was
	// working on when the item failed — a table, view, routine, file or object —
	// so the failure can be located without reading ErrorMsg. FailedObjectName
	// carries the enclosing database, schema or bucket when transx-ex knew it
	// ("centipede_test.v_customer_stats").
	//
	// Both stay empty when the failure was not tied to a single object: a
	// connection refused, a dump command that failed as a whole.
	FailedObjectKind string `gorm:"column:failed_object_kind" json:"failedObjectKind,omitempty"`
	FailedObjectName string `gorm:"column:failed_object_name" json:"failedObjectName,omitempty"`

	// TargetCreated records that this item's target database did not exist before
	// the migration and does because of it, which is what decides whether a failure
	// dropped the database or emptied it. DBMS items only.
	//
	// It is not a record of who ran a DDL statement. On MongoDB nobody does: a
	// database begins to exist when the restore writes its first collection, and
	// the flag is true there all the same, because dropping is still what undoing
	// the item means.
	TargetCreated bool `gorm:"column:target_created" json:"targetCreated,omitempty"`

	// RollbackStatus reports the cleanup that followed a failure. What it refers
	// to depends on TargetCreated: emptying a database that already existed
	// (false), or dropping one centipede created (true).
	RollbackStatus   string `gorm:"column:rollback_status"    json:"rollbackStatus,omitempty"`
	RollbackErrorMsg string `gorm:"column:rollback_error_msg" json:"rollbackErrorMsg,omitempty"`

	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
}

// ── API request/response models ──────────────────────────────────────────────

// DBMSOnFailure values for CreateMigrationReq.DBMSOnFailure and
// Migration.DBMSOnFailure. Empty is read as DBMSOnFailureCleanup.
const (
	// DBMSOnFailureCleanup undoes a failed database item: one centipede created
	// is dropped whole, one that already existed is emptied by transx-ex.
	DBMSOnFailureCleanup = "cleanup"

	// DBMSOnFailureKeep leaves a failed database exactly as the failure left it,
	// so the partial result can be inspected. Nothing is dropped and nothing is
	// emptied.
	DBMSOnFailureKeep = "keep"
)

// CreateMigrationReq is the request body for POST /centipede/migration.
type CreateMigrationReq struct {
	Name        string                               `json:"name"        validate:"required"`
	Description string                               `json:"description"`
	Plan        targetmodel.TargetDataMigrationModel `json:"plan"        validate:"required"`

	// DBMSOnFailure decides what happens to a DBMS target when one of its
	// database items fails. It is named for the DBMS domain because only that
	// domain has an undo step: a filesystem or object storage transfer leaves
	// whatever it had already written, with or without this field.
	//
	// "cleanup" (the default) undoes the failed item. "keep" leaves it in place
	// for inspection — useful when the failure is in the data rather than in the
	// connection, since cleanup destroys the evidence.
	//
	// Cancellation is not a failure and always cleans up, whatever this says.
	DBMSOnFailure string `json:"dbmsOnFailure,omitempty" validate:"omitempty,oneof=cleanup keep"`
}

// RetryReq is the optional request body for POST /centipede/migration/{id}/retry.
//
// RetryMode: "partial" (default) — re-run only failed items after rollback
//
//	"full"    — drop entire target DB, then re-run all items
type RetryReq struct {
	RetryMode string `json:"retryMode,omitempty"`
}

// RetryResponse is returned when a retry migration is created successfully.
type RetryResponse struct {
	OriginalMigrationID string    `json:"originalMigrationId"`
	NewMigration        Migration `json:"newMigration"`
}

// ── Migration list projection ────────────────────────────────────────────────

// PlanSummary counts the migration units of a plan by category. It is what the
// list endpoints return in place of the plan itself, so that a caller can still
// tell a filesystem migration from a database one without the response carrying
// connection credentials.
type PlanSummary struct {
	FileSystems    int `json:"fileSystems"`
	ObjectStorages int `json:"objectStorages"`
	Databases      int `json:"databases"`
}

// MigrationSummary is the list projection of Migration: every field of the
// record except Plan, which is replaced by PlanSummary.
//
// The list endpoints return this rather than Migration because a list has no
// use for the connection details: a caller scanning migrations wants to know
// what each one moves, and the single-record read is where the plan itself
// belongs. Leaving the plan out also keeps a list call from touching
// credentials at all — the single-record read masks them (see connsec.MaskPlan),
// and what is never loaded needs no masking.
type MigrationSummary struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	PlanSummary PlanSummary `json:"planSummary"`

	DBMSOnFailure string `json:"dbmsOnFailure,omitempty"`

	Status        string `json:"status"`
	StatusMessage string `json:"statusMessage,omitempty"`

	TotalItems       int64  `json:"totalItems"`
	ProcessedItems   int64  `json:"processedItems"`
	FailedItems      int64  `json:"failedItems"`
	TransferredBytes int64  `json:"transferredBytes"`
	CurrentObject    string `json:"currentObject,omitempty"`

	ValidationStatus  string                 `json:"validationStatus,omitempty"`
	ValidationMessage string                 `json:"validationMessage,omitempty"`
	ValidationDetails []ValidationDetailItem `json:"validationDetails,omitempty"`

	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// NewMigrationSummary projects m into a MigrationSummary.
//
// The unit counts are taken from the stored plan without decrypting it:
// encryption substitutes the connection inside each unit and never adds or
// removes a unit, so len() over an encrypted plan gives the same numbers.
func NewMigrationSummary(m *Migration) MigrationSummary {
	p := &m.Plan.TargetDataMigrationModel
	return MigrationSummary{
		ID:          m.ID,
		Name:        m.Name,
		Description: m.Description,
		PlanSummary: PlanSummary{
			FileSystems:    len(p.FileSystems),
			ObjectStorages: len(p.ObjectStorages),
			Databases:      len(p.Databases),
		},
		DBMSOnFailure:     m.DBMSOnFailure,
		Status:            m.Status,
		StatusMessage:     m.StatusMessage,
		TotalItems:        m.TotalItems,
		ProcessedItems:    m.ProcessedItems,
		FailedItems:       m.FailedItems,
		TransferredBytes:  m.TransferredBytes,
		CurrentObject:     m.CurrentObject,
		ValidationStatus:  m.ValidationStatus,
		ValidationMessage: m.ValidationMessage,
		ValidationDetails: m.ValidationDetails,
		StartedAt:         m.StartedAt,
		CompletedAt:       m.CompletedAt,
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
	}
}

// ── Migration list filter ────────────────────────────────────────────────────

const (
	// DateParamLayout is the wire format of dateFrom and dateTo.
	DateParamLayout = "2006-01-02"

	// MaxLastDays bounds lastDays. Ten years is well past any window a caller
	// would ask for and keeps a typo from turning into an unbounded scan.
	MaxLastDays = 3650
)

// MigrationListFilter is the parsed form of the query parameters shared by
// GET /centipede/migration and GET /centipede/migration/all.
//
// The date bounds are kept as yyyy-mm-dd strings rather than time.Time on
// purpose — see applyMigrationFilter in the dao package for why.
type MigrationListFilter struct {
	// Status filters on the status column. Empty means no filter; an unknown
	// value is not rejected and simply matches nothing.
	Status string

	// DateFrom is the inclusive lower bound on created_at. Empty means none.
	DateFrom string

	// DateToExcl is the *exclusive* upper bound on created_at: the requested
	// dateTo plus one day, since dateTo includes its own day. Empty means none.
	DateToExcl string

	// DateParamGiven records that the request carried at least one of dateFrom,
	// dateTo or lastDays — lastDays=0 included, which asks for no window at
	// all. ApplyDefaultWindow leaves such a filter untouched.
	DateParamGiven bool
}

// ParseMigrationListFilter reads the list filter query parameters, following the
// same shape as ParsePageParams.
//
// It returns an error instead of ignoring a malformed parameter: a date filter
// that silently dropped out would return *more* rows than asked for, and extra
// rows look like data rather than like a failure.
//
// The parser is pure — it injects no defaults. GET /centipede/migration/all
// applies its own default window on top of the result.
func ParseMigrationListFilter(c echo.Context) (MigrationListFilter, error) {
	f := MigrationListFilter{Status: c.QueryParam("status")}

	rawFrom := strings.TrimSpace(c.QueryParam("dateFrom"))
	rawTo := strings.TrimSpace(c.QueryParam("dateTo"))
	rawLastDays := strings.TrimSpace(c.QueryParam("lastDays"))

	if rawLastDays != "" && (rawFrom != "" || rawTo != "") {
		return f, errors.New("lastDays cannot be combined with dateFrom or dateTo")
	}

	if rawLastDays != "" {
		n, err := strconv.Atoi(rawLastDays)
		if err != nil || n < 0 || n > MaxLastDays {
			return f, fmt.Errorf("lastDays must be an integer between 0 and %d (got %q)", MaxLastDays, rawLastDays)
		}
		f.DateParamGiven = true
		if n > 0 {
			f.DateFrom, f.DateToExcl = lastDaysWindow(n, time.Now())
		}
		return f, nil
	}

	var from, to time.Time
	if rawFrom != "" {
		var err error
		if from, err = time.ParseInLocation(DateParamLayout, rawFrom, time.Local); err != nil {
			return f, fmt.Errorf("dateFrom must be yyyy-mm-dd (got %q)", rawFrom)
		}
	}
	if rawTo != "" {
		var err error
		if to, err = time.ParseInLocation(DateParamLayout, rawTo, time.Local); err != nil {
			return f, fmt.Errorf("dateTo must be yyyy-mm-dd (got %q)", rawTo)
		}
	}
	if rawFrom != "" && rawTo != "" && from.After(to) {
		return f, errors.New("dateFrom must not be after dateTo")
	}

	if rawFrom != "" {
		f.DateFrom = from.Format(DateParamLayout)
		f.DateParamGiven = true
	}
	if rawTo != "" {
		f.DateToExcl = to.AddDate(0, 0, 1).Format(DateParamLayout)
		f.DateParamGiven = true
	}
	return f, nil
}

// ApplyDefaultWindow fills in a lastDays window when the request named no date
// filter at all, and reports whether it did. A filter that already carries one —
// including the empty window lastDays=0 asks for — is left alone.
//
// It exists for GET /centipede/migration/all, which is otherwise unbounded;
// the paginated endpoint is already bounded by pageSize and does not call it.
func (f *MigrationListFilter) ApplyDefaultWindow(lastDays int) bool {
	if f.DateParamGiven || lastDays <= 0 {
		return false
	}
	f.DateFrom, f.DateToExcl = lastDaysWindow(lastDays, time.Now())
	f.DateParamGiven = true
	return true
}

// lastDaysWindow converts a lastDays count into [from, toExcl) as yyyy-mm-dd
// dates in the server's local zone, counting calendar days and including today:
// lastDays=1 is today alone, lastDays=7 is today and the six days before it.
//
// The bounds land on midnight rather than on "now minus N×24h" so that repeated
// calls on the same day return the same rows — a polling screen must not see
// its window slide underneath it — and so that lastDays and dateFrom/dateTo
// measure on the same calendar-day axis.
func lastDaysWindow(lastDays int, now time.Time) (from, toExcl string) {
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	return today.AddDate(0, 0, -(lastDays - 1)).Format(DateParamLayout),
		today.AddDate(0, 0, 1).Format(DateParamLayout)
}

// ── Validation response models ───────────────────────────────────────────────

// ValidationDetailItem describes the verification result for a single item.
type ValidationDetailItem struct {
	// PlanEntryID names the plan entry this detail is about, the same id
	// MigrationLog carries. Without it a plan of several entries reports every
	// one of them under the same ItemPath — "filesystem" for a whole entry that
	// could not be resolved — and nothing says which.
	PlanEntryID string `json:"planEntryId,omitempty"`
	// ItemPath: file path | object key | DBMS target
	// ("{database}/{table}", "{database}/{table}.{column}", "{database}/{kind}:{name}").
	ItemPath string `json:"itemPath"`
	// Status: "passed" | "failed" | "warning". A warning is reported but does not
	// fail the validation — a character-set difference can be a managed service
	// imposing its own default.
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// ValidationResultResponse is returned by GET /centipede/migration/{id}/validation.
type ValidationResultResponse struct {
	ValidationStatus  string                 `json:"validationStatus"` // "passed" | "failed" | "not_run" | "running"
	ValidationMessage string                 `json:"validationMessage,omitempty"`
	Details           []ValidationDetailItem `json:"details,omitempty"` // list of failed items
}
