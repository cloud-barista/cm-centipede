// Package beetle reaches cm-beetle for the access details behind a
// beetle* ConnectionRef. cm-beetle fronts cb-tumblebug, so its responses are
// plain text: nothing here decrypts anything.
package beetle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	"github.com/cloud-barista/cm-centipede/pkg/config"
	"github.com/rs/zerolog/log"
)

// SSHConfig is the normalised SSH access info for one migrated node.
// All fields are plain text.
type SSHConfig struct {
	Host       string
	Port       int
	Username   string
	Password   string // empty when key-based auth is used
	PrivateKey string // PEM-encoded private key; empty when password auth is used
}

// OSConfig is the normalised object storage access info, providing
// S3-compatible credentials for the MinIO driver.
type OSConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// DBConfig is the normalised managed-DB access info. Password is not part of it:
// the lookup never returns one, so the caller supplies it from the ref.
type DBConfig struct {
	DBType   string // mysql | mariadb — the engines cb-tumblebug provisions
	Host     string
	Port     int
	Username string
}

// infraInfo mirrors the fields this package reads from
// GET /beetle/migration/ns/{nsId}/infra/{infraId}. The response is a full
// cloudmodel.InfraInfo; only the node list matters here.
type infraInfo struct {
	Node []struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		PublicIP         string `json:"publicIP"`
		SSHPort          int    `json:"sshPort"`
		NodeUserName     string `json:"nodeUserName"`
		NodeUserPassword string `json:"nodeUserPassword"`
		SshKeyId         string `json:"sshKeyId"`
	} `json:"node"`
}

// sshKeyInfo mirrors the fields this package reads from
// GET /beetle/migration/ns/{nsId}/resources/sshKey/{sshKeyId}. That endpoint is
// a transparent proxy onto cb-tumblebug, so the private key comes through
// unmasked.
type sshKeyInfo struct {
	PrivateKey string `json:"privateKey"`
}

// osInfo mirrors the fields this package reads from
// GET /beetle/migration/middleware/ns/{nsId}/objectStorage/{osId}.
type osInfo struct {
	Endpoint  string `json:"endpoint"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	UseSSL    bool   `json:"useSSL"`
}

// rdbmsInfo mirrors the fields this package reads from
// GET /beetle/migration/middleware/ns/{nsId}/rdbms/{rdbmsId}. The response is a
// full RDBMSInfo; only what a client needs to connect matters here.
//
// There is no port of its own: Endpoint carries "host:port", and no password
// either, since the API returns one only when the instance is created.
type rdbmsInfo struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	DBEngine      string `json:"dbEngine"`
	AdminUserName string `json:"adminUserName"`
	Endpoint      string `json:"endpoint"`
	PublicAccess  bool   `json:"publicAccess"`
}

// rdbmsDatabaseList mirrors GET .../rdbms/{rdbmsId}/database.
type rdbmsDatabaseList struct {
	Databases []string `json:"databases"`
}

// rdbmsDatabaseCreateReq is the body of POST .../rdbms/{rdbmsId}/database.
// It has no character set field, which is why a database created here cannot
// inherit the source's (see CreateDatabase).
type rdbmsDatabaseCreateReq struct {
	DatabaseName      string `json:"databaseName"`
	AdminUserPassword string `json:"adminUserPassword"`
}

// adminPasswordHeader carries the managed instance's admin password on the
// logical-database calls. cm-beetle does not store it, so every call repeats it.
const adminPasswordHeader = "X-Admin-User-Password"

// defaultPortOf returns the port an engine listens on when Endpoint omits one.
func defaultPortOf(engine string) int {
	switch strings.ToLower(engine) {
	case "mysql", "mariadb":
		return 3306
	case "postgresql":
		return 5432
	default:
		return 0
	}
}

// GetSSHAccessInfo assembles SSH credentials for one migrated node from two
// cm-beetle calls: the infra read supplies the endpoint and user, and the
// sshKey read supplies the private key.
//
// cm-beetle does not expose cb-tumblebug's ?option=accessinfo, which would
// return all of it at once, so two calls are the only path available.
//
// Errors:
//   - beetle unreachable or non-200 response → wrapped error (caller returns 500)
//   - nodeId not present in the infra → error (caller returns 400)
func GetSSHAccessInfo(ref commonmodel.BeetleSSHRef) (*SSHConfig, error) {
	var infra infoInfraResponse
	path := fmt.Sprintf("/beetle/migration/ns/%s/infra/%s", ref.NsID, ref.InfraID)
	if err := getJSON(path, &infra); err != nil {
		return nil, err
	}

	for _, n := range infra.data().Node {
		if n.ID != ref.NodeID && n.Name != ref.NodeID {
			continue
		}
		if strings.TrimSpace(n.PublicIP) == "" {
			return nil, fmt.Errorf(
				"beetle node %q has no public IP: reaching a private-only node needs a bastion, "+
					"which this API does not report", ref.NodeID)
		}

		cfg := &SSHConfig{
			Host:     n.PublicIP,
			Port:     n.SSHPort,
			Username: n.NodeUserName,
			Password: n.NodeUserPassword,
		}

		if key := strings.TrimSpace(n.SshKeyId); key != "" {
			var sk infoSSHKeyResponse
			p := fmt.Sprintf("/beetle/migration/ns/%s/resources/sshKey/%s", ref.NsID, key)
			if err := getJSON(p, &sk); err != nil {
				return nil, err
			}
			cfg.PrivateKey = normalizePEM(sk.data().PrivateKey)
		}
		return cfg, nil
	}

	return nil, fmt.Errorf("beetle infra %q has no node %q", ref.InfraID, ref.NodeID)
}

// GetObjectStorageInfo returns S3-compatible credentials for one migrated
// object storage.
func GetObjectStorageInfo(ref commonmodel.BeetleOSRef) (*OSConfig, error) {
	var res infoOSResponse
	path := fmt.Sprintf("/beetle/migration/middleware/ns/%s/objectStorage/%s", ref.NsID, ref.OsID)
	if err := getJSON(path, &res); err != nil {
		return nil, err
	}
	raw := res.data()

	return &OSConfig{
		Endpoint:  raw.Endpoint,
		AccessKey: raw.AccessKey,
		SecretKey: raw.SecretKey,
		Bucket:    raw.Bucket,
		Region:    raw.Region,
		UseSSL:    raw.UseSSL,
	}, nil
}

// GetRDBMSAccessInfo returns host/port/user/engine for a managed RDBMS instance.
// The password is not among them — cm-beetle takes one when the instance is
// created and never hands it back — so the caller supplies it from the ref.
func GetRDBMSAccessInfo(ref commonmodel.BeetleDBRef) (*DBConfig, error) {
	var res infoRDBMSResponse
	path := fmt.Sprintf("/beetle/migration/middleware/ns/%s/rdbms/%s", ref.NsID, ref.RdbmsID)
	if err := getJSON(path, &res); err != nil {
		return nil, err
	}
	raw := res.data()

	endpoint := strings.TrimSpace(raw.Endpoint)
	if endpoint == "" {
		// An instance still being provisioned has no endpoint yet. CSP RDS
		// provisioning runs for minutes, so this is a wait, not a failure.
		return nil, fmt.Errorf(
			"beetle rdbms %q has no endpoint yet (status=%q); wait until it is available",
			ref.RdbmsID, raw.Status)
	}

	host, port := endpoint, defaultPortOf(raw.DBEngine)
	if h, p, err := net.SplitHostPort(endpoint); err == nil {
		host = h
		if n, convErr := strconv.Atoi(p); convErr == nil {
			port = n
		}
	}

	if !raw.PublicAccess {
		// The endpoint resolves inside the target VPC only. Say so here rather
		// than leaving the caller with a bare dial timeout.
		log.Warn().
			Str("rdbmsId", ref.RdbmsID).
			Str("host", host).
			Msg("beetle rdbms has publicAccess disabled; its endpoint is reachable only from inside the target network")
	}

	return &DBConfig{
		DBType:   strings.ToLower(raw.DBEngine),
		Host:     host,
		Port:     port,
		Username: raw.AdminUserName,
	}, nil
}

// ListDatabases returns the logical databases inside a managed RDBMS instance.
//
// This is cm-beetle's own list, the same one CreateDatabase and DropDatabase
// write to. Connecting to the database directly to ask instead can disagree with
// it, and that answer is what decides whether centipede owns a database and must
// drop it after a failure.
func ListDatabases(ref commonmodel.BeetleDBRef) ([]string, error) {
	var res infoRDBMSDatabaseResponse
	path := fmt.Sprintf("/beetle/migration/middleware/ns/%s/rdbms/%s/database", ref.NsID, ref.RdbmsID)
	if err := getJSONWithAdminPassword(path, ref.Password, &res); err != nil {
		return nil, err
	}
	return res.data().Databases, nil
}

// CreateDatabase creates a logical database inside a managed RDBMS instance.
// A managed database has to be asked for through cm-beetle, which owns the
// instance, so this never falls back to connecting to the database and issuing
// CREATE DATABASE.
//
// The database takes the instance's default character set — cm-beetle's creation
// request carries only a name and the admin password. That settles nothing for
// the migration either way: a managed instance is MySQL or MariaDB, whose dumps
// name a character set on every table they create, so the database default is
// never what a restored table ends up with.
//
// TODO(beetle): pass the source character set through once cm-beetle's request
// can carry one.
func CreateDatabase(ref commonmodel.BeetleDBRef, database string) error {
	path := fmt.Sprintf("/beetle/migration/middleware/ns/%s/rdbms/%s/database", ref.NsID, ref.RdbmsID)
	body := rdbmsDatabaseCreateReq{DatabaseName: database, AdminUserPassword: ref.Password}
	return sendJSON(http.MethodPost, path, body, "")
}

// DropDatabase removes a logical database centipede created through
// CreateDatabase, after its migration failed or was cancelled.
func DropDatabase(ref commonmodel.BeetleDBRef, database string) error {
	path := fmt.Sprintf("/beetle/migration/middleware/ns/%s/rdbms/%s/database/%s",
		ref.NsID, ref.RdbmsID, url.PathEscape(database))
	return sendJSON(http.MethodDelete, path, nil, ref.Password)
}

// ── HTTP plumbing ────────────────────────────────────────────────────────────

// cm-beetle wraps every payload in model.ApiResponse[T]. Some endpoints are
// transparent proxies onto cb-tumblebug and return the bare object instead, so
// each response type below accepts either shape.

type infoInfraResponse struct {
	Success *bool     `json:"success"`
	Data    infraInfo `json:"data"`
	infraInfo
}

func (r infoInfraResponse) data() infraInfo {
	if r.Success != nil {
		return r.Data
	}
	return r.infraInfo
}

type infoSSHKeyResponse struct {
	Success *bool      `json:"success"`
	Data    sshKeyInfo `json:"data"`
	sshKeyInfo
}

func (r infoSSHKeyResponse) data() sshKeyInfo {
	if r.Success != nil {
		return r.Data
	}
	return r.sshKeyInfo
}

type infoOSResponse struct {
	Success *bool  `json:"success"`
	Data    osInfo `json:"data"`
	osInfo
}

func (r infoOSResponse) data() osInfo {
	if r.Success != nil {
		return r.Data
	}
	return r.osInfo
}

type infoRDBMSResponse struct {
	Success *bool     `json:"success"`
	Data    rdbmsInfo `json:"data"`
	rdbmsInfo
}

func (r infoRDBMSResponse) data() rdbmsInfo {
	if r.Success != nil {
		return r.Data
	}
	return r.rdbmsInfo
}

type infoRDBMSDatabaseResponse struct {
	Success *bool             `json:"success"`
	Data    rdbmsDatabaseList `json:"data"`
	rdbmsDatabaseList
}

func (r infoRDBMSDatabaseResponse) data() rdbmsDatabaseList {
	if r.Success != nil {
		return r.Data
	}
	return r.rdbmsDatabaseList
}

// getJSON performs an authenticated GET against cm-beetle and decodes the body
// into out.
func getJSON(path string, out any) error {
	return call(http.MethodGet, path, nil, "", out)
}

// getJSONWithAdminPassword is getJSON for the logical-database reads, which
// additionally need the managed instance's admin password.
func getJSONWithAdminPassword(path, adminPassword string, out any) error {
	return call(http.MethodGet, path, nil, adminPassword, out)
}

// sendJSON performs an authenticated write against cm-beetle, discarding the
// response body. body may be nil for requests that carry none.
func sendJSON(method, path string, body any, adminPassword string) error {
	return call(method, path, body, adminPassword, nil)
}

// call issues one authenticated request against cm-beetle. out may be nil when
// the response body is not needed; adminPassword is sent only when non-empty.
func call(method, path string, body any, adminPassword string, out any) error {
	endpoint := strings.TrimRight(config.Conf.Beetle.Endpoint, "/")
	target := endpoint + path

	log.Debug().Str("method", method).Str("url", target).Msg("calling cm-beetle API")

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode beetle request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, target, payload)
	if err != nil {
		return fmt.Errorf("build beetle request: %w", err)
	}
	req.SetBasicAuth(config.Conf.Beetle.Username, config.Conf.Beetle.Password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if adminPassword != "" {
		req.Header.Set(adminPasswordHeader, adminPassword)
	}

	resp, err := http.DefaultClient.Do(req) //nolint:noctx
	if err != nil {
		return fmt.Errorf("beetle unreachable: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read beetle response: %w", err)
	}
	// Writes answer 201 as readily as 200, so accept the whole 2xx range.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("beetle returned %d: %s", resp.StatusCode, string(respBody))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("parse beetle response: %w", err)
	}
	return nil
}

// normalizePEM converts literal "\n" escape sequences to actual newlines.
// A PEM private key can arrive with escaped newlines rather than real ones.
func normalizePEM(pem string) string {
	return strings.ReplaceAll(pem, `\n`, "\n")
}
