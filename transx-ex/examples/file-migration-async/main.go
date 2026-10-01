package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex"
)

func main() {
	var configFile string
	var verbose bool

	flag.StringVar(&configFile, "config", "", "StorageMigrationModel configuration JSON file path (required)")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose progress logging")
	flag.Parse()

	if configFile == "" {
		fmt.Fprintln(os.Stderr, "Error: -config is required")
		flag.Usage()
		os.Exit(1)
	}

	dmm, err := loadMigrationConfig(configFile)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	fmt.Printf("Source      : %s\n", formatLocation(dmm.Source))
	fmt.Printf("Destination : %s\n", formatLocation(dmm.Destination))
	if dmm.Strategy != "" {
		fmt.Printf("Strategy    : %s\n", dmm.Strategy)
	}
	fmt.Println()

	handle := transxex.MigrateStorageAsync(dmm)

	// SIGINT/SIGTERM → cancel
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Fprintln(os.Stderr, "\nCancelling migration...")
		handle.Cancel()
	}()

	// Poll progress in background
	pollStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if verbose {
					p := handle.Progress()
					fmt.Printf("[%s] %.1fs\n", p.Status, p.ElapsedSec)
				}
			case <-pollStop:
				return
			}
		}
	}()

	handle.Wait()
	close(pollStop)

	p := handle.Progress()
	switch p.Status {
	case transxex.StatusDone:
		fmt.Printf("[SUCCESS] Migration completed in %.1fs\n", p.ElapsedSec)
	case transxex.StatusCancelled:
		fmt.Printf("[CANCELLED] Migration cancelled after %.1fs\n", p.ElapsedSec)
		os.Exit(2)
	case transxex.StatusFailed:
		fmt.Printf("[FAILED] Migration failed after %.1fs: %s\n", p.ElapsedSec, p.ErrMessage)
		os.Exit(1)
	}
}

func loadMigrationConfig(configFile string) (transxex.StorageMigrationModel, error) {
	var dmm transxex.StorageMigrationModel
	f, err := os.Open(configFile)
	if err != nil {
		return dmm, fmt.Errorf("failed to open config file: %w", err)
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&dmm); err != nil {
		return dmm, fmt.Errorf("failed to decode JSON: %w", err)
	}
	return dmm, nil
}

func formatLocation(loc transxex.StorageLocation) string {
	switch loc.StorageType {
	case transxex.StorageTypeFilesystem:
		if loc.Filesystem != nil && loc.Filesystem.SSH != nil {
			cfg := loc.Filesystem.SSH
			host := cfg.Host
			if cfg.Port > 0 && cfg.Port != 22 {
				host = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
			}
			return fmt.Sprintf("%s@%s:%s", cfg.Username, host, loc.Path)
		}
		return loc.Path
	case transxex.StorageTypeObjectStorage:
		return loc.Path
	}
	return loc.Path
}
