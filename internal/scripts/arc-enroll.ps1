# arcify in-VM enrollment script for Windows.
#
# Invoked by the action-style `POST .../virtualMachines/{vm}/runCommand` API
# (commandId=RunPowerShellScript) with a single named parameter `Config`.
# The Windows agent invokes PowerShell with `-Config <value>`, which binds
# to the `param([string]$Config)` block below.
#
# The value is a base64-encoded JSON blob produced by arcify:
#   {
#     "subscriptionId": "...",
#     "resourceGroup":  "...",
#     "location":       "...",
#     "tenantId":       "...",
#     "resourceName":   "...",
#     "vmId":           "<uuid>",
#     "privateKey":     "<base64 PKCS#1 DER>"
#   }
#
# Because the action-style runCommand API has no `protectedParameters`
# concept, the encoded config travels in the request body just like any
# other parameter. arcify never logs the encoded value; the script does not
# echo it. On the success path, the script prints a sentinel line
# `ARCIFY_RESULT=success` as its very last output so arcify can distinguish
# success from failure without an exit code (the action API does not return
# the script's exit code).

param(
    [Parameter(Mandatory = $true)]
    [string] $Config
)

$ErrorActionPreference = 'Stop'

# arcify exists specifically to Arc-enroll Azure VMs (testing/sandbox flow).
# Both the official aka.ms/azcmagent-windows installer script and `azcmagent
# connect` refuse to run on an Azure VM unless MSFT_ARC_TEST=true is set; see
# https://aka.ms/azcmagent-testwarning. The installer reads the *machine-
# scoped* environment variable (registry-backed) from a SYSTEM-context custom
# action, so we set it at machine scope before invoking either tool.
[Environment]::SetEnvironmentVariable('MSFT_ARC_TEST', 'true', 'Machine')
$env:MSFT_ARC_TEST = 'true'

function Log($Message) {
    Write-Output "[arcify] $Message"
}

# Decode credentials in memory.
try {
    $cfg = [System.Text.Encoding]::UTF8.GetString(
        [System.Convert]::FromBase64String($Config)
    ) | ConvertFrom-Json
}
catch {
    Log "failed to decode/parse config: $_"
    exit 1
}

$required = @('subscriptionId', 'resourceGroup', 'location', 'tenantId', 'resourceName', 'vmId', 'privateKey')
foreach ($field in $required) {
    if (-not $cfg.$field) {
        Log "missing required config field: $field"
        exit 1
    }
}

$AgentUrl = 'https://aka.ms/azcmagent-windows'
$InstallerPath = Join-Path $env:TEMP 'install-azcmagent.ps1'
$Azcm = 'C:\Program Files\AzureConnectedMachineAgent\azcmagent.exe'

# Install azcmagent if missing. Defer to the official installer script
# (aka.ms/azcmagent-windows) so we inherit its signature validation, Azure-VM
# detection, and download retries. The installer reads MSFT_ARC_TEST from the
# Machine env scope (set above) and proceeds with a warning on Azure VMs.
if (-not (Test-Path $Azcm)) {
    try {
        Log "downloading azcmagent installer from $AgentUrl"
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        $ProgressPreference = 'SilentlyContinue'
        Invoke-WebRequest -Uri $AgentUrl -OutFile $InstallerPath -UseBasicParsing

        if (-not (Test-Path $InstallerPath)) {
            throw "failed to download azcmagent installer script"
        }

        Log "running azcmagent installer..."
        & $InstallerPath
        if (-not (Test-Path $Azcm)) {
            throw "azcmagent.exe missing after install"
        }
    }
    finally {
        if (Test-Path $InstallerPath) {
            Remove-Item -Path $InstallerPath -Force -ErrorAction SilentlyContinue
        }
    }
}

# Idempotency: if already connected to the same sub+RG AND vmId, exit 0.
# A vmId mismatch (e.g., after `arcify --force` regenerated the keypair) means
# the agent is holding stale identity and won't reach Connected against the
# new Arc resource; disconnect locally so the connect below uses the new key.
$existingJson = & $Azcm show -j 2>$null | Out-String
if ($LASTEXITCODE -eq 0 -and $existingJson) {
    try {
        $existing = $existingJson | ConvertFrom-Json
        if ($existing.resourceId) {
            if ($existing.resourceId -match '^/subscriptions/(?<sub>[^/]+)/resourceGroups/(?<rg>[^/]+)/') {
                $curSub = $Matches['sub']
                $curRg = $Matches['rg']
                if ($curSub -eq $cfg.subscriptionId -and $curRg -eq $cfg.resourceGroup) {
                    if ($existing.vmId -and $existing.vmId -eq $cfg.vmId) {
                        Log "already connected to $($cfg.subscriptionId)/$($cfg.resourceGroup) with matching vmId; nothing to do"
                        & $Azcm show
                        Write-Output "ARCIFY_RESULT=success"
                        exit 0
                    }
                    Log "connected to $($cfg.subscriptionId)/$($cfg.resourceGroup) but vmId mismatch (have $($existing.vmId), want $($cfg.vmId)); disconnecting local state"
                    & $Azcm disconnect --force-local-only
                }
                else {
                    Log "already connected to a different target ($curSub/$curRg); refusing to clobber"
                    Log "run 'azcmagent disconnect' on this VM, then re-run arcify"
                    exit 1
                }
            }
        }
    }
    catch {
        # azcmagent show returned non-JSON output (likely "not connected"); fall through.
        Write-Verbose "ignoring non-JSON azcmagent show output: $_"
    }
}

$arguments = @(
    'connect', 'existing',
    '--subscription-id', $cfg.subscriptionId,
    '--resource-group', $cfg.resourceGroup,
    '--resource-name', $cfg.resourceName,
    '--location', $cfg.location,
    '--tenant-id', $cfg.tenantId,
    '--vmid', $cfg.vmId,
    '--private-key', $cfg.privateKey
)

Log "calling azcmagent connect existing"
& $Azcm @arguments
if ($LASTEXITCODE -ne 0) {
    throw "azcmagent connect failed with exit code $LASTEXITCODE"
}
Log "azcmagent connect succeeded"
& $Azcm show
Write-Output "ARCIFY_RESULT=success"
