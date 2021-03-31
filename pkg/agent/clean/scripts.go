package clean

import "runtime"

func Script() string {
	if runtime.GOOS == "windows" {
		return PowershellScript
	}

	return BashScript
}

const BashScript = `#!/bin/bash

# Directories to cleanup
CLEANUP_DIRS=(/etc/ceph /etc/cni /etc/kubernetes /opt/cni /opt/rke /run/secrets/kubernetes.io /run/calico /run/flannel /var/lib/calico /var/lib/weave /var/lib/etcd /var/lib/cni /var/lib/kubelet/* /var/lib/rancher/rke/log /var/log/containers /var/log/pods /var/run/calico)

# Interfaces to cleanup
CLEANUP_INTERFACES=(flannel.1 cni0 tunl0 weave datapath vxlan-6784)

run() {

  CONTAINERS=$(docker ps -qa)
  if [[ -n ${CONTAINERS} ]]
    then
      cleanup-containers
    else
      techo "No containers exist, skipping container cleanup..."
  fi
  cleanup-dirs
  cleanup-interfaces
  VOLUMES=$(docker volume ls -q)
  if [[ -n ${VOLUMES} ]]
    then
      cleanup-volumes
    else
      techo "No volumes exist, skipping container volume cleanup..."
  fi
  if [[ ${CLEANUP_IMAGES} -eq 1 ]]
    then
      IMAGES=$(docker images -q)
      if [[ -n ${IMAGES} ]]
        then
          cleanup-images
        else
          techo "No images exist, skipping container image cleanup..."
      fi
  fi
  if [[ ${FLUSH_IPTABLES} -eq 1 ]]
    then
      flush-iptables
  fi
  techo "Done!"

}

cleanup-containers() {

  techo "Removing containers..."
  docker rm -f $(docker ps -qa)

}

cleanup-dirs() {

  techo "Unmounting filesystems..."
  for mount in $(mount | grep tmpfs | grep '/var/lib/kubelet' | awk '{ print $3 }')
    do
      umount $mount
  done

  techo "Removing directories..."
  for DIR in "${CLEANUP_DIRS[@]}"
    do
      techo "Removing $DIR"
      rm -rf $DIR
  done

}

cleanup-images() {

  techo "Removing images..."
  docker rmi -f $(docker images -q)

}

cleanup-interfaces() {

  techo "Removing interfaces..."
  for INTERFACE in "${CLEANUP_INTERFACES[@]}"
    do
      if $(ip link show ${INTERFACE} > /dev/null 2>&1)
        then
          techo "Removing $INTERFACE"
          ip link delete $INTERFACE
      fi
  done

}

cleanup-volumes() {

  techo "Removing volumes..."
  docker volume rm $(docker volume ls -q)

}

flush-iptables() {

  techo "Flushing iptables..."
  iptables -F -t nat
  iptables -X -t nat
  iptables -F -t mangle
  iptables -X -t mangle
  iptables -F
  iptables -X
  techo "Restarting Docker..."
  if systemctl list-units --full -all | grep -q docker.service
    then
      systemctl restart docker
    else
      /etc/init.d/docker restart
  fi

}

help() {

  echo "Rancher 2.x extended cleanup
  Usage: bash extended-cleanup-rancher2.sh [ -f -i ]

  All flags are optional

  -f | --flush-iptables     Flush all iptables rules (includes a Docker restart)
  -i | --flush-images       Cleanup all container images
  -h                        This help menu

  !! Warning, this script removes containers and all data specific to Kubernetes and Rancher
  !! Backup data as needed before running this script, and use at your own risk."

}

timestamp() {

  date "+%Y-%m-%d %H:%M:%S"

}

techo() {

  echo "$(timestamp): $*"

}

# Check if we're running as root.
if [[ $EUID -ne 0 ]]
  then
    techo "This script must be run as root"
    exit 1
fi

while test $# -gt 0
  do
    case ${1} in
      -f|--flush-iptables)
        shift
        FLUSH_IPTABLES=1
        ;;
      -i|--flush-images)
        shift
        CLEANUP_IMAGES=1
        ;;
      h)
        help && exit 0
        ;;
      *)
        help && exit 0
    esac
done

# Run the cleanup
run
`

const PowershellScript = `#Requires -RunAsAdministrator
<# 
.SYNOPSIS 
    Cleans Rancher managed Windows Worker Nodes. Backup your data. Use at your own risk.
.DESCRIPTION 
    Run the script to clean the windows host of all Rancher related data (kubernetes, docker, network) 
.NOTES
    This script needs to be run with Elevated permissions to allow for the complete collection of information.
    Backup your data.
    Use at your own risk.
.EXAMPLE 
    windows-clean.ps1
    Clean the windows host of all Rancher related data (kubernetes, docker, network).
#>
$ErrorActionPreference = 'Stop'
$WarningPreference = 'SilentlyContinue'
$VerbosePreference = 'SilentlyContinue'
$DebugPreference = 'SilentlyContinue'
$InformationPreference = 'SilentlyContinue'
function Check-Command($cmdname)
{
    return [bool](Get-Command -Name $cmdname -ErrorAction SilentlyContinue)
}
function Log-Info
{
    Write-Host -NoNewline -ForegroundColor Blue "INFO: "
    Write-Host -ForegroundColor Gray ("{0,-44}" -f ($args -join " "))
}
function Log-Warn
{
    Write-Host -NoNewline -ForegroundColor DarkYellow "WARN: "
    Write-Host -ForegroundColor Gray ("{0,-44}" -f ($args -join " "))
}
function Log-Error
{
    Write-Host -NoNewline -ForegroundColor DarkRed "ERRO: "
    Write-Host -ForegroundColor Gray ("{0,-44}" -f ($args -join " "))
}
function Log-Fatal
{
    Write-Host -NoNewline -ForegroundColor DarkRed "FATA: "
    Write-Host -ForegroundColor Gray ("{0,-44}" -f ($args -join " "))
    exit 255
}
function Get-VmComputeNativeMethods()
{
    $ret = 'VmCompute.PrivatePInvoke.NativeMethods' -as [type]
    if (-not $ret) {
        $signature = @'
[DllImport("vmcompute.dll")]
public static extern void HNSCall([MarshalAs(UnmanagedType.LPWStr)] string method, [MarshalAs(UnmanagedType.LPWStr)] string path, [MarshalAs(UnmanagedType.LPWStr)] string request, [MarshalAs(UnmanagedType.LPWStr)] out string response);
'@
        $ret = Add-Type -MemberDefinition $signature -Namespace VmCompute.PrivatePInvoke -Name "NativeMethods" -PassThru
    }
    return $ret
}
function Invoke-HNSRequest
{
    param
    (
        [ValidateSet('GET', 'DELETE')]
        [parameter(Mandatory = $true)] [string] $Method,
        [ValidateSet('networks', 'endpoints', 'activities', 'policylists', 'endpointstats', 'plugins')]
        [parameter(Mandatory = $true)] [string] $Type,
        [parameter(Mandatory = $false)] [string] $Action,
        [parameter(Mandatory = $false)] [string] $Data = "",
        [parameter(Mandatory = $false)] [Guid] $Id = [Guid]::Empty
    )
    $hnsPath = "/$Type"
    if ($id -ne [Guid]::Empty) {
        $hnsPath += "/$id"
    }
    if ($Action) {
        $hnsPath += "/$Action"
    }
    $response = ""
    $hnsApi = Get-VmComputeNativeMethods
    $hnsApi::HNSCall($Method, $hnsPath, "$Data", [ref]$response)
    $output = @()
    if ($response) {
        try {
            $output = ($response | ConvertFrom-Json)
            if ($output.Error) {
                Log-Error $output;
            } else {
                $output = $output.Output;
            }
        } catch {
            Log-Error $_.Exception.Message
        }
    }
    return $output;
}
# cleanup
Log-Info "Start cleaning ..."
# clean up docker container: docker rm -fv $(docker ps -qa)
$containers = $(docker.exe ps -aq)
if ($containers)
{
    Log-Info "Cleaning up docker containers ..."
    $errMsg = $($containers | ForEach-Object {docker.exe rm -f $_})
    if (-not $?) {
        Log-Warn "Could not remove docker containers: $errMsg"
    }
    # wait a while for rancher-wins to clean up processes
    Start-Sleep -Seconds 10
}
# clean up kubernetes components processes
Get-Process -ErrorAction Ignore -Name "rancher-wins-*" | ForEach-Object {
    Log-Info "Stopping process $($_.Name) ..."
    $_ | Stop-Process -ErrorAction Ignore -Force
}
# clean up firewall rules
Get-NetFirewallRule -PolicyStore ActiveStore -Name "rancher-wins-*" -ErrorAction Ignore | ForEach-Object {
    Log-Info "Cleaning up firewall rule $($_.Name) ..."
    $_ | Remove-NetFirewallRule -ErrorAction Ignore | Out-Null
}
# clean up rancher-wins service
Get-Service -Name "rancher-wins" -ErrorAction Ignore | Where-Object {$_.Status -eq "Running"} | ForEach-Object {
    Log-Info "Stopping rancher-wins service ..."
    $_ | Stop-Service -Force -ErrorAction Ignore
    Log-Info "Unregistering rancher-wins service ..."
    Push-Location c:\etc\rancher
    $errMsg = $(.\wins.exe srv app run --unregister)
    if (-not $?) {
        Log-Warn "Could not unregister: $errMsg"
    }
    Pop-Location
}

try {
    Get-HnsNetwork | Where { $_.Name -eq 'vxlan0' -or $_.Name -eq 'cbr0' -or $_.Name -eq 'nat'} | Select Name, ID | ForEach-Object {
        Log-Info "Cleaning up HnsNetwork $($_.Name) ..."
        hnsdiag delete networks ($_.ID)
    }
    Invoke-HNSRequest -Method "GET" -Type "policylists" | Where-Object {-not [string]::IsNullOrEmpty($_.Id)} | ForEach-Object {
Log-Info "Cleaning up HNSPolicyList $($_.Id) ..."
Invoke-HNSRequest -Method "DELETE" -Type "policylists" -Id $_.Id
    }
    Get-HnsEndpoint  | Select Name, ID | ForEach-Object {
        Log-Info "Cleaning up HnsEndpoint $($_.Name) ..."
        hnsdiag delete endpoints ($_.ID)
    }
}
catch {
    Log-Warn "Could not clean: $($_.Exception.Message)"
}

# clean up data
Get-Item -ErrorAction Ignore -Path @(
    "c:\run\*"
    "c:\opt\*"
    "c:\var\*"
    "c:\etc\*"
    "c:\ProgramData\docker\containers\*"
) | ForEach-Object {
    Log-Info "Cleaning up data $($_.FullName) ..."
    try {
        $_ | Remove-Item -ErrorAction Ignore -Recurse -Force | Out-Null
    } catch {
        Log-Warn "Could not clean: $($_.Exception.Message)"
    }
}
try{
    Log-Info "Restarting the Docker service"
    Stop-Service docker
    Start-Sleep -Seconds 5
    start-service docker
}
catch {
    Log-Fatal "Could not restart docker: $($_.Exception.Message)"
}
Log-Info "Finished!"
`
