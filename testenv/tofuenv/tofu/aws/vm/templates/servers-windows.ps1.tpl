# AWS Windows VM: install the selected servers natively.
#   Rendered by tofu/aws/vm when TF_VAR_aws_vm_servers is not empty, copied to
#   C:\cptf\servers.ps1 over SSH and run by terraform_data.servers as
#   Administrator. It deletes itself when it ends, since it holds the password,
#   and never prints it.
#
#   Idempotent: it runs again whenever the server list, a version or the account
#   changes, on the same VM. What is installed is kept and every account is
#   created or reset.
#
#     mysql       3306   official ZIP      C:\cptf\mysql       service cptf-mysql
#     mariadb     3307   official ZIP      C:\cptf\mariadb     service cptf-mariadb
#     postgresql  5432   EDB binaries ZIP  C:\cptf\postgresql  service cptf-postgresql
#     mongodb     27017  official MSI      C:\cptf\mongodb     service MongoDB
#                        + mongosh ZIP     C:\cptf\mongosh
#                        + tools ZIP       C:\cptf\mongodb-tools   (mongodump, ...)
#     minio       9000   GitHub release    C:\cptf\minio       service cptf-minio (WinSW)
#                                          console 9001
#
#   Every server listens on all interfaces with a password. Windows Firewall
#   opens their ports only at the very end, once every account is in place.
#
#   The bin directories go on the machine PATH, mysql's before mariadb's: the
#   MariaDB ZIP also ships mysql.exe and mysqldump.exe, and mysql/mysqldump are
#   meant to be MySQL's. MariaDB's own tools are mariadb, mariadb-dump, ...
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'   # Invoke-WebRequest is many times slower with it
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
Add-Type -AssemblyName System.IO.Compression.FileSystem

$Servers        = '${servers}' -split ','
$SrvUser        = '${username}'
$SrvPass        = '${password}'
$DbName         = '${db_name}'
$MinioBucket    = '${minio_bucket}'
$MySQLVersion   = '${mysql_version}'
$MariaDBVersion = '${mariadb_version}'
$PgVersion      = '${postgres_version}'
$MongoVersion   = '${mongodb_version}'
$MinioRelease   = '${minio_release}'
$MinioSha256    = '${minio_sha256}'
$Tools          = '${tools}' -split ','
$FirewallPorts  = '${firewall_ports}' -split ','

# Pinned here rather than exposed as settings: they only support the servers.
$MongoshVersion    = '2.5.6'
$MongoToolsVersion = '100.12.2'
$WinSWUrl          = 'https://github.com/winsw/winsw/releases/download/v2.12.0/WinSW-x64.exe'
$WinSWSha256       = '05b82d46ad331cc16bdc00de5c6332c1ef818df8ceefcd49c726553209b3a0da'

$Root = 'C:\cptf'
$Dl   = "$Root\downloads"
$Data = "$Root\data"
$Log  = "$Root\logs\servers.log"
New-Item -ItemType Directory -Force -Path $Dl, $Data, "$Root\logs" | Out-Null

$Utf8 = New-Object System.Text.UTF8Encoding $false

function Write-Log([string]$Message) {
    $line = "[cptf-servers] $Message"
    Write-Output $line
    Add-Content -Path $Log -Value ("{0:s} {1}" -f (Get-Date), $line)
}

function Test-Want([string]$Name) { return $Servers -contains $Name }

# Write-Text <path> <text> — UTF-8 without a BOM, which mysqld, mongod and
#   PostgreSQL all read; Set-Content -Encoding utf8 would add one in PowerShell 5.1.
function Write-Text([string]$Path, [string]$Text) { [IO.File]::WriteAllText($Path, $Text, $Utf8) }

# Invoke-Native <exe> <args...> — run a program, pass its output through and fail
#   on a non-zero exit. Stop is lifted inside: PowerShell 5.1 turns a native
#   program's stderr into terminating errors under it, and mysqld --initialize
#   reports progress on stderr.
function Invoke-Native([string]$File, [string[]]$ArgList) {
    $ErrorActionPreference = 'Continue'
    & $File @ArgList 2>&1 | ForEach-Object { "$_" }
    if ($LASTEXITCODE -ne 0) { throw "$File exited with $LASTEXITCODE" }
}

# Get-Download <url(s)> <out> [sha256] — download once (a finished file is
#   reused), trying each URL in turn and retrying the lot; checked against sha256
#   when one is given.
function Get-Download([string[]]$Urls, [string]$Out, [string]$Sha256 = '') {
    if (-not (Test-Path $Out)) {
        $errors = @()
        for ($i = 1; $i -le 3 -and -not (Test-Path $Out); $i++) {
            foreach ($url in $Urls) {
                try {
                    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile "$Out.part"
                    Move-Item -Force "$Out.part" $Out
                    break
                } catch {
                    $errors += "$url ($($_.Exception.Message))"
                }
            }
            if (-not (Test-Path $Out)) { Start-Sleep -Seconds 10 }
        }
        if (-not (Test-Path $Out)) {
            Remove-Item -Force -ErrorAction SilentlyContinue "$Out.part"
            throw "download failed: $(($errors | Select-Object -Unique) -join '; ')"
        }
    }
    if ($Sha256) {
        $hash = (Get-FileHash -Algorithm SHA256 -Path $Out).Hash
        if ($hash -ne $Sha256.ToUpper()) {
            Remove-Item -Force $Out
            throw "sha256 mismatch: $($Urls[0])"
        }
    }
}

# Expand-Package <zip> <dest> — unzip a package whose content sits in one top
#   folder, so that folder's content becomes <dest>.
function Expand-Package([string]$Zip, [string]$Dest) {
    $tmp = "$Dest.extract"
    if (Test-Path $tmp) { Remove-Item -Recurse -Force $tmp }
    [IO.Compression.ZipFile]::ExtractToDirectory($Zip, $tmp)
    $top = @(Get-ChildItem -Path $tmp)
    $src = if ($top.Count -eq 1 -and $top[0].PSIsContainer) { $top[0].FullName } else { $tmp }
    if (Test-Path $Dest) { Remove-Item -Recurse -Force $Dest }
    Move-Item -Path $src -Destination $Dest
    if (Test-Path $tmp) { Remove-Item -Recurse -Force $tmp }
}

function Wait-Port([int]$Port, [int]$Seconds = 300) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $c = New-Object Net.Sockets.TcpClient('127.0.0.1', $Port)
            $c.Close()
            return
        } catch { Start-Sleep -Seconds 2 }
    }
    throw "nothing listens on port $Port"
}

# Start-ServiceAndWait <name> <port> [-NoRestart] — (re)start a service so it
#   picks up its configuration, and wait for its port. A running service is
#   restarted unless -NoRestart.
#
#   A start can fail once and then succeed: right after its MSI, MongoDB's restart
#   failed with "did not respond to the start or control request in a timely
#   fashion" (SCM 7009/7000), and a later start of the same service worked. So the
#   old process is waited out before the start, and a failed start is retried.
function Start-ServiceAndWait([string]$Name, [int]$Port, [switch]$NoRestart) {
    Set-Service -Name $Name -StartupType Automatic
    if ((Get-Service $Name).Status -eq 'Running') {
        if ($NoRestart) { Wait-Port $Port; return }
        $procId = (Get-CimInstance Win32_Service -Filter "Name='$Name'").ProcessId
        Stop-Service -Name $Name -Force
        if ($procId) { Wait-Process -Id $procId -Timeout 120 -ErrorAction SilentlyContinue }
    }
    for ($i = 1; ; $i++) {
        try {
            Start-Service -Name $Name
            break
        } catch {
            if ($i -ge 5) { throw }
            Write-Log "starting $Name failed (attempt $i), retrying: $($_.Exception.Message)"
            Start-Sleep -Seconds 5
        }
    }
    Wait-Port $Port
}

function Install-VCRedist {
    $key = 'HKLM:\SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64'
    if ((Test-Path $key) -and (Get-ItemProperty $key).Installed -eq 1) { return }
    $exe = "$Dl\vc_redist.x64.exe"
    Get-Download 'https://aka.ms/vs/17/release/vc_redist.x64.exe' $exe
    $p = Start-Process -FilePath $exe -ArgumentList '/install', '/quiet', '/norestart' -Wait -PassThru
    # 3010: installed, reboot pending (not needed here); 1638: a newer one is there.
    if (@(0, 3010, 1638) -notcontains $p.ExitCode) { throw "vc_redist exited with $($p.ExitCode)" }
}

# ------------------------------------------------------------------------------
# mysql / mariadb (ZIP)
# ------------------------------------------------------------------------------

# Invoke-MySqlClient <client.exe> <port> <sql> — run SQL as root over TCP. root
#   has no password on the first run and SrvPass on every run after it, so both
#   are tried. The password goes through an option file, never argv.
function Invoke-MySqlClient([string]$Client, [int]$Port, [string]$Sql) {
    $cnf = "$Root\client-$Port.cnf"
    $file = "$Root\client-$Port.sql"
    try {
        Write-Text $file $Sql
        Write-Text $cnf "[client]`r`nuser=root`r`npassword=`"$SrvPass`"`r`nhost=127.0.0.1`r`nport=$Port`r`n"
        $ErrorActionPreference = 'Continue'
        & $Client "--defaults-extra-file=$cnf" -e 'SELECT 1' 2>&1 | Out-Null
        if ($LASTEXITCODE -ne 0) {
            Write-Text $cnf "[client]`r`nuser=root`r`nhost=127.0.0.1`r`nport=$Port`r`n"
        }
        $ErrorActionPreference = 'Stop'
        Invoke-Native $Client @("--defaults-extra-file=$cnf", '-e', "source $($file -replace '\\', '/')")
    } finally {
        Remove-Item -Force -ErrorAction SilentlyContinue $cnf, $file
    }
}

function Get-AccountSql {
    return @"
ALTER USER 'root'@'localhost' IDENTIFIED BY '$SrvPass';
CREATE USER IF NOT EXISTS '$SrvUser'@'%' IDENTIFIED BY '$SrvPass';
ALTER USER '$SrvUser'@'%' IDENTIFIED BY '$SrvPass';
GRANT ALL PRIVILEGES ON *.* TO '$SrvUser'@'%' WITH GRANT OPTION;
CREATE DATABASE IF NOT EXISTS $DbName;
FLUSH PRIVILEGES;
"@
}

# mariadb-install-db can leave anonymous accounts and an open test database, as
# mysql_secure_installation would remove. An anonymous localhost account also
# outranks '<user>'@'%' for local logins, which it denies.
function Get-MariaDBCleanupSql {
    return @"
DELETE FROM mysql.global_priv WHERE User = '';
DROP DATABASE IF EXISTS test;
DELETE FROM mysql.db WHERE Db = 'test' OR Db = 'test\\_%';
"@
}

function Install-MySQL {
    $base = "$Root\mysql"; $data = "$Data\mysql"; $ini = "$base\my.ini"
    Write-Log "mysql $MySQLVersion ..."
    Install-VCRedist
    if (-not (Test-Path "$base\bin\mysqld.exe")) {
        # The CDN first: downloads.mysql.com's archive gateway answers 403 to
        # PowerShell's User-Agent (any but curl's, it seems), and only redirects
        # here anyway. archives/ keeps every release; Downloads/ only the current.
        $zip = "$Dl\mysql-$MySQLVersion-winx64.zip"
        $series = ($MySQLVersion -split '\.')[0..1] -join '.'
        Get-Download @(
            "https://cdn.mysql.com/archives/mysql-$series/mysql-$MySQLVersion-winx64.zip",
            "https://cdn.mysql.com/Downloads/MySQL-$series/mysql-$MySQLVersion-winx64.zip",
            "https://downloads.mysql.com/archives/get/p/23/file/mysql-$MySQLVersion-winx64.zip") $zip
        Expand-Package $zip $base
    }
    $b = $base -replace '\\', '/'; $d = $data -replace '\\', '/'
    Write-Text $ini @"
[mysqld]
basedir=$b
datadir=$d
port=3306
bind-address=0.0.0.0
mysqlx=OFF
# shop_db creates functions and triggers, and binary logging is on by default.
log_bin_trust_function_creators=1
"@
    if (-not (Test-Path "$data\mysql")) {
        # --initialize wants the datadir absent or empty.
        Invoke-Native "$base\bin\mysqld.exe" @("--defaults-file=$ini", '--initialize-insecure', '--console')
    }
    if (-not (Get-Service cptf-mysql -ErrorAction SilentlyContinue)) {
        Invoke-Native "$base\bin\mysqld.exe" @('--install', 'cptf-mysql', "--defaults-file=$ini")
    }
    Start-ServiceAndWait cptf-mysql 3306
    Invoke-MySqlClient "$base\bin\mysql.exe" 3306 (Get-AccountSql)
}

function Install-MariaDB {
    $base = "$Root\mariadb"; $data = "$Data\mariadb"; $ini = "$data\my.ini"
    Write-Log "mariadb $MariaDBVersion ..."
    Install-VCRedist
    if (-not (Test-Path "$base\bin\mariadbd.exe")) {
        $zip = "$Dl\mariadb-$MariaDBVersion-winx64.zip"
        Get-Download "https://archive.mariadb.org/mariadb-$MariaDBVersion/winx64-packages/mariadb-$MariaDBVersion-winx64.zip" $zip
        Expand-Package $zip $base
    }
    if (-not (Test-Path $ini)) {
        # Creates the datadir, writes its my.ini and registers the service. No
        # --password: it would sit in argv; root gets one below, over TCP.
        Invoke-Native "$base\bin\mariadb-install-db.exe" @("--datadir=$data", '--service=cptf-mariadb', '--port=3307')
    }
    $text = [IO.File]::ReadAllText($ini)
    foreach ($opt in 'bind-address=0.0.0.0', 'log_bin_trust_function_creators=1') {
        if ($text -notmatch "(?m)^$([regex]::Escape(($opt -split '=')[0]))=") {
            $text = $text -replace '(?m)^\[mysqld\]\r?$', "[mysqld]`r`n$opt"
        }
    }
    Write-Text $ini $text
    Start-ServiceAndWait cptf-mariadb 3307
    Invoke-MySqlClient "$base\bin\mariadb.exe" 3307 ((Get-MariaDBCleanupSql) + "`r`n" + (Get-AccountSql))
}

# ------------------------------------------------------------------------------
# postgresql (EDB binaries ZIP)
# ------------------------------------------------------------------------------
# The binaries ZIP rather than EDB's installer: the installer exited 1 over SSH
# with nothing but its own log to say why. This is the same build, set up the way
# mysql and mariadb are - initdb, then pg_ctl register - so every step reports.
function Install-PostgreSQL {
    $base = "$Root\postgresql"; $data = "$Data\postgresql"
    Write-Log "postgresql $PgVersion ..."
    Install-VCRedist
    if (-not (Test-Path "$base\bin\postgres.exe")) {
        $zip = "$Dl\postgresql-$PgVersion-1-windows-x64-binaries.zip"
        Get-Download "https://get.enterprisedb.com/postgresql/postgresql-$PgVersion-1-windows-x64-binaries.zip" $zip
        Expand-Package $zip $base
        # The ZIP also carries pgAdmin and StackBuilder, which a server does not need.
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue "$base\pgAdmin*", "$base\StackBuilder", "$base\doc"
    }
    if (-not (Test-Path "$data\PG_VERSION")) {
        # initdb drops to a restricted token, and the service runs as
        # NetworkService (S-1-5-20); both need the datadir, which initdb wants empty.
        New-Item -ItemType Directory -Force -Path $data | Out-Null
        icacls.exe $data /grant "$($env:USERNAME):(OI)(CI)F" /grant '*S-1-5-20:(OI)(CI)F' | Out-Null
        # The superuser password goes through a file, never argv.
        $pw = "$Root\pg-pw.txt"
        try {
            Write-Text $pw $SrvPass
            Invoke-Native "$base\bin\initdb.exe" @('-D', $data, '-U', 'postgres', "--pwfile=$pw",
                '-A', 'scram-sha-256', '-E', 'UTF8', '--locale=C')
        } finally {
            Remove-Item -Force -ErrorAction SilentlyContinue $pw
        }
    }
    $conf = [IO.File]::ReadAllText("$data\postgresql.conf")
    $conf = $conf -replace "(?m)^#?listen_addresses\s*=.*$", "listen_addresses = '*'"
    # A service has no console: the server log goes to $data\log instead.
    $conf = $conf -replace "(?m)^#?logging_collector\s*=.*$", 'logging_collector = on'
    Write-Text "$data\postgresql.conf" $conf
    $hba = [IO.File]::ReadAllText("$data\pg_hba.conf")
    if ($hba -notmatch '(?m)^host all all 0\.0\.0\.0/0 scram-sha-256') {
        $hba = $hba.TrimEnd() + "`r`nhost all all 0.0.0.0/0 scram-sha-256`r`nhost all all ::/0 scram-sha-256`r`n"
        Write-Text "$data\pg_hba.conf" $hba
    }
    if (-not (Get-Service cptf-postgresql -ErrorAction SilentlyContinue)) {
        Invoke-Native "$base\bin\pg_ctl.exe" @('register', '-N', 'cptf-postgresql',
            '-U', 'NT AUTHORITY\NetworkService', '-D', $data, '-S', 'auto', '-w')
    }
    Start-ServiceAndWait cptf-postgresql 5432

    $psql = "$base\bin\psql.exe"
    $sqlFile = "$Root\pg-account.sql"
    $env:PGPASSWORD = $SrvPass
    try {
        Write-Text $sqlFile @"
DO `$`$
BEGIN
  IF EXISTS (SELECT FROM pg_roles WHERE rolname = '$SrvUser') THEN
    ALTER ROLE $SrvUser LOGIN SUPERUSER PASSWORD '$SrvPass';
  ELSE
    CREATE ROLE $SrvUser LOGIN SUPERUSER PASSWORD '$SrvPass';
  END IF;
END
`$`$;
"@
        Invoke-Native $psql @('-q', '-U', 'postgres', '-h', '127.0.0.1', '-d', 'postgres', '-v', 'ON_ERROR_STOP=1', '-f', $sqlFile)
        $exists = & $psql -U postgres -h 127.0.0.1 -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname = '$DbName'"
        if ("$exists".Trim() -ne '1') {
            Invoke-Native $psql @('-q', '-U', 'postgres', '-h', '127.0.0.1', '-d', 'postgres', '-c', "CREATE DATABASE $DbName OWNER $SrvUser")
        }
    } finally {
        Remove-Item -Force -ErrorAction SilentlyContinue $sqlFile
        Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
    }
}

# ------------------------------------------------------------------------------
# mongodb (MSI + mongosh + database tools)
# ------------------------------------------------------------------------------
function Install-MongoDB {
    $base = "$Root\mongodb"; $cfg = "$base\bin\mongod.cfg"
    Write-Log "mongodb $MongoVersion ..."
    if (-not (Get-Service MongoDB -ErrorAction SilentlyContinue)) {
        $msi = "$Dl\mongodb-windows-x86_64-$MongoVersion-signed.msi"
        Get-Download "https://fastdl.mongodb.org/windows/mongodb-windows-x86_64-$MongoVersion-signed.msi" $msi
        $p = Start-Process msiexec.exe -Wait -PassThru -ArgumentList (
            "/i `"$msi`" /qn /norestart /l*v `"$Root\logs\mongodb-msi.log`" " +
            "ADDLOCAL=`"ServerService`" SHOULD_INSTALL_COMPASS=`"0`" INSTALLLOCATION=`"$base`"")
        if ($p.ExitCode -ne 0) { throw "MongoDB MSI exited with $($p.ExitCode) (see $Root\logs\mongodb-msi.log)" }
    }
    # The MSI has carried no shell or dump tools since 6.0.
    if (-not (Test-Path "$Root\mongosh\bin\mongosh.exe")) {
        $zip = "$Dl\mongosh-$MongoshVersion-win32-x64.zip"
        Get-Download "https://downloads.mongodb.com/compass/mongosh-$MongoshVersion-win32-x64.zip" $zip
        Expand-Package $zip "$Root\mongosh"
    }
    if (-not (Test-Path "$Root\mongodb-tools\bin\mongodump.exe")) {
        $zip = "$Dl\mongodb-database-tools-windows-x86_64-$MongoToolsVersion.zip"
        Get-Download "https://fastdl.mongodb.org/tools/db/mongodb-database-tools-windows-x86_64-$MongoToolsVersion.zip" $zip
        Expand-Package $zip "$Root\mongodb-tools"
    }
    # The MSI leaves it running with its own config; nothing to pick up yet.
    Start-ServiceAndWait MongoDB 27017 -NoRestart

    # The root user first, while only localhost is bound; then authorization and
    # the public bind. Once authorization is on, the script logs in as the user
    # it is about to reset. The credentials reach mongosh through the
    # environment, not argv.
    $text = [IO.File]::ReadAllText($cfg)
    $auth = $text -match '(?m)^security:'
    $env:CPTF_AUTH = if ($auth) { '1' } else { '0' }
    $env:CPTF_U = $SrvUser
    $env:CPTF_P = $SrvPass
    # From a file, not --eval: PowerShell 5.1 passes a native argument's embedded
    # double quotes through unescaped, so --eval 'db.getSiblingDB("admin")' reached
    # mongosh as getSiblingDB(admin) - a SyntaxError. The file holds no secret;
    # the credentials stay in the environment.
    $js = "$Root\mongo-account.js"
    try {
        Write-Text $js @'
const a = db.getSiblingDB("admin");
if (process.env.CPTF_AUTH === "1") a.auth(process.env.CPTF_U, process.env.CPTF_P);
if (a.getUser(process.env.CPTF_U)) a.updateUser(process.env.CPTF_U, { pwd: process.env.CPTF_P, roles: ["root"] });
else a.createUser({ user: process.env.CPTF_U, pwd: process.env.CPTF_P, roles: ["root"] });
'@
        Invoke-Native "$Root\mongosh\bin\mongosh.exe" @('--quiet', '--norc', 'mongodb://127.0.0.1:27017/admin', $js)
    } finally {
        Remove-Item -Force -ErrorAction SilentlyContinue $js
        Remove-Item Env:\CPTF_AUTH, Env:\CPTF_U, Env:\CPTF_P -ErrorAction SilentlyContinue
    }

    $text = $text -replace '(?m)^(\s*bindIp:).*$', '$1 0.0.0.0'
    if (-not $auth) {
        $text = $text -replace '(?m)^#security:.*$', ''
        $text = $text.TrimEnd() + "`r`nsecurity:`r`n  authorization: enabled`r`n"
    }
    Write-Text $cfg $text
    Start-ServiceAndWait MongoDB 27017
}

# ------------------------------------------------------------------------------
# minio (GitHub release + WinSW)
# ------------------------------------------------------------------------------

# New-MinioBucket — a SigV4-signed PUT. Not curl.exe --aws-sigv4: older curl builds
#   sign it wrong, and mc is not installed. 200 created, 409 exists.
function New-MinioBucket([string]$Bucket) {
    $endpoint = '127.0.0.1:9000'; $region = 'us-east-1'
    $now = [DateTime]::UtcNow
    $amz = $now.ToString('yyyyMMddTHHmmssZ'); $day = $now.ToString('yyyyMMdd')
    $sha = [Security.Cryptography.SHA256]::Create()
    $hex = { param([byte[]]$b) -join ($b | ForEach-Object { $_.ToString('x2') }) }
    $hmac = {
        param([byte[]]$k, [string]$m)
        $h = New-Object Security.Cryptography.HMACSHA256 -ArgumentList (, $k)
        $h.ComputeHash([Text.Encoding]::UTF8.GetBytes($m))
    }
    $payload = & $hex $sha.ComputeHash([byte[]]@())
    $signed = 'host;x-amz-content-sha256;x-amz-date'
    $canon = "PUT`n/$Bucket`n`nhost:$endpoint`nx-amz-content-sha256:$payload`nx-amz-date:$amz`n`n$signed`n$payload"
    $scope = "$day/$region/s3/aws4_request"
    $sts = "AWS4-HMAC-SHA256`n$amz`n$scope`n" + (& $hex $sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($canon)))
    $key = [Text.Encoding]::UTF8.GetBytes("AWS4$SrvPass")
    foreach ($part in $day, $region, 's3', 'aws4_request') { $key = & $hmac $key $part }
    $sig = & $hex (& $hmac $key $sts)

    $req = [Net.HttpWebRequest]::Create("http://$endpoint/$Bucket")
    $req.Method = 'PUT'
    $req.ContentLength = 0
    $req.Headers.Add('x-amz-date', $amz)
    $req.Headers.Add('x-amz-content-sha256', $payload)
    $req.Headers.Add('Authorization', "AWS4-HMAC-SHA256 Credential=$SrvUser/$scope, SignedHeaders=$signed, Signature=$sig")
    try {
        $resp = $req.GetResponse()
        $code = [int]$resp.StatusCode
        $resp.Close()
    } catch [Net.WebException] {
        if (-not $_.Exception.Response) { throw }
        $code = [int]$_.Exception.Response.StatusCode
    }
    Write-Log "bucket $($Bucket): HTTP $code"
    if (@(200, 409) -notcontains $code) { throw "creating bucket $Bucket failed with HTTP $code" }
}

function Install-MinIO {
    $dir = "$Root\minio"; $exe = "$dir\minio.exe"; $svc = "$dir\minio-service.exe"; $xml = "$dir\minio-service.xml"
    Write-Log "minio $MinioRelease ..."
    New-Item -ItemType Directory -Force -Path $dir, "$Data\minio" | Out-Null
    $current = ''
    if (Test-Path $exe) {
        $ErrorActionPreference = 'Continue'
        $current = "$(& $exe --version 2>&1 | Select-Object -First 1)"
        $ErrorActionPreference = 'Stop'
    }
    if ($current -notmatch [regex]::Escape($MinioRelease)) {
        $url = "https://github.com/minio/minio/releases/download/$MinioRelease/minio.windows-amd64.$MinioRelease.exe"
        $sum = $MinioSha256
        if (-not $sum) { $sum = ((Invoke-WebRequest -UseBasicParsing -Uri "$url.sha256sum").Content -split '\s+')[0] }
        $dl = "$Dl\minio-$MinioRelease.exe"
        Get-Download $url $dl $sum
        if (Get-Service cptf-minio -ErrorAction SilentlyContinue) { Stop-Service cptf-minio }
        Copy-Item -Force $dl $exe
    }
    # minio.exe does not talk to the service control manager, so sc.exe cannot run
    # it (error 1053). WinSW wraps it: the exe and its XML share a base name.
    Get-Download $WinSWUrl $svc $WinSWSha256
    $u = [Security.SecurityElement]::Escape($SrvUser)
    $p = [Security.SecurityElement]::Escape($SrvPass)
    Write-Text $xml @"
<service>
  <id>cptf-minio</id>
  <name>cptf-minio</name>
  <description>MinIO (cm-centipede test server)</description>
  <executable>$exe</executable>
  <arguments>server $Data\minio --address ":9000" --console-address ":9001"</arguments>
  <env name="MINIO_ROOT_USER" value="$u"/>
  <env name="MINIO_ROOT_PASSWORD" value="$p"/>
  <startmode>Automatic</startmode>
  <onfailure action="restart" delay="10 sec"/>
  <log mode="roll-by-size"/>
</service>
"@
    # The XML holds the password: Administrators and SYSTEM only.
    icacls.exe $xml /inheritance:r /grant '*S-1-5-32-544:F' /grant '*S-1-5-18:F' | Out-Null
    if (-not (Get-Service cptf-minio -ErrorAction SilentlyContinue)) { Invoke-Native $svc @('install') }
    Start-ServiceAndWait cptf-minio 9000
    $deadline = (Get-Date).AddSeconds(120)
    while ($true) {
        try { Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:9000/minio/health/ready' | Out-Null; break }
        catch { if ((Get-Date) -gt $deadline) { throw 'MinIO did not become ready' }; Start-Sleep -Seconds 2 }
    }
    New-MinioBucket $MinioBucket
}

# ------------------------------------------------------------------------------
# PATH and firewall
# ------------------------------------------------------------------------------

# Set-MachinePath — put the installed servers' bin directories on the machine
#   PATH, in the order below. The value is read raw and written back as
#   REG_EXPAND_SZ: [Environment]::SetEnvironmentVariable would expand %SystemRoot%
#   and friends into fixed paths and store a plain REG_SZ.
function Set-MachinePath {
    $dirs = @()
    if (Test-Want 'mysql')      { $dirs += "$Root\mysql\bin" }
    if (Test-Want 'mariadb')    { $dirs += "$Root\mariadb\bin" }
    if (Test-Want 'postgresql') { $dirs += "$Root\postgresql\bin" }
    if (Test-Want 'mongodb')    { $dirs += "$Root\mongodb\bin", "$Root\mongosh\bin", "$Root\mongodb-tools\bin" }
    if (Test-Want 'minio')      { $dirs += "$Root\minio" }

    $key = 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment'
    $raw = (Get-Item $key).GetValue('Path', '', 'DoNotExpandEnvironmentNames')
    # Ours are dropped and appended again, so their order holds across runs.
    $keep = @($raw -split ';' | Where-Object { $_ -and $_ -notlike "$Root\*" })
    Set-ItemProperty -Path $key -Name Path -Value (($keep + $dirs) -join ';') -Type ExpandString

    # Tell running programs (Explorer, new RDP shells) the environment changed.
    Add-Type -Namespace Cptf -Name Native -MemberDefinition @'
[DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Auto)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
'@
    $r = [UIntPtr]::Zero
    [void][Cptf.Native]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$r)

    $env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
    $missing = @($Tools | Where-Object { $_ -and -not (Get-Command $_ -ErrorAction SilentlyContinue) })
    if ($missing.Count -gt 0) { throw "missing on PATH: $($missing -join ', ')" }
    Write-Log "on PATH: $($Tools -join ' ')"
}

function Open-Firewall {
    foreach ($port in $FirewallPorts) {
        if (-not $port) { continue }
        $name = "cptf-tcp-$port"
        if (-not (Get-NetFirewallRule -Name $name -ErrorAction SilentlyContinue)) {
            New-NetFirewallRule -Name $name -DisplayName "cm-centipede test server TCP $port" `
                -Direction Inbound -Protocol TCP -LocalPort $port -Action Allow | Out-Null
        }
    }
    Write-Log "firewall open: $($FirewallPorts -join ' ')"
}

# Write-ServerLogs — on failure, the tail of every server log there is, so the
#   apply output says why without anyone having to log in to the VM (which may be
#   gone by the time someone looks).
function Write-ServerLogs {
    $logs = @(
        "$Data\mysql\*.err", "$Data\mariadb\*.err", "$Data\postgresql\log\*",
        "$Root\mongodb\log\mongod.log", "$Root\logs\mongodb-msi.log", "$Root\minio\*.log"
    )
    foreach ($pattern in $logs) {
        $file = Get-ChildItem -Path $pattern -File -ErrorAction SilentlyContinue |
            Sort-Object LastWriteTime | Select-Object -Last 1
        if (-not $file) { continue }
        Write-Output "----- $($file.FullName) (last 30 lines) -----"
        Get-Content -Path $file.FullName -Tail 30 -ErrorAction SilentlyContinue
    }
    # A service that fails before it can write its own log only shows up here.
    Write-Output '----- Service Control Manager errors (last 10) -----'
    Get-WinEvent -FilterHashtable @{ LogName = 'System'; ProviderName = 'Service Control Manager'; Level = 2 } -MaxEvents 10 -ErrorAction SilentlyContinue |
        ForEach-Object { "{0:s} {1}" -f $_.TimeCreated, ($_.Message -replace '\s+', ' ') }
}

# ------------------------------------------------------------------------------
try {
    Write-Log "servers: $($Servers -join ' ')"
    if (Test-Want 'mysql')      { Install-MySQL }
    if (Test-Want 'mariadb')    { Install-MariaDB }
    if (Test-Want 'postgresql') { Install-PostgreSQL }
    if (Test-Want 'mongodb')    { Install-MongoDB }
    if (Test-Want 'minio')      { Install-MinIO }
    Set-MachinePath
    Open-Firewall
    Write-Log 'done'
} catch {
    Write-Log "FAILED: $_"
    Write-ServerLogs
    exit 1
} finally {
    Remove-Item -Force -ErrorAction SilentlyContinue $PSCommandPath
}
