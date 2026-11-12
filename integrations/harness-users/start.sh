#!/bin/sh
set -eu
# Agent UIDs cannot bypass the gateway through the host/LAN or sibling services.
iptables -N HARNESS_USERS
iptables -A HARNESS_USERS -d 127.0.0.0/8 -j RETURN
for network in 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 224.0.0.0/4 240.0.0.0/4; do
  iptables -A HARNESS_USERS -d "$network" -j REJECT
done
iptables -A HARNESS_USERS -j RETURN
iptables -A OUTPUT -m owner --uid-owner 100001-160000 -j HARNESS_USERS
# Public repositories remain reachable over IPv4. Do not allow IPv6 LAN bypasses.
ip6tables -A OUTPUT -m owner --uid-owner 100001-160000 ! -d ::1/128 -j REJECT
exec node /opt/harness-users/server.mjs
