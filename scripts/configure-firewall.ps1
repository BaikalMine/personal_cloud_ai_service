$ErrorActionPreference = 'Stop'

$rules = @(
  'AI Gateway Public 8090 Allow LAN',
  'AI Gateway Public 8090 Allow Keenetic',
  'AI Gateway Public 8090 Temporary Diagnostic Allow LAN',
  'AI Gateway Public 8090 Block Non-LAN IPv4 Temporary Diagnostic',
  'AI Gateway Public 8090 Block Other IPv4',
  'AI Gateway Public 8090 Block IPv6',
  'AI Gateway Admin 8091 Allow LAN',
  'AI Gateway Admin 8091 Block Non-LAN IPv4',
  'AI Gateway Admin 8091 Block IPv6',
  'AI Gateway PostgreSQL 5432 Block LAN Direct Access',
  'AI Gateway HTTP 80 Block LAN Direct Access',
  'AI Gateway HTTPS 443 Block LAN Direct Access'
)

foreach ($name in $rules) {
  Get-NetFirewallRule -DisplayName $name -ErrorAction SilentlyContinue | Remove-NetFirewallRule
}

$nonLanRanges = @(
  '0.0.0.0-126.255.255.255',
  '128.0.0.0-192.168.0.255',
  '192.168.2.0-255.255.255.255'
)
$ipv6Ranges = @('2000::/3','fc00::/7','fe80::/10')

New-NetFirewallRule -DisplayName 'AI Gateway Public 8090 Allow LAN' -Direction Inbound -Action Allow -Enabled True -Protocol TCP -LocalPort 8090 -RemoteAddress 192.168.1.0/24 -Profile Any -Description 'Allow AI Access Gateway public listener from the local IPv4 network' | Out-Null
New-NetFirewallRule -DisplayName 'AI Gateway Public 8090 Block Other IPv4' -Direction Inbound -Action Block -Enabled True -Protocol TCP -LocalPort 8090 -RemoteAddress $nonLanRanges -Profile Any -Description 'Block AI Access Gateway public listener from IPv4 outside loopback and LAN' | Out-Null
New-NetFirewallRule -DisplayName 'AI Gateway Public 8090 Block IPv6' -Direction Inbound -Action Block -Enabled True -Protocol TCP -LocalPort 8090 -RemoteAddress $ipv6Ranges -Profile Any -Description 'Block AI Access Gateway public listener over routable and LAN IPv6' | Out-Null

New-NetFirewallRule -DisplayName 'AI Gateway Admin 8091 Allow LAN' -Direction Inbound -Action Allow -Enabled True -Protocol TCP -LocalPort 8091 -RemoteAddress 192.168.1.0/24 -Profile Any -Description 'Allow AI Access Gateway admin listener only from LAN' | Out-Null
New-NetFirewallRule -DisplayName 'AI Gateway Admin 8091 Block Non-LAN IPv4' -Direction Inbound -Action Block -Enabled True -Protocol TCP -LocalPort 8091 -RemoteAddress $nonLanRanges -Profile Any -Description 'Block AI Access Gateway admin listener from IPv4 outside loopback and LAN' | Out-Null
New-NetFirewallRule -DisplayName 'AI Gateway Admin 8091 Block IPv6' -Direction Inbound -Action Block -Enabled True -Protocol TCP -LocalPort 8091 -RemoteAddress $ipv6Ranges -Profile Any -Description 'Block AI Access Gateway admin listener over routable and LAN IPv6' | Out-Null

New-NetFirewallRule -DisplayName 'AI Gateway PostgreSQL 5432 Block LAN Direct Access' -Direction Inbound -Action Block -Enabled True -Protocol TCP -LocalPort 5432 -RemoteAddress 192.168.1.0/24 -Profile Any -Description 'PostgreSQL is internal Docker only; do not expose on LAN' | Out-Null
New-NetFirewallRule -DisplayName 'AI Gateway HTTP 80 Block LAN Direct Access' -Direction Inbound -Action Block -Enabled True -Protocol TCP -LocalPort 80 -RemoteAddress 192.168.1.0/24 -Profile Any -Description 'Do not expose Windows HTTP 80 for gateway setup' | Out-Null
New-NetFirewallRule -DisplayName 'AI Gateway HTTPS 443 Block LAN Direct Access' -Direction Inbound -Action Block -Enabled True -Protocol TCP -LocalPort 443 -RemoteAddress 192.168.1.0/24 -Profile Any -Description 'Do not expose Windows HTTPS 443 for gateway setup' | Out-Null

Get-NetFirewallRule -DisplayName 'AI Gateway *' | ForEach-Object {
  $port = $_ | Get-NetFirewallPortFilter
  $addr = $_ | Get-NetFirewallAddressFilter
  [pscustomobject]@{
    DisplayName = $_.DisplayName
    Enabled = $_.Enabled
    Action = $_.Action
    Direction = $_.Direction
    Protocol = $port.Protocol
    LocalPort = $port.LocalPort
    RemoteAddress = ($addr.RemoteAddress -join ',')
  }
} | Format-Table -AutoSize
