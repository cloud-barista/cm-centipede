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
	"time"

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
// Username is empty from cm-beetle v0.6.2 on, whose RDBMS read dropped
// adminUserName; the ref's own username is what callers use.
type DBConfig struct {
	DBType   string // mysql | mariadb — the engines cb-tumblebug provisions
	Host     string
	Port     int
	Username string
}

// SecureTransport is what cb-spider measured on a managed instance's transport
// security, as cm-beetle relays it. It is advice for choosing a TLS mode, not a
// connection setting of its own.
type SecureTransport struct {
	// RecommendedSSLMode is DISABLED, VERIFY_CA, VERIFY_IDENTITY, or empty when
	// the server offers TLS but its CA could not be collected (see CAError).
	RecommendedSSLMode string

	// RequireSecureTransport reports that the server refuses plaintext clients.
	RequireSecureTransport bool

	// CAPEM is the topmost certificate of the chain the server presented. cb-spider
	// collects it without verifying it, so it is trusted on first use. Empty when
	// the server sent none or the collection failed.
	CAPEM string

	// CANotAfter is when CAPEM expires; zero when unknown.
	CANotAfter time.Time

	// CAError says why CAPEM is empty when the server does offer TLS.
	CAError string
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
// AdminUserName is read for cm-beetle before v0.6.2 only; later versions omit it
// (rdbmsmodel.RDBMSInfo), which is why BeetleDBRef requires a username.
type rdbmsInfo struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	DBEngine      string `json:"dbEngine"`
	AdminUserName string `json:"adminUserName"`
	Endpoint      string `json:"endpoint"`
	PublicAccess  bool   `json:"publicAccess"`

	// ConnectionConfig names the CSP holding the instance, which decides what
	// of the source character set a new database can take (see charsetPolicies).
	ConnectionConfig struct {
		ProviderName string `json:"providerName"`
	} `json:"connectionConfig"`
}

// secureTransportInfo mirrors the fields this package reads from
// GET .../rdbms/{rdbmsId}/secure-transport. cm-beetle measures them by
// connecting to the instance through cb-spider on every call.
type secureTransportInfo struct {
	RequireSecureTransport string `json:"requireSecureTransport"` // MySQL/MariaDB: "ON" | "OFF"
	Enforced               bool   `json:"enforced"`               // PostgreSQL; false also when unknown
	CACertificate          struct {
		PEM      string `json:"pem"`
		NotAfter string `json:"notAfter"`
	} `json:"caCertificate"`
	CACertificateError string `json:"caCertificateError"`
	RecommendedSSLMode string `json:"recommendedSSLMode"`
}

// rdbmsDatabaseList mirrors GET .../rdbms/{rdbmsId}/database.
type rdbmsDatabaseList struct {
	Databases []string `json:"databases"`
}

// rdbmsDatabaseCreateReq is the body of POST .../rdbms/{rdbmsId}/database.
// The admin account travels as headers, not here (see adminCredsOf).
type rdbmsDatabaseCreateReq struct {
	DatabaseName string `json:"databaseName"`
	Charset      string `json:"charset,omitempty"`
	Collation    string `json:"collation,omitempty"`
}

// cm-beetle requires both headers on every logical-database call and answers
// 400 when either is missing. It stores neither, so every call repeats them.
const (
	adminUserNameHeader = "X-Admin-User-Name"
	adminPasswordHeader = "X-Admin-User-Password"
)

// adminCreds is the managed instance's admin account, sent as headers.
// The zero value sends none — the plain reads use it.
type adminCreds struct {
	user, password string
}

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

// GetSecureTransport asks cm-beetle how a managed RDBMS instance does transport
// security, for choosing a TLS mode to connect with.
//
// adminUser is the account the connection itself uses, which the caller has
// already resolved; cm-beetle needs it with the password to reach the instance,
// and stores neither.
func GetSecureTransport(ref commonmodel.BeetleDBRef, adminUser string) (*SecureTransport, error) {
	if ref.Password == "" {
		return nil, fmt.Errorf("beetle rdbms %q: admin password is empty", ref.RdbmsID)
	}
	admin := adminCreds{user: strings.TrimSpace(adminUser), password: ref.Password}
	if admin.user == "" {
		return nil, errNoAdminUser(ref)
	}

	var res infoSecureTransportResponse
	if err := getJSONAsAdmin(rdbmsPath(ref)+"/secure-transport", admin, &res); err != nil {
		return nil, err
	}
	raw := res.data()

	st := &SecureTransport{
		RecommendedSSLMode: strings.ToUpper(strings.TrimSpace(raw.RecommendedSSLMode)),
		RequireSecureTransport: raw.Enforced ||
			strings.EqualFold(raw.RequireSecureTransport, "ON") || raw.RequireSecureTransport == "1",
		CAPEM:   strings.TrimSpace(normalizePEM(raw.CACertificate.PEM)),
		CAError: raw.CACertificateError,
	}
	if t, err := time.Parse(time.RFC3339, raw.CACertificate.NotAfter); err == nil {
		st.CANotAfter = t
	}
	return st, nil
}

// ListDatabases returns the logical databases inside a managed RDBMS instance.
//
// This is cm-beetle's own list, the same one CreateDatabase and DropDatabase
// write to. Connecting to the database directly to ask instead can disagree with
// it, and that answer is what decides whether centipede owns a database and must
// drop it after a failure.
func ListDatabases(ref commonmodel.BeetleDBRef) ([]string, error) {
	admin, err := adminCredsOf(ref, nil)
	if err != nil {
		return nil, err
	}
	var res infoRDBMSDatabaseResponse
	path := fmt.Sprintf("/beetle/migration/middleware/ns/%s/rdbms/%s/database", ref.NsID, ref.RdbmsID)
	if err := getJSONAsAdmin(path, admin, &res); err != nil {
		return nil, err
	}
	return res.data().Databases, nil
}

// CreateDatabase creates a logical database inside a managed RDBMS instance.
// A managed database has to be asked for through cm-beetle, which owns the
// instance, so this never falls back to connecting to the database and issuing
// CREATE DATABASE.
//
// charset and collation are the source database's defaults. What the instance's
// engine and CSP cannot take is dropped with a warning (see charsetPolicies),
// and the database then follows the instance default for it. That costs the
// migration nothing: a managed instance is MySQL or MariaDB, whose dumps name a
// character set on every table they create, so the database default is never
// what a restored table ends up with — only tables created later see it.
func CreateDatabase(ref commonmodel.BeetleDBRef, database, charset, collation string) error {
	// One instance read serves both the CSP and engine the character set
	// depends on and the admin user name.
	var res infoRDBMSResponse
	if err := getJSON(rdbmsPath(ref), &res); err != nil {
		return err
	}
	info := res.data()

	admin, err := adminCredsOf(ref, &info)
	if err != nil {
		return err
	}

	cs, co, warning := databaseCharsetFor(info.ConnectionConfig.ProviderName, info.DBEngine, charset, collation)
	if warning != "" {
		log.Warn().
			Str("rdbmsId", ref.RdbmsID).
			Str("database", database).
			Str("charset", charset).
			Str("collation", collation).
			Msg(warning)
	}

	path := rdbmsPath(ref) + "/database"
	body := rdbmsDatabaseCreateReq{DatabaseName: database, Charset: cs, Collation: co}
	return sendJSON(http.MethodPost, path, body, admin)
}

// DropDatabase removes a logical database centipede created through
// CreateDatabase, after its migration failed or was cancelled.
func DropDatabase(ref commonmodel.BeetleDBRef, database string) error {
	admin, err := adminCredsOf(ref, nil)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/beetle/migration/middleware/ns/%s/rdbms/%s/database/%s",
		ref.NsID, ref.RdbmsID, url.PathEscape(database))
	return sendJSON(http.MethodDelete, path, nil, admin)
}

// adminCredsOf returns the admin account for ref's logical-database calls.
// The user name is the ref's; only when it is empty does this fall back to the
// one cm-beetle reports, as the connection itself does (managedDBConnConfig), so
// both always name the same account. That fallback finds a name on cm-beetle
// before v0.6.2 only.
//
// info is the instance when the caller has already read it; nil makes this read
// it, and only when the ref names no user. The read goes to the instance
// directly rather than through GetRDBMSAccessInfo, which fails on an instance
// without an endpoint and needs none of it here.
func adminCredsOf(ref commonmodel.BeetleDBRef, info *rdbmsInfo) (adminCreds, error) {
	if ref.Password == "" {
		return adminCreds{}, fmt.Errorf("beetle rdbms %q: admin password is empty", ref.RdbmsID)
	}
	user := strings.TrimSpace(ref.Username)
	if user == "" {
		if info == nil {
			var res infoRDBMSResponse
			if err := getJSON(rdbmsPath(ref), &res); err != nil {
				return adminCreds{}, err
			}
			raw := res.data()
			info = &raw
		}
		user = strings.TrimSpace(info.AdminUserName)
	}
	if user == "" {
		return adminCreds{}, errNoAdminUser(ref)
	}
	return adminCreds{user: user, password: ref.Password}, nil
}

// errNoAdminUser is the failure when neither ref nor cm-beetle names the admin.
// The phrase "reports no admin user name" is matched by testenv/beetle-db-test.
func errNoAdminUser(ref commonmodel.BeetleDBRef) error {
	return fmt.Errorf(
		"beetle rdbms %q reports no admin user name (cm-beetle v0.6.2+ does not return it); "+
			"set beetleDb.username to the instance admin", ref.RdbmsID)
}

// rdbmsPath is the cm-beetle path of ref's managed instance.
func rdbmsPath(ref commonmodel.BeetleDBRef) string {
	return fmt.Sprintf("/beetle/migration/middleware/ns/%s/rdbms/%s", ref.NsID, ref.RdbmsID)
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

type infoSecureTransportResponse struct {
	Success *bool               `json:"success"`
	Data    secureTransportInfo `json:"data"`
	secureTransportInfo
}

func (r infoSecureTransportResponse) data() secureTransportInfo {
	if r.Success != nil {
		return r.Data
	}
	return r.secureTransportInfo
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
	return call(http.MethodGet, path, nil, adminCreds{}, out)
}

// getJSONAsAdmin is getJSON for the logical-database reads, which additionally
// need the managed instance's admin account.
func getJSONAsAdmin(path string, admin adminCreds, out any) error {
	return call(http.MethodGet, path, nil, admin, out)
}

// sendJSON performs an authenticated write against cm-beetle, discarding the
// response body. body may be nil for requests that carry none.
func sendJSON(method, path string, body any, admin adminCreds) error {
	return call(method, path, body, admin, nil)
}

// call issues one authenticated request against cm-beetle. out may be nil when
// the response body is not needed; the admin headers are sent only when admin
// is set.
func call(method, path string, body any, admin adminCreds, out any) error {
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
	if admin != (adminCreds{}) {
		req.Header.Set(adminUserNameHeader, admin.user)
		req.Header.Set(adminPasswordHeader, admin.password)
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
