package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

const ModuleName = "cm-centipede"

// Conf is the global configuration instance.
var Conf CentipedeConfig

type CentipedeConfig struct {
	Self       SelfConfig       `mapstructure:"self"`
	Honeybee   HoneybeeConfig   `mapstructure:"honeybee"`
	Beetle     BeetleConfig     `mapstructure:"beetle"`
	Tumblebug  TumblebugConfig  `mapstructure:"tumblebug"`
	API        APIConfig        `mapstructure:"api"`
	DB         DBConfig         `mapstructure:"db"`
	Encryption EncryptionConfig `mapstructure:"encryption"`
	Logfile    LogfileConfig    `mapstructure:"logfile"`
	Loglevel   string           `mapstructure:"loglevel"`
	Logwriter  string           `mapstructure:"logwriter"`
	Node       NodeConfig       `mapstructure:"node"`
}

type SelfConfig struct {
	Endpoint string `mapstructure:"endpoint"`
}

type HoneybeeConfig struct {
	Endpoint       string `mapstructure:"endpoint"`
	PrivateKeyPath string `mapstructure:"privateKeyPath"`
}

// BeetleConfig holds the cm-beetle REST endpoint and Basic Auth credentials.
// cm-beetle is the only reference source for migration targets: node SSH access
// and object storage both resolve through it.
type BeetleConfig struct {
	Endpoint string `mapstructure:"endpoint"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

// TumblebugConfig holds the cb-tumblebug REST endpoint used during object
// storage transfers only. transx-ex reaches managed buckets through
// cb-tumblebug's ObjectStorage API, so the endpoint is still needed even though
// cb-tumblebug is not a selectable connection source.
type TumblebugConfig struct {
	Endpoint string `mapstructure:"endpoint"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

type APIConfig struct {
	Allow     AllowConfig `mapstructure:"allow"`
	Auth      AuthConfig  `mapstructure:"auth"`
	Username  string      `mapstructure:"username"`
	Password  string      `mapstructure:"password"`
	RateLimit int         `mapstructure:"rateLimit"`
}

type AllowConfig struct {
	Origins string `mapstructure:"origins"`
}

type AuthConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Mode    string `mapstructure:"mode"`
}

type DBConfig struct {
	Path string `mapstructure:"path"`
}

type EncryptionConfig struct {
	SecretKey string `mapstructure:"secretKey"`
}

type LogfileConfig struct {
	Path       string `mapstructure:"path"`
	MaxSize    int    `mapstructure:"maxsize"`
	MaxBackups int    `mapstructure:"maxbackups"`
	MaxAge     int    `mapstructure:"maxage"`
}

type NodeConfig struct {
	Env string `mapstructure:"env"`
}

// ExpandTilde expands a leading "~/" to the current user's home directory.
// Go does not expand tilde automatically; paths from config/env must be
// processed through this function before being passed to os/filepath calls.
func ExpandTilde(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// RootPath returns the root directory for cm-centipede data.
// Uses CENTIPEDE_ROOT env var if set, otherwise defaults to ~/.cm-centipede.
func RootPath() string {
	if root := os.Getenv("CENTIPEDE_ROOT"); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cm-centipede"
	}
	return filepath.Join(home, ".cm-centipede")
}

// Init loads configuration from the YAML file and applies environment variable overrides.
func Init() {
	v := viper.New()
	v.SetConfigName("cm-centipede")
	v.SetConfigType("yaml")
	v.AddConfigPath(filepath.Join(RootPath(), "conf"))
	v.AddConfigPath("./conf")

	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Explicit env bindings. AutomaticEnv alone does not bind a key that never
	// appears in a config file, so every override is registered by name here.
	_ = v.BindEnv("centipede.self.endpoint", "CENTIPEDE_SELF_ENDPOINT")
	_ = v.BindEnv("centipede.honeybee.endpoint", "CENTIPEDE_HONEYBEE_ENDPOINT")
	_ = v.BindEnv("centipede.honeybee.privateKeyPath", "CENTIPEDE_HONEYBEE_PRIVATE_KEY_PATH")
	_ = v.BindEnv("centipede.beetle.endpoint", "CENTIPEDE_BEETLE_ENDPOINT")
	_ = v.BindEnv("centipede.beetle.username", "CENTIPEDE_BEETLE_USERNAME")
	_ = v.BindEnv("centipede.beetle.password", "CENTIPEDE_BEETLE_PASSWORD")
	_ = v.BindEnv("centipede.tumblebug.endpoint", "CENTIPEDE_TUMBLEBUG_ENDPOINT")
	_ = v.BindEnv("centipede.tumblebug.username", "CENTIPEDE_TUMBLEBUG_USERNAME")
	_ = v.BindEnv("centipede.tumblebug.password", "CENTIPEDE_TUMBLEBUG_PASSWORD")
	_ = v.BindEnv("centipede.api.auth.enabled", "CENTIPEDE_API_AUTH_ENABLED")
	_ = v.BindEnv("centipede.db.path", "CENTIPEDE_DB_PATH")
	_ = v.BindEnv("centipede.encryption.secretKey", "CENTIPEDE_ENCRYPTION_SECRET_KEY")
	_ = v.BindEnv("centipede.loglevel", "CENTIPEDE_LOGLEVEL")

	if err := v.ReadInConfig(); err != nil {
		// Config file is optional; environment variables and defaults still apply.
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			panic("failed to read config file: " + err.Error())
		}
	}

	// AutomaticEnv and BindEnv are only consulted by v.Get, while UnmarshalKey
	// decodes the raw config tree — so without this loop every environment
	// override above would be silently ignored. Reading each key through Get and
	// writing it back materialises the resolved value (env first, YAML second)
	// into the override layer that UnmarshalKey does read.
	for _, key := range v.AllKeys() {
		v.Set(key, v.Get(key))
	}

	if err := v.UnmarshalKey("centipede", &Conf); err != nil {
		panic("failed to unmarshal config: " + err.Error())
	}
}
