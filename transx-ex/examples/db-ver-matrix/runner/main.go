// Version-matrix migration runner.
//
// It reads a transx-ex DBMSMigrationModel from a JSON config file, runs the
// migration, and writes a machine-readable result file the shell script turns
// into one cell of the version matrix.
//
// One runner serves every engine: the engine is whatever the config file names
// in dbmsType, so mysql-ver-matrix-migration.sh and its three siblings all build
// and call this directory. Each script keeps its own config and result under
// .run/<engine>/ so concurrent runs never share a file.
//
// The matrix only ever migrates a whole database, so the scope is fixed to
// "full" and there is no flag to change it.
//
// Usage:
//
//	go build -o runner .
//	./runner --config=config.json --result=result.json [--async] [--skip-version-check]
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	transxex "github.com/cloud-barista/cm-centipede/transx-ex"
)

// result is what the shell script reads back. Every field is filled in even
// when the migration fails, so a failed cell still reports the versions it ran
// against and how long it took to give up.
type result struct {
	OK            bool    `json:"ok"`
	ErrorKind     string  `json:"errorKind,omitempty"`
	Error         string  `json:"error,omitempty"`
	ElapsedSec    float64 `json:"elapsedSec"`
	SourceVersion string  `json:"sourceVersion,omitempty"`
	TargetVersion string  `json:"targetVersion,omitempty"`
	AccessType    string  `json:"accessType"`
}

func main() {
	var (
		configPath string
		resultPath string
		async      bool
		skipVer    bool
	)
	flag.StringVar(&configPath, "config", "config.json", "migration configuration JSON file")
	flag.StringVar(&resultPath, "result", "", "path the JSON result is written to (optional)")
	flag.BoolVar(&async, "async", false, "run asynchronously and poll progress")
	flag.BoolVar(&skipVer, "skip-version-check", false, "skip the server version compatibility check")
	flag.Parse()

	data, err := os.ReadFile(configPath)
	if err != nil {
		exit(resultPath, result{ErrorKind: "config", Error: err.Error()})
	}

	var dmm transxex.DBMSMigrationModel
	if err := json.Unmarshal(data, &dmm); err != nil {
		exit(resultPath, result{ErrorKind: "config", Error: err.Error()})
	}
	// The matrix migrates whole databases only — a config that says otherwise is
	// overridden rather than honoured.
	dmm.Scope = transxex.ScopeFull
	if skipVer {
		dmm.SkipVersionCheck = true
	}

	res := result{AccessType: dmm.Source.AccessType}

	if err := transxex.ValidateDBMS(dmm); err != nil {
		res.ErrorKind = "invalid-model"
		res.Error = err.Error()
		exit(resultPath, res)
	}

	// Server versions are reported for the matrix, not used for a decision: a
	// version that cannot be read leaves the field empty and the migration goes
	// ahead, which is what transx-ex itself does with an unreadable version.
	res.SourceVersion, _ = serverVersion(dmm.Source)
	res.TargetVersion, _ = serverVersion(dmm.Destination)

	fmt.Printf("    source     : %s %s (%s)\n", dmm.Source.DBMSType, res.SourceVersion, dmm.Source.AccessType)
	fmt.Printf("    target     : %s %s (%s)\n", dmm.Destination.DBMSType, res.TargetVersion, dmm.Destination.AccessType)
	fmt.Printf("    options    : rollback=%v   skipVersionCheck=%v\n",
		dmm.RollbackOnFailure, dmm.SkipVersionCheck)

	started := time.Now()
	var migErr error
	if async {
		migErr = runAsync(dmm)
	} else {
		migErr = transxex.MigrateDBMS(dmm)
	}
	res.ElapsedSec = time.Since(started).Seconds()

	if migErr != nil {
		res.ErrorKind = classify(migErr)
		res.Error = migErr.Error()
		exit(resultPath, res)
	}
	res.OK = true
	exit(resultPath, res)
}

// runAsync launches the migration in the background and prints progress until
// it settles.
func runAsync(dmm transxex.DBMSMigrationModel) error {
	h := transxex.MigrateDBMSAsync(dmm)
	for {
		st := h.Status()
		if st == transxex.StatusDone || st == transxex.StatusFailed {
			break
		}
		p := h.Progress()
		fmt.Printf("\r    progress   : %-12s tables=%d/%d  %.0fs",
			p.Status, p.TablesDone, p.TablesTotal, p.ElapsedSec)
		time.Sleep(time.Second)
	}
	fmt.Println()
	return h.Wait()
}

// serverVersion reads the version string a location's server reports.
func serverVersion(loc transxex.DBMSLocation) (string, error) {
	v, err := transxex.ServerVersion(loc)
	if err != nil {
		return "", err
	}
	return v, nil
}

// classify names the condition a failure represents so the matrix can tell an
// expected refusal (a downgrade transx-ex blocks on purpose) from a real
// failure.
func classify(err error) string {
	var (
		downgrade *transxex.VersionDowngradeError
		notEmpty  *transxex.TargetNotEmptyError
		charset   *transxex.CharsetIncompatibleError
		notFound  *transxex.TargetDatabaseNotFoundError
		migration *transxex.MigrationError
	)
	switch {
	case errors.As(err, &downgrade):
		return "version-downgrade"
	case errors.As(err, &notEmpty):
		return "target-not-empty"
	case errors.As(err, &charset):
		return "charset-incompatible"
	case errors.As(err, &notFound):
		return "target-database-not-found"
	case errors.As(err, &migration):
		return "migration-" + migration.Stage
	default:
		return "other"
	}
}

// exit writes the result file (when asked for), prints a one-line summary, and
// ends the process: 0 when the migration succeeded, 1 otherwise.
func exit(resultPath string, res result) {
	if resultPath != "" {
		b, err := json.MarshalIndent(res, "", "  ")
		if err == nil {
			if err := os.WriteFile(resultPath, b, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "    write result file: %v\n", err)
			}
		}
	}
	if res.OK {
		fmt.Printf("    result     : SUCCESS (%.1fs)\n", res.ElapsedSec)
		os.Exit(0)
	}
	fmt.Printf("    result     : FAILED [%s] %s\n", res.ErrorKind, res.Error)
	os.Exit(1)
}
