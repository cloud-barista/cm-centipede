package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex"
)

func main() {
	var configFile string
	var verbose bool

	// Setting up command-line flags
	flag.StringVar(&configFile, "config", "config-minio-upload.json", "Migration configuration JSON file path")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose logging")
	flag.Parse()

	if verbose {
		fmt.Println("🚀 Starting Object Storage Migration")
		fmt.Printf("📁 Configuration file: %s\n", configFile)
	}

	startTime := time.Now()

	// Load migration configuration
	task, err := loadMigrationConfig(configFile)
	if err != nil {
		log.Fatalf("❌ Error loading migration configuration: %v", err)
	}

	// Run the migration: preCmd and postCmd, when the config sets them on a
	// filesystem side, run before and after the transfer.
	if err := transxex.MigrateStorage(task); err != nil {
		log.Fatalf("❌ Migration failed: %v", err)
	}

	totalDuration := time.Since(startTime)
	if verbose {
		fmt.Printf("✅ Migration completed successfully in %s\n", totalDuration)
	}
}

func loadMigrationConfig(configFile string) (transxex.StorageMigrationModel, error) {
	var task transxex.StorageMigrationModel

	file, err := os.Open(configFile)
	if err != nil {
		return task, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&task); err != nil {
		return task, fmt.Errorf("failed to decode JSON: %w", err)
	}

	return task, nil
}
