package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Inputs holds the infrastructure-derived values one run needs: where the
// bucket, the VM and the databases are, and the credentials to reach them.
//
// gendata derives none of it. The provisioning environment does, because that is
// where the knowledge lives - tofuenv/scripts/gen-data.sh reads `tofu output`
// and OpenBao - and hands the result over as JSON on stdin. The tags below are
// therefore a contract with that script: renaming a field means renaming a tag.
//
// Secrets reach this struct and go no further: they are read once, kept in
// memory, and never written back out.
type Inputs struct {
	// bucket
	BucketName string `json:"BucketName"`
	Region     string `json:"Region"`
	AccessKey  string `json:"AccessKey"` // object storage; from OpenBao, never from .env
	SecretKey  string `json:"SecretKey"`
	// vm (filesystem)
	VMHost     string `json:"VMHost"`
	VMUser     string `json:"VMUser"`
	VMKeyPath  string `json:"VMKeyPath"`
	VMDataPath string `json:"VMDataPath"` // vm module "data_path" output; overrides config.json basePath
	// database
	DBUser       string `json:"DBUser"`
	DBPassword   string `json:"DBPassword"`
	DBName       string `json:"DBName"`
	MySQLHost    string `json:"MySQLHost"`
	MariaDBHost  string `json:"MariaDBHost"`
	PostgresHost string `json:"PostgresHost"`
	MongoHost    string `json:"MongoHost"`
	MySQLPort    string `json:"MySQLPort"`
	MariaDBPort  string `json:"MariaDBPort"`
	PostgresPort string `json:"PostgresPort"`
	MongoPort    string `json:"MongoPort"`
	// How much the provisioned targets can hold, in GB. These belong here rather
	// than in config.json for the same reason every other field does: they are
	// facts about the infrastructure, which the provisioning environment knows and
	// gendata does not. gendata uses them to refuse a size request that would fill
	// the disk - a full managed instance is not a failed run, it is an instance
	// that has to be destroyed and provisioned again.
	//
	// 0 means unknown, and the check is skipped rather than guessed at.
	VMVolumeGB  int `json:"VMVolumeGB"`
	DBStorageGB int `json:"DBStorageGB"`
}

// LoadInputsFile reads Inputs as JSON from a file, or from stdin when path is
// "-". Both provisioning environments pass "-", and that is the point of it:
// Inputs carries the database password and the object-storage secret key, so a
// real file would leave both on disk after the run - outliving OpenBao itself,
// since discarding the store does not touch it. A pipe keeps them in memory, and
// unlike argv it does not put them in ps output.
//
// A path is still accepted, for a hand-written inputs file while debugging. Put
// no secrets in one.
//
// Engine hosts that are absent stay empty, and the caller skips that engine with
// a warning - which is exactly what "this CSP only has mysql" should look like.
func LoadInputsFile(path string) (*Inputs, error) {
	var (
		data []byte
		err  error
		src  = path
	)
	if path == "-" {
		src = "stdin"
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("inputs from %s: %w", src, err)
	}
	in := &Inputs{}
	if err := json.Unmarshal(data, in); err != nil {
		return nil, fmt.Errorf("inputs from %s: %w", src, err)
	}
	if in.MySQLPort == "" {
		in.MySQLPort = "3306"
	}
	if in.MariaDBPort == "" {
		in.MariaDBPort = "3306"
	}
	if in.PostgresPort == "" {
		in.PostgresPort = "5432"
	}
	if in.MongoPort == "" {
		in.MongoPort = "27017"
	}
	if in.DBUser == "" {
		in.DBUser = "dbadmin"
	}
	if in.DBName == "" {
		in.DBName = "testdb"
	}
	return in, nil
}
