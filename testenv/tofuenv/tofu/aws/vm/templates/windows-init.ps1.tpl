<powershell>
# AWS Windows VM init: OpenSSH Server, logging in as Administrator with the key
# tofu generated.
#   Rendered only when TF_VAR_aws_vm_os=windows. EC2Launch runs it once, on first
#   boot, as LocalSystem.
#   Everything that reaches this VM afterwards comes in over SSH: the server
#   install (terraform_data.servers), gendata's SFTP, and ssh_command. sshd is
#   therefore the last thing started, once the key is in place, so the first
#   connection that gets through can also log in.
#   The OpenSSH capability comes from Windows Update and occasionally fails to
#   download; the Win32-OpenSSH MSI from GitHub is the fallback.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

New-Item -ItemType Directory -Force -Path 'C:\cptf\logs', 'C:\cptf\downloads' | Out-Null
Start-Transcript -Path 'C:\cptf\logs\init.log' -Append | Out-Null
try {
    # gendata's SFTP writes here and cannot create a drive-level folder itself.
    New-Item -ItemType Directory -Force -Path '${data_path}' | Out-Null

    if (-not (Get-Service sshd -ErrorAction SilentlyContinue)) {
        $installed = $false
        for ($i = 1; $i -le 3 -and -not $installed; $i++) {
            try {
                Add-WindowsCapability -Online -Name 'OpenSSH.Server~~~~0.0.1.0' | Out-Null
                $installed = [bool](Get-Service sshd -ErrorAction SilentlyContinue)
            } catch {
                Write-Output "OpenSSH capability attempt $i failed: $_"
                Start-Sleep -Seconds 20
            }
        }
        if (-not $installed) {
            $msi = 'C:\cptf\downloads\OpenSSH-Win64.msi'
            Invoke-WebRequest -UseBasicParsing -OutFile $msi `
                -Uri 'https://github.com/PowerShell/Win32-OpenSSH/releases/download/v9.8.3.0p2-Preview/OpenSSH-Win64-v9.8.3.0.msi'
            $p = Start-Process msiexec.exe -ArgumentList "/i `"$msi`" /qn /norestart" -Wait -PassThru
            if ($p.ExitCode -ne 0) { throw "OpenSSH MSI exited with $($p.ExitCode)" }
        }
    }

    # sshd writes C:\ProgramData\ssh (host keys, sshd_config) on its first start.
    Set-Service -Name sshd -StartupType Automatic
    Start-Service sshd
    Stop-Service sshd

    # Members of Administrators read their keys from here, not ~\.ssh. sshd
    # ignores the file unless only Administrators and SYSTEM can reach it; SIDs,
    # since the group names are localised.
    $keys = 'C:\ProgramData\ssh\administrators_authorized_keys'
    Set-Content -Path $keys -Value '${public_key}' -Encoding ascii
    icacls.exe $keys /inheritance:r /grant '*S-1-5-32-544:F' /grant '*S-1-5-18:F' | Out-Null

    if (-not (Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -DisplayName 'OpenSSH Server (sshd)' `
            -Direction Inbound -Protocol TCP -LocalPort 22 -Action Allow | Out-Null
    }
    Start-Service sshd
    Write-Output 'sshd ready'
} finally {
    Stop-Transcript | Out-Null
}
</powershell>
