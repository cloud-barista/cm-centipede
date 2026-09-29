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
	Current    string
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
