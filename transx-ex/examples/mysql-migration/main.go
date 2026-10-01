package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex"
)

func main() {
	var configFile string
	var scopeOverride string
	var async bool
	var inspect bool

	flag.StringVar(&configFile, "config", "config.json", "Migration configuration JSON file path")
	flag.StringVar(&scopeOverride, "scope", "", "Override scope: full | schema-only | data-only")
	flag.BoolVar(&async, "async", false, "Run migration asynchronously with progress polling")
	flag.BoolVar(&inspect, "inspect", false, "Inspect source DB before migration")
	flag.Parse()

	startTime := time.Now()

	if !filepath.IsAbs(configFile) {
		wd, err := os.Getwd()
		if err == nil {
			configFile = filepath.Join(wd, configFile)
		}
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		log.Fatalf("Failed to read config file %s: %v", configFile, err)
	}

	var dmm transxex.DBMSMigrationModel
	if err := json.Unmarshal(data, &dmm); err != nil {
		log.Fatalf("Failed to parse config JSON: %v", err)
	}

	if scopeOverride != "" {
		dmm.Scope = scopeOverride
	}
	if dmm.Scope == "" {
		dmm.Scope = transxex.ScopeFull
	}

	if err := transxex.ValidateDBMS(dmm); err != nil {
		log.Fatalf("Invalid migration configuration: %v", err)
	}

	src := dmm.Source
	dst := dmm.Destination

	printHeader()
	fmt.Printf("  Source:      %s (host=%s port=%d db=%s)\n",
		src.DBMSType, src.Direct.Host, src.Direct.Port, src.Database)
	fmt.Printf("  Destination: %s (host=%s port=%d db=%s)\n",
		dst.DBMSType, dst.Direct.Host, dst.Direct.Port, dst.Database)
	fmt.Printf("  Scope:       %s\n", dmm.Scope)
	fmt.Printf("  Mode:        %s\n", mode(async))
	fmt.Printf("  Rollback:    %v\n", dmm.RollbackOnFailure)
	fmt.Println()

	if inspect {
		fmt.Println(">> Inspecting source database...")
		printInspect(src)
		fmt.Println()
	}

	fmt.Println(">> Running migration...")
	var migErr error
	if async {
		migErr = runAsync(dmm)
	} else {
		migErr = transxex.MigrateDBMS(dmm)
	}

	elapsed := time.Since(startTime)

	fmt.Println()
	printSeparator()
	fmt.Println("  Migration Summary")
	printSeparator()
	if migErr != nil {
		fmt.Printf("  Result:   FAILED — %v\n", migErr)
		fmt.Printf("  Duration: %s\n", elapsed.Round(time.Millisecond))
		os.Exit(1)
	}
	fmt.Printf("  Result:   SUCCESS\n")
	fmt.Printf("  Duration: %s\n", elapsed.Round(time.Millisecond))
	fmt.Println()
}

// =============================================================================
// Async runner
// =============================================================================

func runAsync(dmm transxex.DBMSMigrationModel) error {
	handle := transxex.MigrateDBMSAsync(dmm)

	waitCh := make(chan error, 1)
	go func() { waitCh <- handle.Wait() }()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case err := <-waitCh:
			p := handle.Progress()
			fmt.Printf("\r  %-90s\n",
				fmt.Sprintf("Status: %-12s | Elapsed: %5.1fs | Tables: %d/%d",
					p.Status, p.ElapsedSec, p.TablesDone, p.TablesTotal))
			return err
		case <-ticker.C:
			p := handle.Progress()
			fmt.Printf("\r  Status: %-12s | Elapsed: %5.1fs | Bytes: %-10d | Tables: %d/%d  ",
				p.Status, p.ElapsedSec, p.BytesTransferred, p.TablesDone, p.TablesTotal)
		}
	}
}

// =============================================================================
// Inspect helper
// =============================================================================

func printInspect(loc transxex.DBMSLocation) {
	info, err := transxex.InspectDBMS(loc)
	if err != nil {
		fmt.Printf("  Inspect failed: %v\n", err)
		return
	}
	fmt.Printf("  Server Version: %s\n", info.ServerVersion)
	fmt.Printf("  Database:       %s\n", info.Database)
	fmt.Printf("  Total Size:     %s\n", fmtBytes(info.TotalSize))
	fmt.Printf("  Tables:         %d\n", info.TableCount)
	if len(info.Tables) > 0 {
		fmt.Println()
		fmt.Printf("  %-25s %10s %12s %12s\n", "Table", "Rows", "Data", "Index")
		fmt.Printf("  %-25s %10s %12s %12s\n", "-----", "----", "----", "-----")
		for _, t := range info.Tables {
			fmt.Printf("  %-25s %10d %12s %12s\n",
				t.Name, t.RowCount, fmtBytes(t.DataSize), fmtBytes(t.IndexSize))
		}
	}
}

// =============================================================================
// Formatting helpers
// =============================================================================

func fmtBytes(b int64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/1024/1024)
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func mode(async bool) string {
	if async {
		return "async (polling)"
	}
	return "sync"
}

func printHeader() {
	printSeparator()
	fmt.Println("  MySQL Direct-to-Direct Migration  (transx-ex)")
	printSeparator()
}

func printSeparator() {
	fmt.Println("  ============================================")
}
