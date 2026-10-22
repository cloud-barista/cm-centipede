package migration

// EventKind separates an item's outcome from an observation made while it runs.
//
// EventDone is the zero value, so a ProgressEvent built without naming a kind is
// what it always was: one item, finished.
type EventKind int

const (
	// EventDone reports that an item finished. It advances ProcessedItems and
	// writes one MigrationLog row.
	EventDone EventKind = iota
	// EventTick reports how far the current item has got. It writes no log row
	// and does not advance ProcessedItems; SizeBytes is the item's running total,
	// not a delta.
	EventTick
)

// ProgressEvent is emitted on the progressCh channel: once per migration item
// (directory, bucket, or database) as it finishes, and optionally as it runs.
//
// Status values: "success" | "failed" | "cancelled"
type ProgressEvent struct {
	Kind     EventKind
	ItemPath string
	// Current is the file, object or table the item is on right now — one level
	// below ItemPath. Ticks only.
	Current string
	// Step is the pipeline step SizeBytes counts within, and Steps how many the
	// item's transfer has: a relay's 1/2 stages, its 2/2 sends. Ticks only; zero
	// where the transfer does not report one.
	Step  int
	Steps int
	// StepName names Step when the transfer has its own word for it — a
	// database's "dumping" and "restoring". Empty for a file transfer, whose
	// steps the reader names by position.
	StepName string
	// StepDetail is what the step is doing beyond its name, where the
	// transfer says: a database restore's "indexes deferred: 2 tables", then
	// "rebuilding indexes". Ticks only.
	StepDetail string
	// Rows, Units and UnitsTotal are a database item's: rows written so far,
	// and tables (or collections) finished out of how many. BytesTotal is what
	// SizeBytes counts towards when that is known — the dump a restore reads.
	// Zero where the step cannot tell. Ticks only.
	Rows       int64
	Units      int
	UnitsTotal int
	BytesTotal int64
	Status     string
	SizeBytes  int64
	DurationMs int64
	// TargetCreated reports that this item's target database did not exist before
	// the migration and does because of it, which is what decides whether a
	// failure dropped the database or emptied it. DBMS items only. See
	// model.MigrationLog.TargetCreated for why MongoDB sets it without anyone
	// running a create statement.
	TargetCreated bool
	Err           error
}
