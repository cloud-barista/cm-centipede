package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The environment variables gendata recognizes.
//
// They are read straight from the process environment rather than arriving over
// --inputs-file, because they are not infrastructure facts. Inputs answers
// "where are the resources and how do I reach them", which only the provisioning
// environment knows; these answer "how much data do you want", which is the
// operator's intent and carries no secret. tofuenv keeps them in .env, where
// ../scripts/gen-data.sh sources the file with `set -a` and every value is
// exported into gendata's environment for free.
//
// GENDATA_ rather than TF_VAR_: OpenTofu never reads these. In tofuenv's .env a
// TF_VAR_ prefix means "a variable of some tofu module", and borrowing it for a
// value that never reaches tofu would be a lie a reader has to test to disprove.
const (
	EnvDummySizeMB = "GENDATA_DUMMY_SIZE_MB"
	EnvDummySizes  = "GENDATA_DUMMY_SIZES"
	EnvDBSizeMB    = "GENDATA_DB_SIZE_MB"
	EnvMaxFiles    = "GENDATA_DUMMY_MAX_FILES"
	EnvTmpDir      = "GENDATA_TMP_DIR"

	// How many at once - the parallelism of each step. All optional; config.json
	// holds the defaults.
	EnvGenWorkers        = "GENDATA_GEN_WORKERS"        // files generated at once (0 = one per CPU)
	EnvUploadConcurrency = "GENDATA_UPLOAD_CONCURRENCY" // objects uploaded at once
	EnvSFTPConcurrency   = "GENDATA_SFTP_CONCURRENCY"   // files transferred at once
	EnvSFTPConnections   = "GENDATA_SFTP_CONNECTIONS"   // SSH connections they share
	EnvDBWorkers         = "GENDATA_DB_WORKERS"         // DB connections writing bulk rows
)

// EnvKeys lists every recognized variable, in the order they are documented.
//
// gendata --env-keys prints this list, and gen-data.sh compares it against the
// GENDATA_* keys actually present in .env. That is the one weakness of passing
// settings through the environment: a misspelled key is not rejected, it is
// simply never seen, and the run quietly uses the config.json default instead.
// Having the list come from the binary keeps the script from growing a second
// copy that drifts.
func EnvKeys() []string {
	return []string{EnvDummySizeMB, EnvDummySizes, EnvMaxFiles, EnvDBSizeMB, EnvTmpDir,
		EnvGenWorkers, EnvUploadConcurrency, EnvSFTPConcurrency, EnvSFTPConnections, EnvDBWorkers}
}

// TmpDir returns where the dummy files are staged before they are uploaded, or
// "" for the system temp directory.
//
// A key of gendata's own rather than TMPDIR: tofuenv's .env is also sourced by
// the scripts that run tofu inside the runner container, and a host path in
// TMPDIR would reach tofu there, where it does not exist. GENDATA_TMP_DIR is read
// by gendata and nothing else.
//
// The path must be absolute. gen-data.sh runs gendata from its own directory, so
// a relative one would land somewhere the person writing .env did not mean.
func TmpDir() (string, error) {
	v, ok := envValue(EnvTmpDir)
	if !ok {
		return "", nil
	}
	if !filepath.IsAbs(v) {
		return "", fmt.Errorf("%s=%q must be an absolute path", EnvTmpDir, v)
	}
	return filepath.Clean(v), nil
}

// Origins records where each group of settings ended up coming from, so that the
// run manifest can answer "which size was actually in effect" without the reader
// having to replay the config/env precedence by hand.
type Origins struct {
	Dummy    string `json:"dummy"`
	MaxFiles string `json:"maxFiles"`
	Database string `json:"database"`
}

// ApplyEnv overlays the GENDATA_* environment variables onto a loaded config.
//
// Precedence is config.json < environment < command-line flag, the usual order:
// the file holds the defaults a repository ships, the environment holds what one
// deployment wants, and a flag holds what this single run wants.
//
// A variable that is set but unparseable is an error, never a fallback to the
// config.json value. GENDATA_DB_SIZE_MB=1G would otherwise generate nothing at
// all and say nothing about why - and the failure would surface minutes later as
// an empty database rather than immediately as a typo.
func ApplyEnv(c *Config) (Origins, error) {
	o := Origins{Dummy: "config", MaxFiles: "config", Database: "config"}

	var sizes map[string]int
	if v, ok := envValue(EnvDummySizes); ok {
		var err error
		if sizes, err = parseDummySizes(v); err != nil {
			return o, err
		}
		o.Dummy = "env"
	}
	// GENDATA_DUMMY_SIZE_MB is the total over all formats. The formats
	// GENDATA_DUMMY_SIZES names keep their own size, and what is left of the total
	// is spread over the rest - so png=0,gif=0 moves their share to the other six
	// rather than shrinking the total.
	if v, ok := envValue(EnvDummySizeMB); ok {
		total, err := envInt(EnvDummySizeMB, v)
		if err != nil {
			return o, err
		}
		listed := 0
		var rest []string
		for _, name := range DummyFormats() {
			if mb, named := sizes[name]; named {
				listed += mb
			} else {
				rest = append(rest, name)
			}
		}
		if listed > total {
			return o, fmt.Errorf("%s names %d MB, more than the %s=%d total", EnvDummySizes, listed, EnvDummySizeMB, total)
		}
		if len(rest) == 0 && listed != total {
			return o, fmt.Errorf("%s names every format, %d MB in all, which is not the %s=%d total", EnvDummySizes, listed, EnvDummySizeMB, total)
		}
		if len(rest) > 0 {
			c.Dummy.Split(total-listed, rest)
		}
		o.Dummy = "env"
	}
	// Without a total, the named formats override config.json one by one.
	for name, mb := range sizes {
		c.Dummy.Set(name, mb)
	}
	if v, ok := envValue(EnvMaxFiles); ok {
		n, err := envInt(EnvMaxFiles, v)
		if err != nil {
			return o, err
		}
		c.Layout.MaxFiles = n
		o.MaxFiles = "env"
	}
	// The parallelism knobs. 0 means "one per CPU" for generation only; for the
	// others it would mean nothing runs, so it is refused.
	for _, k := range []struct {
		key    string
		dst    *int
		zeroOK bool
	}{
		{EnvGenWorkers, &c.Generate.Workers, true},
		{EnvUploadConcurrency, &c.ObjectStorage.Concurrency, false},
		{EnvSFTPConcurrency, &c.Filesystem.Concurrency, false},
		{EnvSFTPConnections, &c.Filesystem.Connections, false},
		{EnvDBWorkers, &c.Database.Workers, false},
	} {
		v, ok := envValue(k.key)
		if !ok {
			continue
		}
		n, err := envInt(k.key, v)
		if err != nil {
			return o, err
		}
		if n == 0 && !k.zeroOK {
			return o, fmt.Errorf("%s=0: at least 1 is needed", k.key)
		}
		*k.dst = n
	}
	if v, ok := envValue(EnvDBSizeMB); ok {
		mb, err := envInt(EnvDBSizeMB, v)
		if err != nil {
			return o, err
		}
		c.Database.SizeMB = mb
		o.Database = "env"
	}
	return o, nil
}

// envValue reports a variable only when it carries a value. A key present but
// empty counts as unset, which is how .env expresses "left blank on purpose" -
// register-creds.sh blanks the credential keys that way rather than deleting them.
func envValue(key string) (string, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", false
	}
	return v, true
}

func envInt(key, v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a number", key, v)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s=%d is negative (0 disables it)", key, n)
	}
	return n, nil
}

// parseDummySizes parses the per-format form: "csv=100,json=50,zip=0".
func parseDummySizes(v string) (map[string]int, error) {
	known := map[string]bool{}
	for _, name := range DummyFormats() {
		known[name] = true
	}
	sizes := map[string]int{}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, size, found := strings.Cut(part, "=")
		if !found {
			return nil, fmt.Errorf("%s: %q is not format=size (e.g. csv=100)", EnvDummySizes, part)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		mb, err := envInt(EnvDummySizes, strings.TrimSpace(size))
		if err != nil {
			return nil, err
		}
		if !known[name] {
			return nil, fmt.Errorf("%s: unknown format %q (%s)", EnvDummySizes, name,
				strings.Join(DummyFormats(), ", "))
		}
		sizes[name] = mb
	}
	return sizes, nil
}
