package db

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloud-barista/cm-centipede/pkg/api/rest/model"
	"github.com/cloud-barista/cm-centipede/pkg/config"
	"github.com/glebarez/sqlite"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// DB is the global GORM database handle.
var DB *gorm.DB

// Open opens the SQLite database, creating parent directories as needed,
// and runs AutoMigrate for all persistent models.
func Open() error {
	dbPath := resolvedDBPath()

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return fmt.Errorf("create db directory: %w", err)
	}

	gormCfg := &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormLogLevel()),
	}

	db, err := gorm.Open(sqlite.Open(dbPath), gormCfg)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}

	DB = db
	log.Info().Str("path", dbPath).Msg("SQLite database opened")

	return autoMigrate()
}

// Close releases the underlying database connection.
func Close() {
	if DB == nil {
		return
	}
	sqlDB, err := DB.DB()
	if err != nil {
		log.Error().Err(err).Msg("failed to get underlying sql.DB")
		return
	}
	if err := sqlDB.Close(); err != nil {
		log.Error().Err(err).Msg("failed to close SQLite database")
		return
	}
	log.Info().Msg("SQLite database closed")
}

// autoMigrate runs GORM AutoMigrate for all persistent models.
func autoMigrate() error {
	return DB.AutoMigrate(&model.Migration{}, &model.MigrationLog{}, &Meta{})
}

// resolvedDBPath returns the absolute DB path from config,
// falling back to $CENTIPEDE_ROOT/db/cm-centipede.db when the config value is empty.
func resolvedDBPath() string {
	p := config.Conf.DB.Path
	if p == "" {
		return filepath.Join(config.RootPath(), "db", "cm-centipede.db")
	}
	// Resolve ~ prefix
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	return p
}

// gormLogLevel maps the centipede loglevel config to a GORM log level.
// Only "trace"/"debug" reach GORM's Info level, where every SQL statement is
// printed; the default "info" keeps the output to slow queries and errors.
func gormLogLevel() gormlogger.LogLevel {
	switch strings.ToLower(config.Conf.Loglevel) {
	case "trace", "debug":
		return gormlogger.Info
	case "info", "warn":
		return gormlogger.Warn
	case "error", "fatal":
		return gormlogger.Error
	default:
		return gormlogger.Warn
	}
}
