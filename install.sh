#!/usr/bin/env bash
# NexOS installer: sets up the hub or an agent on a Debian/Ubuntu server,
# including WireGuard, nftables rules and the docker compose deployment.
#
#   Hub (server A):
#     curl -fsSL https://raw.githubusercontent.com/matelko123/nexosv2/main/install.sh | sudo bash -s -- hub
#   Another server: on the hub run `sudo nexos add-host <name>` and execute
#   the command it prints on the new server.
#
# Once installed, the script is available as `nexos` (run `nexos help`).
set -euo pipefail

REPO=matelko123/nexosv2
REF=${NEXOS_REF:-main}
RAW=https://raw.githubusercontent.com/$REPO/$REF

WG_IF=wg0
WG_NET=10.66.0
WG_PORT=51821
HUB_IP=$WG_NET.1
AGENT_PORT=7443
ENROLL_PORT=7444
UI_PORT=8443

BASE=/opt/nexos
CONF_DIR=/etc/nexos
HUB_CONF=$CONF_DIR/hub.conf
WG_CONF=/etc/wireguard/$WG_IF.conf
FW_FILE=$CONF_DIR/firewall.nft
FW_UNIT=/etc/systemd/system/nexos-firewall.service
SELF=/usr/local/sbin/nexos
MARK="# managed by nexos"

# Directory of a repository checkout when run from one (deploy files are then
# taken from it instead of being downloaded).
SRC=""
if [[ -n ${BASH_SOURCE[0]:-} && -f ${BASH_SOURCE[0]} ]]; then
	d=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
	[[ -f $d/deploy/hub/compose.yaml ]] && SRC=$d
fi

if [[ -t 1 ]]; then B=$'\e[1m' G=$'\e[32m' Y=$'\e[33m' R=$'\e[31m' N=$'\e[0m'; else B="" G="" Y="" R="" N=""; fi
info() { printf '%s==>%s %s\n' "$G" "$N" "$*"; }
warn() { printf '%s!!%s  %s\n' "$Y" "$N" "$*" >&2; }
die() { printf '%serror:%s %s\n' "$R" "$N" "$*" >&2; exit 1; }

# ask VAR "question" [default]: reads from the terminal, also when the script
# itself arrives on stdin (curl | bash).
ask() {
	local __var=$1 q=$2 def=${3:-} ans=""
	if [[ ! -r /dev/tty ]]; then
		[[ -n $def ]] || die "$q: no terminal to ask on, pass it as an option"
		printf -v "$__var" '%s' "$def"
		return
	fi
	printf '%s%s%s%s: ' "$B" "$q" "$N" "${def:+ [$def]}" >/dev/tty
	read -r ans </dev/tty || true
	printf -v "$__var" '%s' "${ans:-$def}"
}

usage() {
	cat <<EOF
NexOS installer

Usage: nexos <command> [options]

  hub                     Install the hub (and a local agent) on this server
      --endpoint HOST[:PORT]  Public address/DDNS name agents dial (WireGuard UDP ${WG_PORT})
      --lan CIDR              LAN allowed to open the UI (default: detected)
      --no-agent              Do not install an agent on the hub server
      --host-features         Enable host features (processes, systemd, apt) for the local agent
      --no-firewall           Do not install nftables rules

  add-host NAME           (hub) Add a server: WireGuard peer + enrollment;
                          prints the command to run on that server
  remove-host NAME        (hub) Remove the server's WireGuard peer

  agent --join TOKEN      Install an agent using a token from add-host
      --host-features         Enable host features (processes, systemd, apt)
      --no-firewall           Do not install nftables rules
  enroll BUNDLE           (agent) Re-enroll with a new nxe1_ bundle from the UI

  uninstall [--purge]     Remove NexOS from this server (--purge also deletes
                          its data volumes; stacks in $BASE/stacks are kept)

Environment: NEXOS_REF (git ref of deploy files, default main),
             NEXOS_GITHUB_TOKEN (only if the images are private)
EOF
}

# --- prerequisites ---------------------------------------------------------

need_root() { [[ $EUID -eq 0 ]] || die "run as root (sudo)"; }

prereqs() {
	need_root
	[[ $(uname -s) == Linux ]] || die "Linux only"
	command -v systemctl >/dev/null || die "systemd is required"
	local pkgs=()
	command -v curl >/dev/null || pkgs+=(curl ca-certificates)
	command -v ip >/dev/null || pkgs+=(iproute2)
	command -v wg >/dev/null || pkgs+=(wireguard-tools)
	command -v nft >/dev/null || pkgs+=(nftables)
	if ((${#pkgs[@]})); then
		command -v apt-get >/dev/null || die "install manually: ${pkgs[*]}"
		info "Installing ${pkgs[*]}"
		DEBIAN_FRONTEND=noninteractive apt-get update -qq
		DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${pkgs[@]}" >/dev/null
	fi
	if ! command -v docker >/dev/null; then
		info "Installing Docker (get.docker.com)"
		curl -fsSL https://get.docker.com | sh
	fi
	docker compose version >/dev/null 2>&1 || die "docker compose plugin is missing"
	systemctl is-active --quiet docker || systemctl enable --now docker
	if [[ -n ${NEXOS_GITHUB_TOKEN:-} ]]; then
		printf '%s' "$NEXOS_GITHUB_TOKEN" | docker login ghcr.io -u "${REPO%%/*}" --password-stdin >/dev/null
	fi
}

fetch() { # fetch <repo path> <destination>
	if [[ -n $SRC ]]; then
		install -m 644 "$SRC/$1" "$2"
	else
		curl -fsSL "$RAW/$1" -o "$2.tmp" && mv "$2.tmp" "$2"
	fi
}

install_self() {
	[[ ${BASH_SOURCE[0]:-} -ef $SELF ]] && return
	if [[ -n $SRC ]]; then
		install -m 755 "$SRC/install.sh" "$SELF"
	else
		curl -fsSL "$RAW/install.sh" -o "$SELF.tmp" && chmod 755 "$SELF.tmp" && mv "$SELF.tmp" "$SELF"
	fi
}

# --- networking ------------------------------------------------------------

default_iface() { ip -4 route show default | awk '{for (i = 1; i < NF; i++) if ($i == "dev") { print $(i + 1); exit }}' || true; }
iface_ip() { ip -4 -o addr show dev "$1" 2>/dev/null | awk 'NR == 1 { split($4, a, "/"); print a[1] }' || true; }
iface_net() { ip -4 route show dev "$1" scope link 2>/dev/null | awk '$1 ~ /\// { print $1; exit }' || true; }

wg_owned() { [[ -f $WG_CONF ]] && grep -qxF "$MARK" "$WG_CONF"; }

wg_check_free() {
	if [[ -f $WG_CONF ]] && ! wg_owned; then
		die "$WG_CONF already exists and is not managed by NexOS; set up WireGuard manually (see README)"
	fi
}

wg_apply() { # restart: address changes need it; peers alone can be synced live
	systemctl enable --quiet "wg-quick@$WG_IF"
	if [[ ${1:-} != restart ]] && systemctl is-active --quiet "wg-quick@$WG_IF"; then
		wg syncconf "$WG_IF" <(wg-quick strip "$WG_IF")
	else
		systemctl restart "wg-quick@$WG_IF"
	fi
}

firewall() { # firewall <rules>
	local nft
	nft=$(command -v nft)
	mkdir -p "$CONF_DIR"
	# Own table, so the host's (and Docker's) rules stay untouched; the
	# delete makes reloading idempotent.
	cat >"$FW_FILE" <<EOF
#!/usr/sbin/nft -f
$MARK
table inet nexos
delete table inet nexos
table inet nexos {
    chain input {
        type filter hook input priority -10; policy accept;
$1
    }
}
EOF
	# After (and part of) nftables.service: its config usually starts with
	# "flush ruleset", which would drop this table.
	cat >"$FW_UNIT" <<EOF
[Unit]
Description=NexOS firewall rules
After=nftables.service
PartOf=nftables.service
Wants=network-pre.target
Before=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=$nft -f $FW_FILE
ExecStop=$nft delete table inet nexos

[Install]
WantedBy=multi-user.target
EOF
	systemctl daemon-reload
	systemctl enable --quiet nexos-firewall
	systemctl restart nexos-firewall
}

# --- docker compose --------------------------------------------------------

compose() { (cd "$1" && shift && docker compose "$@"); }

wait_log() { # wait_log <container> <regex> <seconds>
	local i
	for ((i = 0; i < $3; i++)); do
		docker logs "$1" 2>&1 | grep -qE "$2" && return 0
		sleep 1
	done
	return 1
}

set_env() { # set_env <file> <key> <value>: replace or append
	local f=$1 k=$2 v=$3
	touch "$f"
	sed -i "/^$k=/d" "$f"
	printf '%s=%s\n' "$k" "$v" >>"$f"
}

install_agent() { # install_agent <listen> <bundle> <host features: yes|no>
	local dir=$BASE/agent
	mkdir -p "$dir" "$BASE/stacks"
	fetch deploy/agent/compose.yaml "$dir/compose.yaml"
	fetch deploy/agent/compose.host-features.yaml "$dir/compose.host-features.yaml"
	umask 077
	set_env "$dir/.env" NEXOS_LISTEN "$1"
	set_env "$dir/.env" NEXOS_ENROLL "$2"
	if [[ $3 == yes ]]; then
		set_env "$dir/.env" COMPOSE_FILE compose.yaml:compose.host-features.yaml
	elif [[ $3 == no ]]; then
		sed -i '/^COMPOSE_FILE=/d' "$dir/.env"
	fi
	umask 022
	info "Starting the agent"
	compose "$dir" pull -q
	compose "$dir" up -d
	agent_wait_enrolled
}

agent_wait_enrolled() {
	local dir=$BASE/agent
	info "Waiting for the agent to enroll"
	if wait_log nexos-agent '"msg":"agent listening"' 120; then
		# The bundle is single-use: spent now, or unused by an agent that
		# already has an identity.
		sed -i '/^NEXOS_ENROLL=/d' "$dir/.env"
		if docker logs nexos-agent 2>&1 | grep -q '"msg":"enrolled"'; then
			info "Agent enrolled"
		else
			warn "The agent kept its existing identity (to enroll again: nexos uninstall --purge first)"
		fi
		compose "$dir" up -d
	else
		docker logs --tail 5 nexos-agent >&2 || true
		warn "The agent has not enrolled yet. It keeps retrying; if the bundle expired (15 min),"
		warn "create a new one in the UI (host -> new bundle) and run: sudo nexos enroll <bundle>"
	fi
}

# --- hub -------------------------------------------------------------------

conf_get() { if [[ -f $HUB_CONF ]]; then sed -n "s/^$1=//p" "$HUB_CONF" | tail -1; fi; }

cmd_hub() {
	local endpoint="" lan="" local_agent=yes features=no fw=yes
	while (($#)); do
		case $1 in
		--endpoint) endpoint=${2:?}; shift ;;
		--lan) lan=${2:?}; shift ;;
		--no-agent) local_agent=no ;;
		--host-features) features=yes ;;
		--no-firewall) fw=no ;;
		*) die "unknown option $1" ;;
		esac
		shift
	done
	prereqs
	wg_check_free

	local iface lan_ip
	iface=$(default_iface)
	lan_ip=$(iface_ip "$iface")
	[[ -n $lan ]] || lan=$(iface_net "$iface")

	endpoint=${endpoint:-$(conf_get ENDPOINT)}
	if [[ -z $endpoint ]]; then
		local pub
		pub=$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)
		ask endpoint "Public IP or DDNS name of this location (agents connect to it)" "$pub"
		[[ -n $endpoint ]] || die "the endpoint is required (--endpoint)"
	fi
	[[ $endpoint =~ :[0-9]+$ ]] || endpoint=$endpoint:$WG_PORT
	mkdir -p "$CONF_DIR"
	printf '%s\nENDPOINT=%s\n' "$MARK" "$endpoint" >"$HUB_CONF"

	if ! wg_owned; then
		info "Configuring WireGuard ($WG_IF, $HUB_IP, UDP $WG_PORT)"
		umask 077
		cat >"$WG_CONF" <<EOF
$MARK
[Interface]
Address = $HUB_IP/24
ListenPort = $WG_PORT
PrivateKey = $(wg genkey)
EOF
		umask 022
		wg_apply restart
	else
		wg_apply
	fi

	if [[ $fw == yes ]]; then
		info "Installing firewall rules (UI from ${lan:-WireGuard only})"
		[[ -n $lan ]] || warn "LAN not detected: the UI is reachable only over WireGuard (use --lan)"
		firewall "        # Local agent: only the hub on this host.
        tcp dport $AGENT_PORT iifname \"lo\" ip saddr $HUB_IP accept
        tcp dport $AGENT_PORT drop
        # Enrollment endpoint: WireGuard peers only.
        tcp dport $ENROLL_PORT iifname \"$WG_IF\" accept
        tcp dport $ENROLL_PORT drop
        # Web UI: LAN and WireGuard clients only.
${lan:+        tcp dport $UI_PORT ip saddr $lan accept
}        tcp dport $UI_PORT iifname \"$WG_IF\" accept
        tcp dport $UI_PORT iifname \"lo\" accept
        tcp dport $UI_PORT drop"
	fi

	local dir=$BASE/hub
	mkdir -p "$dir"
	fetch deploy/hub/compose.yaml "$dir/compose.yaml"
	if [[ ! -f $dir/.env ]]; then
		umask 077
		{
			echo "NEXOS_ENROLL_LISTEN=$HUB_IP:$ENROLL_PORT"
			if [[ -n $lan_ip ]]; then echo "NEXOS_UI_HOSTS=$lan_ip"; fi
			if [[ -n ${NEXOS_GITHUB_TOKEN:-} ]]; then echo "NEXOS_UPDATE_GITHUB_TOKEN=$NEXOS_GITHUB_TOKEN"; fi
		} >"$dir/.env"
		umask 022
	fi
	info "Starting the hub"
	compose "$dir" pull -q
	compose "$dir" up -d
	wait_log nexos-hub '"msg":"NexOS hub"' 60 || { docker logs --tail 20 nexos-hub >&2; die "the hub did not start"; }
	install_self

	if [[ $local_agent == yes ]]; then
		if [[ -f $BASE/agent/.env ]]; then
			info "Local agent already installed, updating its deployment"
			compose "$BASE/agent" up -d
		else
			local bundle
			bundle=$(docker exec nexos-hub /nexos-hub add-host "$(hostname)" "$HUB_IP:$AGENT_PORT")
			install_agent "$HUB_IP:$AGENT_PORT" "$bundle" "$features"
		fi
	fi

	local token ui=${lan_ip:-<server-ip>}
	token=$(docker logs nexos-hub 2>&1 | sed -n 's/.*"setup_token":"\([^"]*\)".*/\1/p' | tail -1)
	cat <<EOF

${G}${B}NexOS hub is running.${N}

  UI:           ${B}https://$ui:$UI_PORT${N}   (self-signed certificate)
EOF
	if [[ -n $token ]]; then echo "  Setup token:  ${B}$token${N}"; fi
	cat <<EOF

  Forward ${B}UDP $WG_PORT${N} on your router to this server ($endpoint),
  then add more servers with:  ${B}sudo nexos add-host <name>${N}

EOF
}

next_peer_ip() {
	local n
	n=$(sed -n "s|^AllowedIPs = $WG_NET\.\([0-9]*\)/32|\1|p" "$WG_CONF" | sort -n | tail -1)
	n=$((${n:-1} + 1))
	((n < 255)) || die "no free WireGuard addresses in $WG_NET.0/24"
	echo "$WG_NET.$n"
}

cmd_add_host() {
	need_root
	local name=${1:-}
	[[ $name =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "usage: nexos add-host <name> (letters, digits, . _ -)"
	[[ -f $HUB_CONF ]] && wg_owned || die "no NexOS hub on this server (run: nexos hub)"
	grep -qxF "# peer: $name" "$WG_CONF" && die "peer $name already exists (nexos remove-host $name)"

	local ip key pub psk hub_key bundle endpoint
	endpoint=$(conf_get ENDPOINT)
	ip=$(next_peer_ip)
	key=$(wg genkey)
	pub=$(wg pubkey <<<"$key")
	psk=$(wg genpsk)
	hub_key=$(sed -n 's/^PrivateKey = //p' "$WG_CONF" | wg pubkey)
	bundle=$(docker exec nexos-hub /nexos-hub add-host "$name" "$ip:$AGENT_PORT") || die "the hub refused to add the host"
	cat >>"$WG_CONF" <<EOF

# peer: $name
[Peer]
PublicKey = $pub
PresharedKey = $psk
AllowedIPs = $ip/32
EOF
	wg_apply

	# The peer's private key is generated here and exists only in the token.
	local token
	token=nxj1_$(printf 'ip=%s\nkey=%s\npsk=%s\nhub_key=%s\nendpoint=%s\nenroll=%s\n' \
		"$ip" "$key" "$psk" "$hub_key" "$endpoint" "$bundle" | base64 -w0)
	cat <<EOF

Host ${B}$name${N} added ($ip). Run on that server within 15 minutes:

  curl -fsSL $RAW/install.sh | sudo bash -s -- agent --join $token

The command contains the server's WireGuard key: do not share it.
EOF
}

cmd_remove_host() {
	need_root
	local name=${1:-}
	[[ -n $name ]] || die "usage: nexos remove-host <name>"
	wg_owned && grep -qxF "# peer: $name" "$WG_CONF" || die "no peer $name"
	# Drop the block from its "# peer:" line up to the next one.
	awk -v start="# peer: $name" '
		$0 == start { skip = 1; next }
		skip && /^# peer: / { skip = 0 }
		!skip' "$WG_CONF" >"$WG_CONF.tmp"
	chmod 600 "$WG_CONF.tmp" && mv "$WG_CONF.tmp" "$WG_CONF"
	wg_apply
	info "WireGuard peer $name removed; delete the host in the UI as well"
}

# --- agent -----------------------------------------------------------------

cmd_agent() {
	local token="" features="" fw=yes
	while (($#)); do
		case $1 in
		--join) token=${2:?}; shift ;;
		--host-features) features=yes ;;
		--no-firewall) fw=no ;;
		*) die "unknown option $1" ;;
		esac
		shift
	done
	[[ -n $token ]] || ask token "Join token (from 'nexos add-host' on the hub)"
	[[ $token == nxj1_* ]] || die "invalid join token"

	local line ip="" key="" psk="" hub_key="" endpoint="" enroll=""
	while IFS= read -r line; do
		case ${line%%=*} in
		ip) ip=${line#*=} ;;
		key) key=${line#*=} ;;
		psk) psk=${line#*=} ;;
		hub_key) hub_key=${line#*=} ;;
		endpoint) endpoint=${line#*=} ;;
		enroll) enroll=${line#*=} ;;
		esac
	done < <(printf '%s' "${token#nxj1_}" | base64 -d 2>/dev/null || true)
	local wgkey='^[A-Za-z0-9+/]{43}=$' ipre="^${WG_NET//./[.]}[.][0-9]{1,3}$" hostre='^[][A-Za-z0-9.:-]+$'
	[[ $ip =~ $ipre && $key =~ $wgkey && $psk =~ $wgkey && $hub_key =~ $wgkey &&
		$endpoint =~ $hostre && $enroll == nxe1_* ]] || die "invalid or truncated join token"

	prereqs
	wg_check_free
	info "Configuring WireGuard ($WG_IF, $ip -> $endpoint)"
	umask 077
	cat >"$WG_CONF" <<EOF
$MARK
[Interface]
Address = $ip/24
PrivateKey = $key

[Peer]
PublicKey = $hub_key
PresharedKey = $psk
Endpoint = $endpoint
# Only the hub may be reached through the tunnel.
AllowedIPs = $HUB_IP/32
# Keeps NAT mappings open so the hub can reach this agent at any time.
PersistentKeepalive = 25
EOF
	umask 022
	wg_apply restart

	if [[ $fw == yes ]]; then
		info "Installing firewall rules (agent port only from the hub over $WG_IF)"
		firewall "        tcp dport $AGENT_PORT iifname \"$WG_IF\" ip saddr $HUB_IP accept
        tcp dport $AGENT_PORT drop"
	fi

	info "Waiting for the WireGuard handshake with the hub"
	local i ok=""
	for ((i = 0; i < 30; i++)); do
		if wg show "$WG_IF" latest-handshakes | awk '$2 > 0 { f = 1 } END { exit !f }'; then ok=1; break; fi
		sleep 1
	done
	[[ -n $ok ]] || warn "no handshake with $endpoint yet: check UDP $WG_PORT forwarding on the hub's router"

	install_self
	install_agent "$ip:$AGENT_PORT" "$enroll" "${features:-keep}"
	echo
	info "Done. The host appears in the hub UI as online within a few seconds."
}

cmd_enroll() {
	need_root
	[[ ${1:-} == nxe1_* ]] || die "usage: nexos enroll <nxe1_ bundle from the UI>"
	[[ -f $BASE/agent/.env ]] || die "no NexOS agent on this server"
	umask 077
	set_env "$BASE/agent/.env" NEXOS_ENROLL "$1"
	umask 022
	compose "$BASE/agent" up -d --force-recreate
	agent_wait_enrolled
}

# --- uninstall -------------------------------------------------------------

cmd_uninstall() {
	need_root
	local down=(down)
	[[ ${1:-} == --purge ]] && down=(down -v)
	local c
	for c in agent hub; do
		if [[ -f $BASE/$c/compose.yaml ]]; then
			info "Removing the $c"
			compose "$BASE/$c" "${down[@]}" || true
			rm -rf "${BASE:?}/$c"
		fi
	done
	if [[ -f $FW_UNIT ]]; then
		systemctl disable --now --quiet nexos-firewall || true
		rm -f "$FW_UNIT"
		systemctl daemon-reload
	fi
	if wg_owned; then
		info "Removing WireGuard $WG_IF"
		systemctl disable --now --quiet "wg-quick@$WG_IF" || true
		rm -f "$WG_CONF"
	fi
	rm -rf "$CONF_DIR" "$SELF"
	info "NexOS removed${down[1]:+ (including data)}; $BASE/stacks was kept"
}

case ${1:-help} in
hub) shift; cmd_hub "$@" ;;
agent) shift; cmd_agent "$@" ;;
add-host) shift; cmd_add_host "$@" ;;
remove-host) shift; cmd_remove_host "$@" ;;
enroll) shift; cmd_enroll "$@" ;;
uninstall) shift; cmd_uninstall "$@" ;;
help | -h | --help) usage ;;
*) usage >&2; exit 1 ;;
esac
