// Package honeybee reaches cm-honeybee for the access details behind a
// honeybee ConnectionRef. Unlike cm-beetle, cm-honeybee returns credentials
// RSA-encrypted, so this package decrypts them (RSA-OAEP/SHA-512) with the
// key pkg/rsautil manages before handing back a HoneybeeConnConfig.
package honeybee

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	commonmodel "github.com/cloud-barista/cm-centipede/dmdl/common-model"
	"github.com/cloud-barista/cm-centipede/pkg/config"
	"github.com/cloud-barista/cm-centipede/pkg/rsautil"
	"github.com/rs/zerolog/log"
)

// ConnType constants used in HoneybeeConnConfig.ConnType.
const (
	ConnTypeSSH           = "ssh"
	ConnTypeObjectStorage = "objectStorage"
	ConnTypeDBMS          = "dbms"
)

// The cm-honeybee source group types this package accepts, spelled as honeybee
// stores them. They are the data-migration groups: one collected domain each,
// and a connection shape to match.
//
// honeybee's other types are rejected on purpose. "onprem" and "csp" are
// infrastructure groups - honeybee itself answers 400 when asked to inspect a
// filesystem, a bucket or a database through them - so there is no collected
// data behind such a connection to migrate. A "csp" connection additionally
// carries no object storage credentials at all (they live on the group, in
// OpenBao), so the fields this package reads would come back empty.
const (
	sourceGroupTypeFS    = "fs"
	sourceGroupTypeDB    = "db"
	sourceGroupTypeMinIO = "minio"
)

// accessTypeSSHTunnel is honeybee's db_access_type / os_access_type value for a
// source reached through an SSH host.
const accessTypeSSHTunnel = "ssh-tunnel"

// HoneybeeConnConfig is the normalised connection config derived from a
// honeybee ConnectionInfo response. Only the fields relevant to ConnType are
// populated; callers should switch on ConnType before reading credentials.
type HoneybeeConnConfig struct {
	ConnType string // "ssh" | "objectStorage" | "dbms"

	// SSH (ConnType = "ssh"). Key auth only: honeybee accepts no password for
	// the source group types centipede reads.
	Host       string
	Port       int
	User       string
	PrivateKey string

	// ObjectStorage (ConnType = "objectStorage")
	Endpoint  string
	AccessKey string
	SecretKey string
	UseSSL    bool

	// OSTunnel is set when os_access_type = "ssh-tunnel": Endpoint is then
	// reachable only from that SSH host, and every connection to it has to be
	// opened from there.
	OSTunnel *SSHTunnel

	// ProviderName and Region come from the connection's SOURCE GROUP rather than
	// from the connection itself, and they are read for one reason: cm-honeybee
	// keeps its provider knowledge to itself. It works out the endpoint, the
	// signing region, the TLS setting and the bucket addressing style on every
	// inspect and hands none of them back — os_endpoint stays whatever the caller
	// typed, and there is no field for the other three at all. So the only way to
	// reach a bucket the way honeybee reaches it is to fetch these two and run the
	// same table here.
	//
	// Region is the group's region_name, the one region a caller states. Whether
	// it also signs the request is the provider table's decision, not this
	// struct's — see ResolveS3Endpoint.
	ProviderName string
	Region       string

	// DBMS (ConnType = "dbms")
	DBType     string
	DBHost     string
	DBPort     int
	DBUsername string
	DBPassword string
	Database   string

	// Transport security for the DBMS connection, passed straight through to
	// transxex.DirectConfig. Read only on the direct path: an SSH-tunnelled
	// connection has no TLS settings, so these are ignored there.
	DBTLSMode  string
	DBTLSCAPEM string

	// DBAuthSource is the MongoDB authSource database. Unlike the TLS fields it
	// is read on both paths — transxex.SSHTunnelConfig carries one too — because
	// which database holds the account is a property of the server, not of how
	// the connection reaches it. Empty → the driver defaults to "admin".
	DBAuthSource string

	// SSH tunnel for DBMS (non-empty when db_access_type = "ssh-tunnel")
	SSHTunnelHost       string
	SSHTunnelPort       int
	SSHTunnelUser       string
	SSHTunnelPrivateKey string
}

// SSHTunnel is the SSH host a connection's endpoint is reached through.
type SSHTunnel struct {
	Host       string
	Port       int
	User       string
	PrivateKey string
}

// honeybeeConnectionInfo mirrors the JSON returned by
// GET /honeybee/connection_info/{connId}.
// Sensitive fields are RSA-OAEP/SHA-512 + base64-encoded in the response.
type honeybeeConnectionInfo struct {
	// SSH fields
	IPAddress  string `json:"ip_address,omitempty"`
	SSHPort    string `json:"ssh_port,omitempty"`
	User       string `json:"user,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`

	// DB fields
	DBType           string `json:"db_type,omitempty"`
	DBAccessType     string `json:"db_access_type,omitempty"` // "direct" | "ssh-tunnel"
	DBName           string `json:"db_name,omitempty"`
	DBHost           string `json:"db_host,omitempty"`
	DBPort           string `json:"db_port,omitempty"`
	DBUsername       string `json:"db_username,omitempty"`
	DBPassword       string `json:"db_password,omitempty"`
	DBConnectTimeout int    `json:"db_connect_timeout,omitempty"`
	DBTLSMode        string `json:"db_tls_mode,omitempty"`
	DBTLSCAPEM       string `json:"db_tls_ca_pem,omitempty"`
	DBAuthSource     string `json:"db_auth_source,omitempty"`

	// SourceGroupID is the bridge to the provider: the connection knows which
	// group it belongs to, and the group is where provider_name and region_name
	// live.
	SourceGroupID string `json:"source_group_id,omitempty"`

	// MinIO / ObjectStorage fields
	OSAccessType      string `json:"os_access_type,omitempty"` // "direct" | "ssh-tunnel"
	OSEndpoint        string `json:"os_endpoint,omitempty"`
	OSAccessKeyId     string `json:"os_access_key_id,omitempty"`
	OSSecretAccessKey string `json:"os_secret_access_key,omitempty"`
	OSUseSSL          bool   `json:"os_use_ssl,omitempty"`
}

// honeybeeSourceGroup is the part of GET /honeybee/source_group/{sgId} this
// package reads: the type that says what kind of source the connection is, and
// the two fields that decide how an object storage bucket is addressed.
type honeybeeSourceGroup struct {
	Type         string `json:"type,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
	RegionName   string `json:"region_name,omitempty"`
}

// getJSON performs a GET against honeybee and decodes the body into out.
func getJSON(url string, out any) error {
	log.Debug().Str("url", url).Msg("calling honeybee API")

	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return fmt.Errorf("honeybee unreachable: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read honeybee response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("honeybee returned %d: %s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("parse honeybee response: %w", err)
	}
	return nil
}

// GetConnectionInfo calls the honeybee connection_info API for the given ref,
// decrypts RSA-OAEP/SHA-512 encrypted fields, and returns a HoneybeeConnConfig.
//
// Every connection costs two calls: the connection, then the source group it
// names. The group is what says which kind of source this is, and the kind has
// to be known before the connection's fields can be read as anything. The
// connection is fetched by id alone, so its group is only known from the reply -
// the two calls cannot be made at once.
//
// An object storage connection reads two more fields from that same group. See
// the ProviderName field: honeybee resolves a bucket's address from the provider
// and hands none of that back, so the provider is fetched and the same table is
// run on this side.
//
// Errors:
//   - honeybee unreachable or non-200 response → wrapped error (caller returns 500)
//   - source group type other than fs/db/minio → error naming the type
//   - RSA decryption failure                   → wrapped error (caller returns 500)
func GetConnectionInfo(ref commonmodel.HoneybeeRef) (*HoneybeeConnConfig, error) {
	endpoint := strings.TrimRight(config.Conf.Honeybee.Endpoint, "/")

	var raw honeybeeConnectionInfo
	if err := getJSON(fmt.Sprintf("%s/honeybee/connection_info/%s", endpoint, ref.ConnectionID), &raw); err != nil {
		return nil, err
	}

	if raw.SourceGroupID == "" {
		return nil, fmt.Errorf("honeybee connection %s names no source group, so its kind cannot be determined", ref.ConnectionID)
	}
	var sg honeybeeSourceGroup
	if err := getJSON(fmt.Sprintf("%s/honeybee/source_group/%s", endpoint, raw.SourceGroupID), &sg); err != nil {
		return nil, fmt.Errorf("get source group %s of honeybee connection %s: %w", raw.SourceGroupID, ref.ConnectionID, err)
	}

	cfg, err := mapToConfig(&raw, &sg, ref.ConnectionID)
	if err != nil {
		return nil, err
	}
	if cfg.ConnType != ConnTypeObjectStorage {
		return cfg, nil
	}

	// Always required, even when the connection states an endpoint of its own:
	// the provider still decides the bucket addressing style, and a Tencent
	// bucket reached with the wrong one fails after the endpoint resolved fine.
	cfg.ProviderName = sg.ProviderName
	cfg.Region = sg.RegionName
	return cfg, nil
}

// mapToConfig converts a raw honeybee response to HoneybeeConnConfig,
// decrypting sensitive fields along the way.
//
// The source group's type is the only discriminator. The connection's own
// fields are not consulted to work out its kind: they are a proxy, and a proxy
// goes wrong silently when its meaning changes upstream. It already did once -
// resource_type was read as "this is a CSP connection" until honeybee started
// setting it on on-premise connections too, at which point an on-prem
// kubernetes connection, which has no SSH host at all, mapped to an SSH config
// with an empty host and no error.
func mapToConfig(raw *honeybeeConnectionInfo, sg *honeybeeSourceGroup, connID string) (*HoneybeeConnConfig, error) {
	cfg := &HoneybeeConnConfig{}

	switch sg.Type {
	case sourceGroupTypeFS:
		if err := fillSSH(cfg, raw, connID); err != nil {
			return nil, err
		}

	case sourceGroupTypeDB:
		if err := fillDBMS(cfg, raw, connID); err != nil {
			return nil, err
		}

	case sourceGroupTypeMinIO:
		if err := fillObjectStorage(cfg, raw, connID); err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf(
			"honeybee connection %s belongs to a %q source group; "+
				"centipede migrates data only from %q, %q or %q groups",
			connID, sg.Type, sourceGroupTypeFS, sourceGroupTypeDB, sourceGroupTypeMinIO)
	}

	return cfg, nil
}

// decryptSSH reads the SSH host of a connection: its address and the decrypted
// user and private key. The key is required, because SSH is authenticated by key
// only. A password honeybee may still hold for an older connection is not read.
func decryptSSH(raw *honeybeeConnectionInfo, connID string) (*SSHTunnel, error) {
	port, _ := strconv.Atoi(raw.SSHPort)
	t := &SSHTunnel{Host: raw.IPAddress, Port: port}

	var err error
	if t.User, err = decryptField(raw.User); err != nil {
		return nil, fmt.Errorf("decrypt ssh user: %w", err)
	}
	pk, err := decryptField(raw.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt ssh private_key: %w", err)
	}
	// "-" is honeybee's placeholder for "no key".
	if pk == "" || pk == "-" {
		return nil, fmt.Errorf("honeybee connection %s has no ssh private_key; "+
			"SSH access is authenticated by key only", connID)
	}
	t.PrivateKey = pk
	return t, nil
}

// fillSSH populates SSH-type fields, decrypting user/private_key.
func fillSSH(cfg *HoneybeeConnConfig, raw *honeybeeConnectionInfo, connID string) error {
	cfg.ConnType = ConnTypeSSH

	t, err := decryptSSH(raw, connID)
	if err != nil {
		return err
	}
	cfg.Host = t.Host
	cfg.Port = t.Port
	cfg.User = t.User
	cfg.PrivateKey = t.PrivateKey
	return nil
}

// fillObjectStorage populates ObjectStorage-type fields, decrypting access/secret keys.
// When os_access_type = "ssh-tunnel", the SSH host the endpoint is reached
// through is populated as well.
func fillObjectStorage(cfg *HoneybeeConnConfig, raw *honeybeeConnectionInfo, connID string) error {
	cfg.ConnType = ConnTypeObjectStorage
	cfg.Endpoint = raw.OSEndpoint
	cfg.UseSSL = raw.OSUseSSL
	// Region is not a connection field: it comes from the source group, and
	// GetConnectionConfig fills it in after this.

	var err error
	if cfg.AccessKey, err = decryptField(raw.OSAccessKeyId); err != nil {
		return fmt.Errorf("decrypt os_access_key_id: %w", err)
	}
	if cfg.SecretKey, err = decryptField(raw.OSSecretAccessKey); err != nil {
		return fmt.Errorf("decrypt os_secret_access_key: %w", err)
	}

	if raw.OSAccessType == accessTypeSSHTunnel {
		if raw.IPAddress == "" {
			return fmt.Errorf("honeybee connection %s is %s but names no ip_address", connID, accessTypeSSHTunnel)
		}
		if cfg.OSTunnel, err = decryptSSH(raw, connID); err != nil {
			return err
		}
	}
	return nil
}

// fillDBMS populates DBMS-type fields, decrypting db_username and db_password.
// When db_access_type = "ssh-tunnel", SSH tunnel credentials are also populated.
func fillDBMS(cfg *HoneybeeConnConfig, raw *honeybeeConnectionInfo, connID string) error {
	cfg.ConnType = ConnTypeDBMS
	cfg.DBType = raw.DBType
	cfg.DBHost = raw.DBHost
	cfg.Database = raw.DBName
	// Not decrypted: honeybee RSA-wraps credentials, and a CA certificate is a
	// public value. So is the authSource: honeybee stores it in plain text for
	// the same reason.
	cfg.DBTLSMode = raw.DBTLSMode
	cfg.DBTLSCAPEM = raw.DBTLSCAPEM
	cfg.DBAuthSource = raw.DBAuthSource

	port, _ := strconv.Atoi(raw.DBPort)
	cfg.DBPort = port

	var err error
	if cfg.DBUsername, err = decryptField(raw.DBUsername); err != nil {
		return fmt.Errorf("decrypt db_username: %w", err)
	}
	if cfg.DBPassword, err = decryptField(raw.DBPassword); err != nil {
		return fmt.Errorf("decrypt db_password: %w", err)
	}

	// SSH tunnel credentials for ssh-tunnel access type
	if raw.DBAccessType == accessTypeSSHTunnel && raw.IPAddress != "" {
		t, err := decryptSSH(raw, connID)
		if err != nil {
			return err
		}
		cfg.SSHTunnelHost = t.Host
		cfg.SSHTunnelPort = t.Port
		cfg.SSHTunnelUser = t.User
		cfg.SSHTunnelPrivateKey = t.PrivateKey
	}

	return nil
}

// decryptField base64-decodes then RSA-OAEP/SHA-512 decrypts a single
// honeybee-encrypted field. Empty input is returned as-is.
func decryptField(encrypted string) (string, error) {
	if encrypted == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	plain, err := rsautil.DecryptWithPrivateKey(raw, rsautil.PrivKey)
	if err != nil {
		return "", fmt.Errorf("rsa decrypt: %w", err)
	}
	return string(plain), nil
}
