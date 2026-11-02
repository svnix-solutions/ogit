#!/bin/sh
# Keeps a workspace network off private ranges, cloud metadata and the host
# (Docker's enable_icc=false keeps its containers apart), and leaves the internet open. Safe to run again: the chains
# are rebuilt before they're hooked in.
#
#   OGIT_SUBNET  the workspace network
#   OGIT_BRIDGE  its bridge interface
#   OGIT_ALLOW   ip:port pairs workspaces may still reach (the oGit server)
set -eu

chain() { iptables -N "$1" 2>/dev/null || iptables -F "$1"; }
hook() {
	chain=$1
	shift
	iptables -C "$chain" "$@" 2>/dev/null || iptables -I "$chain" 1 "$@"
}

chain OGIT-WORKSPACES
iptables -A OGIT-WORKSPACES -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
for allow in ${OGIT_ALLOW:-}; do
	iptables -A OGIT-WORKSPACES -p tcp -d "${allow%:*}" --dport "${allow##*:}" -j RETURN
done
for range in 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.0.0.0/24 192.168.0.0/16 198.18.0.0/15 224.0.0.0/4 240.0.0.0/4; do
	iptables -A OGIT-WORKSPACES -d "$range" -j DROP
done

# The host itself: only what's allowed, nothing else on any of its addresses.
chain OGIT-WORKSPACES-HOST
iptables -A OGIT-WORKSPACES-HOST -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
for allow in ${OGIT_ALLOW:-}; do
	iptables -A OGIT-WORKSPACES-HOST -p tcp -d "${allow%:*}" --dport "${allow##*:}" -j RETURN
done
iptables -A OGIT-WORKSPACES-HOST -j DROP

hook DOCKER-USER -s "$OGIT_SUBNET" -j OGIT-WORKSPACES
hook INPUT -i "$OGIT_BRIDGE" -j OGIT-WORKSPACES-HOST
