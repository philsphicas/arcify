#!/bin/bash
# arcify in-VM enrollment script for Linux (Ubuntu, Azure Linux, etc.).
#
# Invoked by the action-style `POST .../virtualMachines/{vm}/runCommand` API
# (commandId=RunShellScript) with a single named parameter `configB64`. The
# Linux agent exposes runCommand parameters BOTH as named env vars
# ($configB64) AND as positional args ($1). We read the env var first and
# fall back to $1 to stay robust against future agent changes.
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

set -euo pipefail

if [ -z "${configB64:-}" ]; then
  configB64="${1:-}"
fi
if [ -z "$configB64" ]; then
  echo 'arcify: missing configB64 (expected as runCommand parameter)' >&2
  exit 1
fi

# jq is preinstalled on Ubuntu (via cloud-init defaults) and on Azure Linux.
# If somehow missing, install it before parsing.
if ! command -v jq >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq jq
  elif command -v tdnf >/dev/null 2>&1; then
    tdnf install -y jq
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y jq
  elif command -v yum >/dev/null 2>&1; then
    yum install -y jq
  else
    echo "arcify: jq not available and no supported package manager found" >&2
    exit 1
  fi
fi

# Decode + extract in a single in-memory jq pass. @sh shell-quotes each value
# so the eval is safe; nothing ever touches disk.
eval "$(printf '%s' "$configB64" | jq -Rr '
    @base64d | fromjson |
    @sh "SUB=\(.subscriptionId) RG=\(.resourceGroup) LOC=\(.location) TID=\(.tenantId) RN=\(.resourceName) VMID=\(.vmId) PRIV=\(.privateKey)"
')"

for var in SUB RG LOC TID RN VMID PRIV; do
  if [ -z "${!var:-}" ] || [ "${!var}" = "null" ]; then
    echo "arcify: missing required config field for \$$var" >&2
    exit 1
  fi
done

log() { echo "[arcify] $*"; }

# arcify exists specifically to Arc-enroll Azure VMs (testing/sandbox flow).
# The aka.ms/azcmagent install script and `azcmagent connect` refuse to run on
# an Azure VM unless MSFT_ARC_TEST=true is set in the *systemd manager
# environment* (not just the calling process env); see
# https://aka.ms/azcmagent-testwarning. runCommand already runs as root, so
# no sudo is needed.
if command -v systemctl >/dev/null 2>&1; then
  systemctl set-environment MSFT_ARC_TEST=true
fi
export MSFT_ARC_TEST=true

# Install azcmagent if missing. The package puts the binary at
# /opt/azcmagent/bin/azcmagent with a symlink on PATH; use `command -v` so we
# don't depend on the specific layout.
if ! command -v azcmagent >/dev/null 2>&1; then
  log "installing azcmagent..."
  curl -fsSL https://aka.ms/azcmagent | bash
fi
AZCMAGENT="$(command -v azcmagent || true)"
if [ -z "$AZCMAGENT" ] || [ ! -x "$AZCMAGENT" ]; then
  log "azcmagent not found after install"
  exit 1
fi

# Idempotency: if already connected to the same sub+RG AND vmId, exit 0.
# A vmId mismatch (e.g., after `arcify --force` regenerated the keypair) means
# the agent is holding stale identity and won't reach Connected against the
# new Arc resource; disconnect locally so the connect below uses the new key.
if existing=$("$AZCMAGENT" show -j 2>/dev/null); then
  current=$(echo "$existing" | jq -r '.resourceId // empty')
  if [ -n "$current" ]; then
    cur_sub=$(echo "$current" | sed -nE 's|^/subscriptions/([^/]+)/.*|\1|p')
    cur_rg=$(echo "$current" | sed -nE 's|^/subscriptions/[^/]+/resourceGroups/([^/]+)/.*|\1|p')
    cur_vmid=$(echo "$existing" | jq -r '.vmId // empty')
    if [ "$cur_sub" = "$SUB" ] && [ "$cur_rg" = "$RG" ]; then
      if [ "$cur_vmid" = "$VMID" ]; then
        log "already connected to $SUB/$RG with matching vmId; nothing to do"
        "$AZCMAGENT" show
        echo "ARCIFY_RESULT=success"
        exit 0
      fi
      log "connected to $SUB/$RG but vmId mismatch (have ${cur_vmid:-<empty>}, want $VMID); disconnecting local state"
      "$AZCMAGENT" disconnect --force-local-only
    else
      log "already connected to a different target ($cur_sub/$cur_rg); refusing to clobber"
      log "run 'azcmagent disconnect' on this VM, then re-run arcify"
      exit 1
    fi
  fi
fi

log "calling azcmagent connect existing"
"$AZCMAGENT" connect existing \
  --subscription-id "$SUB" \
  --resource-group "$RG" \
  --resource-name "$RN" \
  --location "$LOC" \
  --tenant-id "$TID" \
  --vmid "$VMID" \
  --private-key "$PRIV"
log "azcmagent connect succeeded"
"$AZCMAGENT" show
echo "ARCIFY_RESULT=success"
