package commonmodel

// ConnectionSource identifies which external system holds the connection info,
// or "inline" when the credentials are carried in the request body.
type ConnectionSource string

const (
	// Unified reference — cm-honeybee (ssh|objectStorage|dbms distinguished by ConnType)
	ConnectionSourceHoneybee ConnectionSource = "honeybee"

	// Reference type — cm-beetle
	ConnectionSourceBeetleSSH           ConnectionSource = "beetleSsh"
	ConnectionSourceBeetleObjectStorage ConnectionSource = "beetleObjectStorage"
	ConnectionSourceBeetleDB            ConnectionSource = "beetleDb"

	// Inline type — raw credentials in the request body (sensitive fields are
	// AES-encrypted on store)
	ConnectionSourceSSH   ConnectionSource = "ssh"
	ConnectionSourceMinio ConnectionSource = "minio"
	ConnectionSourceDB    ConnectionSource = "db"
)

// ── Provider names ───────────────────────────────────────────────────────────

// Provider names for MinioConnConfig.ProviderName and DBConnConfig.ProviderName.
// Spelled as cm-honeybee spells them, which is also how transx-ex spells its
// ProviderAWS…ProviderOnPrem constants, so a name resolved in honeybee reaches
// transx-ex unchanged.
const (
	ProviderAWS       = "aws"
	ProviderAlibaba   = "alibaba"
	ProviderTencent   = "tencent"
	ProviderNCP       = "ncp"
	ProviderNHN       = "nhn"
	ProviderIBM       = "ibm"
	ProviderGCP       = "gcp"
	ProviderKT        = "kt"
	ProviderAzure     = "azure"
	ProviderOpenStack = "openstack"

	// ProviderOnPrem marks infrastructure the operator runs themselves. It is
	// spelled out rather than left blank: an empty provider means "unspecified",
	// which is not a claim that the data lives on-premises.
	ProviderOnPrem = "onprem"
)

// providerNameOneOf is the shared validate tag body for both provider fields.
const providerNameOneOf = "aws alibaba tencent ncp nhn ibm gcp kt azure openstack onprem"

// ── Reference type: cm-honeybee ──────────────────────────────────────────────

// HoneybeeRef — cm-honeybee ConnectionInfo identifier.
type HoneybeeRef struct {
	ConnectionID string `json:"connectionId" validate:"required"`
}

// ── Reference type: cm-beetle ────────────────────────────────────────────────

// BeetleSSHRef — cm-beetle migrated node identifier (nsId·infraId·nodeId
// 3-tuple). The SSH access info is assembled from two calls: the infra read
// yields publicIP/sshPort/nodeUserName/nodeUserPassword/sshKeyId for the
// matching node, and the sshKey read yields the private key.
type BeetleSSHRef struct {
	NsID    string `json:"nsId"    validate:"required"`
	InfraID string `json:"infraId" validate:"required"`
	NodeID  string `json:"nodeId"  validate:"required"`
}

// BeetleOSRef — cm-beetle Object Storage identifier.
type BeetleOSRef struct {
	NsID string `json:"nsId" validate:"required"`
	OsID string `json:"osId" validate:"required"`
}

// BeetleDBRef — cm-beetle managed RDBMS identifier (pure reference plus a
// password). host/port/username/engine are looked up through cm-beetle; the
// lookup never returns the password (it is supplied only at creation time), so
// it is carried in the request and AES-encrypted on store. Access is always
// resolved as direct.
//
// Password doubles as the admin password on cm-beetle's logical-database calls,
// so a Username naming anyone but the instance admin still leaves database
// creation and deletion to the admin credentials.
//
// cb-tumblebug provisions MySQL and MariaDB only, so those are the engines a
// managed reference can carry.
//
// TLSMode and TLSCAPem are the only connection settings here that are not looked
// up. cm-beetle reports where the instance is and who its admin is, but says
// nothing about transport security, and a managed instance offers TLS whether or
// not it demands it — so the choice belongs to the caller, exactly as it does on
// an inline db connection. Without them a managed target could only ever be
// reached in plaintext, and an instance with require_secure_transport=ON could
// not be reached at all.
type BeetleDBRef struct {
	NsID     string `json:"nsId"     validate:"required"`
	RdbmsID  string `json:"rdbmsId"  validate:"required"`
	Username string `json:"username,omitempty"`           // defaults to the looked-up AdminUsername
	Password string `json:"password" validate:"required"` // AES-encrypted on store

	// TLSMode selects transport security; the empty value is "disable". It means
	// the same thing here as on DBConnConfig — see the table in transx-ex's
	// dbmsx/driver/base/tls.go.
	TLSMode string `json:"tlsMode,omitempty" validate:"omitempty,oneof=disable prefer require verify-ca verify-full"`

	// TLSCAPEM is the PEM root that verify-ca and verify-full check against.
	// Empty means the system trust store, which carries only publicly trusted
	// CAs — and managed RDBMS services commonly sign with their own, so those two
	// modes usually need the CSP's root supplied here.
	TLSCAPEM string `json:"tlsCAPem,omitempty"`
}

// ── Inline type ──────────────────────────────────────────────────────────────

// SSHConnConfig — directly supplied SSH access info. Maps to transxex.SSHConfig /
// transxex SSH tunnel config. Authentication is by key only, so there is no
// password field. AES-encrypted on store: PrivateKey.
type SSHConnConfig struct {
	Host       string `json:"host"       validate:"required"`
	Port       int    `json:"port,omitempty"` // defaults to 22
	Username   string `json:"username"   validate:"required"`
	PrivateKey string `json:"privateKey" validate:"required"`
}

// MinioConnConfig — directly supplied object storage access info. The field set
// mirrors cm-honeybee ConnectionInfo's MinIO fields (os_*) plus
// SourceGroup.ProviderName. No field corresponds to os_access_type: inline
// object storage access is always direct. A tunnelled store is reached only
// through a honeybee reference, whose SSH host centipede reads from honeybee.
//
// Maps to transxex.S3MinioConfig after ProviderName is resolved into an
// endpoint. AES-encrypted on store: SecretAccessKey.
type MinioConnConfig struct {
	// ProviderName decides the endpoint, region, TLS and bucket addressing style
	// (see ResolveS3Endpoint in core/migration). transxex.S3MinioConfig has no
	// provider field: it receives the resolved values.
	ProviderName string `json:"providerName" validate:"required,oneof=aws alibaba tencent ncp nhn ibm gcp kt azure openstack onprem"`

	// Endpoint is required for the openstack and onprem providers, where the
	// user supplies the host. For the others it is an optional override of the
	// derived host — except azure, whose host is derived from AccessKeyId.
	Endpoint string `json:"endpoint,omitempty"`

	// AccessKeyId doubles as the storage account name on azure, where it is also
	// the endpoint source.
	AccessKeyId     string `json:"accessKeyId"     validate:"required"`
	SecretAccessKey string `json:"secretAccessKey" validate:"required"`

	// Region is the region this store lives in. Required for the providers whose
	// hostname contains it (aws, alibaba, tencent, ncp, nhn, ibm) and for gcp,
	// which has a region-less host but still signs with a region.
	//
	// It is NOT unconditionally the signing region: NCP serves
	// "kr.object.ncloudstorage.com" and signs "kr-standard", so ResolveS3Endpoint
	// signs with this value only where the two are spelled the same and otherwise
	// leaves the signing region to be discovered.
	Region string `json:"region,omitempty"`

	// UseSSL is honored only for user-supplied endpoints (openstack, onprem).
	// The other providers use the fixed value from the provider table.
	UseSSL       bool   `json:"useSSL,omitempty"`
	BucketLookup string `json:"bucketLookup,omitempty"` // "" | "dns" | "path"
}

// DBConnConfig is the single canonical type for DBMS access info. Every DB
// source (honeybee, beetleDb, db) converges on this type before being converted
// into a transxex.DBMSLocation.
//
// It carries server-level access only and holds no database name: the databases
// to migrate are resolved by the plan layer and carried in
// targetmodel.DBMigrationInfo.SrcName/DstName, which
// DBConnConfigToDBMSLocation and ResolveDBMSLocation take as an argument.
//
// AES-encrypted on store: Password, SSHTunnel.PrivateKey.
type DBConnConfig struct {
	// ProviderName records where the database is hosted. transx-ex carries it
	// through to DBMSLocation.ProviderName but never reads it: no routing,
	// validation or connection decision depends on it. It is required all the
	// same, so that "where does this data live" has an answer for every
	// connection that holds data. A database the operator runs themselves is
	// ProviderOnPrem, spelled out rather than left blank.
	ProviderName string `json:"providerName" validate:"required,oneof=aws alibaba tencent ncp nhn ibm gcp kt azure openstack onprem"`

	DBMSType   string `json:"dbmsType"   validate:"required,oneof=mysql mariadb postgresql mongodb"`
	AccessType string `json:"accessType" validate:"required,oneof=direct sshTunnel"`

	// DB endpoint (the direct connection target, or the target reached through
	// the SSH tunnel)
	Host           string `json:"host"`
	Port           int    `json:"port,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	ConnectTimeout int    `json:"connectTimeout,omitempty"`

	// TLSMode selects transport security; the empty value is "disable". The
	// modes mean the same thing on every engine — see transx-ex's
	// dbmsx/driver/base/tls.go for the table that defines them.
	TLSMode string `json:"tlsMode,omitempty" validate:"omitempty,oneof=disable prefer require verify-ca verify-full"`

	// TLSCAPem is the PEM root that verify-ca and verify-full check against.
	// Empty means the system trust store, which carries only publicly trusted
	// CAs, so a server using a private CA needs its root supplied here.
	//
	// transx-ex also accepts a path to a CA file, but that path would name a
	// file on the centipede server rather than on the caller's machine, so it
	// is deliberately not part of this API.
	TLSCAPEM string `json:"tlsCAPem,omitempty"`

	// AuthSource selects the authentication database. MongoDB only; ignored by
	// other drivers. When empty and credentials are given, MongoDB defaults to
	// "admin" (where users are usually created) rather than the target database.
	AuthSource string `json:"authSource,omitempty"`

	// Jump host used when accessType=sshTunnel (inline SSH).
	SSHTunnel *SSHConnConfig `json:"sshTunnel,omitempty"`
}

// ── ConnectionRef ────────────────────────────────────────────────────────────

// ConnectionRef identifies a connection. Exactly one sub-field matching Source
// must be non-nil.
//
// Reference types (honeybee/beetle*) carry identifiers only; the server looks
// the access info up through each system's API. Inline types (ssh/minio/db)
// carry raw credentials in the request body, and their sensitive fields are
// AES-256-GCM encrypted ("enc:" prefix) on store.
//
// cb-tumblebug is not a connection source. It is reached during an object
// storage transfer, but only as an internal detail of transx-ex's
// managed-bucket access path.
type ConnectionRef struct {
	Source ConnectionSource `json:"source" validate:"required,oneof=honeybee beetleSsh beetleObjectStorage beetleDb ssh minio db"`

	// Reference types
	Honeybee  *HoneybeeRef  `json:"honeybee,omitempty"`
	BeetleSSH *BeetleSSHRef `json:"beetleSsh,omitempty"`
	BeetleOS  *BeetleOSRef  `json:"beetleObjectStorage,omitempty"`
	BeetleDB  *BeetleDBRef  `json:"beetleDb,omitempty"`

	// Inline types
	SSH   *SSHConnConfig   `json:"ssh,omitempty"`
	Minio *MinioConnConfig `json:"minio,omitempty"`
	DB    *DBConnConfig    `json:"db,omitempty"`
}
