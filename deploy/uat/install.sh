#!/bin/sh
# BIN-125 UAT host bootstrap / UAT 主机引导脚本.
# Operator-run as root on Ubuntu 24.04 amd64 only. Installs the three
# project users, directory layout, deploy helper, sudoers rule, systemd
# unit and Caddy config for the UAT host. Accepts only trusted local
# PUBLIC key files; never private keys, never passwords, never curl|sh,
# never repository additions. Actual host execution happens only by Codex
# after review.
# 操作者以 root 运行;仅接收本地受信公钥文件,不做任何远程脚本执行。
#
# Usage / 用法: sudo ./install.sh <upload-public-key-file> <deploy-public-key-file> <public-ipv4-address>
# The third argument is the owner-approved public IPv4 ingress; validated
# before any mutation and rendered into the Caddy config, never stored here.
# 第三个参数为公网 IPv4 入口,先校验后渲染,不入仓库。
set -eu

fail() {
    echo "install: $*" >&2
    exit 1
}

# --- Public ingress address gate / 公网入口地址门槛 ---
# Canonical public unicast IPv4 only, checked with /usr/bin/python3 before
# any host mutation: rejects private, shared, loopback, link-local,
# documentation, multicast, reserved and unspecified ranges. The value is
# one argv element; no shell parsing of untrusted text.
# 公网单播 IPv4,任何变更前校验。
validate_uat_address() {
    _addr=$1
    [ -n "$_addr" ] || fail "uat-address must not be empty"
    [ -x /usr/bin/python3 ] \
        || fail "/usr/bin/python3 is required to validate the UAT address before any host change"
    /usr/bin/python3 -c '
import ipaddress, sys
try:
    ip = ipaddress.IPv4Address(sys.argv[1])
except ValueError:
    sys.exit(1)
ok = ip.is_global and not (ip.is_multicast or ip.is_reserved or ip.is_unspecified)
sys.exit(0 if ok else 1)
' "$_addr" || fail "uat-address must be a canonical public unicast IPv4 dotted quad: $_addr"
}

# --- Caddy release version gate / Caddy 版本门槛 ---
# Short-lived IP certificates need official Caddy >= 2.11.7 (distro 2.6.2 is
# too old; the root orchestrator provisions the official build). No
# repositories, downloads or upgrades here: the gate only refuses.
CADDY_MINIMUM_VERSION=2.11.7

caddy_version_ge() {
    # Numeric dotted triple only; every field is validated as numeric
    # before any comparison, so prerelease/unparseable strings are refused.
    _reported=$1
    case "$_reported" in
        v*) _reported=${_reported#v} ;;
    esac
    awk -v ver="$_reported" -v min="$CADDY_MINIMUM_VERSION" 'BEGIN {
        if (split(ver, v, ".") != 3 || split(min, m, ".") != 3) exit 1
        for (i = 1; i <= 3; i++)
            if (v[i] !~ /^[0-9]+$/ || m[i] !~ /^[0-9]+$/) exit 1
        for (i = 1; i <= 3; i++) {
            if (v[i] + 0 < m[i] + 0) exit 1
            if (v[i] + 0 > m[i] + 0) exit 0
        }
        exit 0
    }'
}

enforce_caddy_minimum() {
    _bin=$1
    _version="$("$_bin" version 2>/dev/null | head -n 1 | cut -d' ' -f1)"
    [ -n "$_version" ] || fail "cannot read a Caddy version from $_bin"
    if ! caddy_version_ge "$_version"; then
        fail "Caddy >= $CADDY_MINIMUM_VERSION is required for short-lived IP certificates; $_bin reports \"$_version\". Install the verified official release first; this script does not add repositories or upgrade Caddy."
    fi
}

# Exact placeholder replacement into the private temp file: the repository
# keeps {$BINFLOW_UAT_ADDRESS}, the generated config the validated literal.
render_caddy_config() {
    _src=$1
    _dst=$2
    awk -v addr="$UAT_ADDRESS" -v ph='{$BINFLOW_UAT_ADDRESS}' '
        {
            line = $0
            out = ""
            while ((n = index(line, ph)) > 0) {
                out = out substr(line, 1, n - 1) addr
                line = substr(line, n + length(ph))
            }
            print out line
        }
    ' "$_src" > "$_dst"
}

BASE=/opt/binflow
RELEASES=/opt/binflow/releases
STAGING_ROOT=/var/lib/binflow
STAGING=/var/lib/binflow/staging
LIBDIR=/usr/local/lib/binflow
HELPER=/usr/local/lib/binflow/uat-release.py
MARKER=/opt/binflow/.binflow-project
UNIT=/etc/systemd/system/binflow-uat.service
SUDOERS=/etc/sudoers.d/binflow-uat-deploy
CADDY_CONF=/etc/caddy/Caddyfile
USERS="binflow binflow-upload binflow-deploy"

[ "$(id -u)" -eq 0 ] || fail "must run as root"

# --- Host gates: Ubuntu 24.04 amd64 only / 仅限 Ubuntu 24.04 amd64 ---
if [ ! -r /etc/os-release ]; then
    fail "/etc/os-release not readable; refusing"
fi
# shellcheck disable=SC1091
. /etc/os-release
[ "${ID-}" = "ubuntu" ] || fail "Ubuntu only, found: ${ID-}"
[ "${VERSION_ID-}" = "24.04" ] || fail "Ubuntu 24.04 only, found: ${VERSION_ID-}"
[ "$(uname -m)" = "x86_64" ] || fail "amd64 (x86_64) only, found: $(uname -m)"
[ -x /usr/sbin/sshd ] || fail "/usr/sbin/sshd not found; forced-command accounts need sshd"

# --- Arguments: two trusted local PUBLIC key files plus the public IPv4 ---
[ "$#" -eq 3 ] || fail "usage: $0 <upload-public-key-file> <deploy-public-key-file> <public-ipv4-address>"
UPLOAD_KEY_FILE=$1
DEPLOY_KEY_FILE=$2
UAT_ADDRESS=$3

validate_public_key() {
    _file=$1
    _label=$2
    if [ ! -f "$_file" ] || [ ! -r "$_file" ]; then
        fail "$_label: not a readable local file: $_file"
    fi
    if grep -q -- 'PRIVATE KEY' "$_file"; then
        fail "$_label: private key material refused; public keys only"
    fi
    _lines=$(grep -cv -e '^#' -e '^[[:space:]]*$' "$_file" || true)
    if [ "$_lines" -ne 1 ]; then
        fail "$_label: expected exactly one public key line, found $_lines"
    fi
    _keytype=$(grep -v -e '^#' -e '^[[:space:]]*$' "$_file" | head -n 1 | cut -d' ' -f1)
    case "$_keytype" in
        ssh-ed25519 | ssh-rsa | ecdsa-sha2-nistp256 | ecdsa-sha2-nistp384 | \
        ecdsa-sha2-nistp521 | sk-ssh-ed25519@openssh.com | sk-ecdsa-sha2-nistp256@openssh.com)
            ;;
        *)
            fail "$_label: unsupported public key type: $_keytype"
            ;;
    esac
    ssh-keygen -lf "$_file" >/dev/null || fail "$_label: invalid public key"
}

validate_public_key "$UPLOAD_KEY_FILE" "upload key"
validate_public_key "$DEPLOY_KEY_FILE" "deploy key"

# Address gate: before any package, user, directory or configuration
# mutation below this point. 公网地址先于一切主机变更校验。
validate_uat_address "$UAT_ADDRESS"

# Fail fast when an already-installed Caddy is too old, before any host
# mutation. The authoritative gate after package installation follows below.
if command -v caddy >/dev/null 2>&1; then
    enforce_caddy_minimum "$(command -v caddy)"
fi

# Keep exactly the key type and blob. Comments must never introduce a
# separate, unrestricted authorized_keys line / 注释不能生成不受限授权行。
UPLOAD_KEY=$(awk '!/^#/ && NF {print $1 " " $2}' "$UPLOAD_KEY_FILE")
DEPLOY_KEY=$(awk '!/^#/ && NF {print $1 " " $2}' "$DEPLOY_KEY_FILE")

# Verify every required source component before any package/host mutation.
# 主机变更前先核实全部安装输入。
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd) || fail "cannot resolve script directory"
helper_src="$script_dir/release.py"
for _f in "$helper_src" "$script_dir/binflow-uat.service" "$script_dir/Caddyfile"; do
    [ -f "$_f" ] && [ ! -L "$_f" ] || fail "missing or linked install component: $_f"
done

user_exists() {
    id "$1" >/dev/null 2>&1
}

# --- Pre-mutation refusals / 变更前拒绝检查 ---
# Without the ownership marker, any existing user, directory, unit, sudoers
# file or Caddy config could belong to an unrelated deployment; the only
# safe action is to refuse and let the operator investigate.
for _path in "$BASE" "$RELEASES" "$STAGING_ROOT" "$STAGING" "$LIBDIR" "$MARKER" "$UNIT" "$SUDOERS" "$CADDY_CONF"; do
    [ ! -L "$_path" ] || fail "linked managed path refused: $_path"
done
for _u in binflow-upload binflow-deploy; do
    for _path in "/home/$_u" "/home/$_u/.ssh" "/home/$_u/.ssh/authorized_keys"; do
        [ ! -L "$_path" ] || fail "linked identity path refused: $_path"
        if [ ! -e "$MARKER" ] && [ -e "$_path" ]; then
            fail "identity path exists without ownership marker: $_path"
        fi
    done
done
if [ -e "$MARKER" ]; then
    [ -f "$MARKER" ] && [ "$(stat -c %u "$MARKER")" = 0 ] && [ "$(stat -c %a "$MARKER")" = 600 ] \
        || fail "untrusted project marker"
    [ "$(head -n 1 "$MARKER")" = 'binflow-uat install (BIN-125)' ] || fail "unexpected project marker"
    for _path in "$BASE" "$RELEASES" "$STAGING_ROOT" "$LIBDIR"; do
        [ -d "$_path" ] && [ "$(stat -c %u "$_path")" = 0 ] && [ "$(stat -c %a "$_path")" = 755 ] \
            || fail "untrusted managed directory: $_path"
    done
else
    for _u in $USERS; do
        if user_exists "$_u"; then
            fail "user $_u exists without the ownership marker $MARKER; refusing"
        fi
    done
    for _path in "$BASE" "$STAGING_ROOT" "$LIBDIR" "$UNIT" "$SUDOERS"; do
        if [ -e "$_path" ]; then
            fail "$_path exists without the ownership marker; refusing"
        fi
    done
    if [ -d /etc/caddy ]; then
        fail "/etc/caddy exists without the ownership marker; refusing"
    fi
fi

# --- Packages: install only what is missing / 仅安装缺失软件包 ---
# The caddy package must come from the host's pre-configured apt sources;
# this script never adds repositories or fetches remote installers.
need=""
command -v python3 >/dev/null 2>&1 || need="python3"
command -v sudo >/dev/null 2>&1 || need="$need sudo"
if command -v caddy >/dev/null 2>&1; then
    fresh_caddy=""
else
    fresh_caddy=1
    need="$need caddy"
fi
if [ -n "$need" ]; then
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    # shellcheck disable=SC2086
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends $need
fi

python3_bin=/usr/bin/python3
sudo_bin=/usr/bin/sudo
caddy_bin=/usr/bin/caddy
for _f in "$python3_bin" "$sudo_bin" "$caddy_bin"; do
    [ -x "$_f" ] || fail "required executable missing: $_f"
done

# Authoritative Caddy version gate: after packages, before the users,
# directories and configuration mutations below this point.
enforce_caddy_minimum "$caddy_bin"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/binflow-uat-install.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

# --- Caddy: render and validate before any host mutation / 渲染校验先于一切变更 ---
# An invalid template must never leave a partly updated deployment: users,
# helper, unit and configs are only written after the rendered file passes
# Caddy's own validation. 坏模板不得部分更新既有部署。
render_caddy_config "$script_dir/Caddyfile" "$tmp/Caddyfile.rendered"
if grep -q -F '{$' "$tmp/Caddyfile.rendered"; then
    fail "rendered Caddyfile still contains an unresolved placeholder"
fi
"$caddy_bin" validate --config "$tmp/Caddyfile.rendered" --adapter caddyfile >/dev/null \
    || fail "rendered Caddyfile failed validation"

# --- Project users / 项目用户 ---
# All three passwords are locked. binflow never logs in. The upload/deploy
# accounts keep /bin/sh only because sshd executes forced commands through
# the account shell; restrict,command= below makes them forced-command-only.
ensure_user() {
    _name=$1
    _home=$2
    _shell=$3
    _comment=$4
    if user_exists "$_name"; then
        _entry=$(getent passwd "$_name") || fail "cannot inspect existing user $_name"
        [ "$(printf '%s' "$_entry" | cut -d: -f3)" -ne 0 ] \
            && [ "$(printf '%s' "$_entry" | cut -d: -f5)" = "$_comment" ] \
            && [ "$(printf '%s' "$_entry" | cut -d: -f6)" = "$_home" ] \
            && [ "$(printf '%s' "$_entry" | cut -d: -f7)" = "$_shell" ] \
            || fail "existing user $_name does not match the project identity"
        return 0
    fi
    useradd --system --home-dir "$_home" --shell "$_shell" -p '*' -c "$_comment" "$_name"
}

ensure_user binflow /nonexistent /usr/sbin/nologin 'BinFlow UAT service account (BIN-125)'
ensure_user binflow-upload /home/binflow-upload /bin/sh 'BinFlow UAT upload forced-command account (BIN-125)'
ensure_user binflow-deploy /home/binflow-deploy /bin/sh 'BinFlow UAT deploy forced-command account (BIN-125)'

# --- Directories / 目录 ---
install -d -o root -g root -m 0755 "$BASE" "$RELEASES" "$STAGING_ROOT" "$LIBDIR"
# Only staging is writable, by the upload account only; root (the activate
# helper) reads it regardless of the mode bits.
install -d -o binflow-upload -g binflow-upload -m 0700 "$STAGING"

# Ownership marker: after this point a rerun is an idempotent project
# update of files this marker proves are ours.
printf 'binflow-uat install (BIN-125)\ndate: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$tmp/marker"
install -o root -g root -m 0600 "$tmp/marker" "$MARKER"

# --- Deploy helper / 部署网关 ---
install -o root -g root -m 0755 "$helper_src" "$HELPER"

# --- SSH forced-command identities / SSH 强制命令身份 ---
# Homes and authorized_keys stay root-owned so the accounts cannot alter
# them; no sshd_config or global auth settings are touched.
for _u in binflow-upload binflow-deploy; do
    install -d -o root -g root -m 0755 "/home/$_u"
    install -d -o root -g root -m 0755 "/home/$_u/.ssh"
done

upload_command="$python3_bin -I $HELPER upload"
deploy_command="$sudo_bin -n $python3_bin -I $HELPER activate"

printf 'restrict,command="%s" %s\n' "$upload_command" "$UPLOAD_KEY" > "$tmp/upload.authorized_keys"
printf 'restrict,command="%s" %s\n' "$deploy_command" "$DEPLOY_KEY" > "$tmp/deploy.authorized_keys"
# Public keys are readable by their account, while only root can change
# the forced command / 公钥可读，只有 root 能修改强制命令。
install -o root -g root -m 0644 "$tmp/upload.authorized_keys" /home/binflow-upload/.ssh/authorized_keys
install -o root -g root -m 0644 "$tmp/deploy.authorized_keys" /home/binflow-deploy/.ssh/authorized_keys

# --- Sudoers: one exact command / 单一固定命令提权 ---
{
    echo '# BIN-125 UAT deploy escalation / UAT 部署提权: 仅此固定命令'
    echo "binflow-deploy ALL=(root) NOPASSWD: $python3_bin -I $HELPER activate"
} > "$tmp/sudoers"
chmod 0440 "$tmp/sudoers"
visudo -cf "$tmp/sudoers" >/dev/null || fail "generated sudoers rule failed visudo validation"
install -o root -g root -m 0440 "$tmp/sudoers" "$SUDOERS"
visudo -c >/dev/null || fail "system-wide sudoers check failed after install"

# --- systemd unit: enable only, never start / 仅启用,绝不启动 ---
install -o root -g root -m 0644 "$script_dir/binflow-uat.service" "$UNIT"
systemctl daemon-reload
systemctl enable binflow-uat.service

# --- Caddy: install the pre-validated config, keep stopped / 安装已校验配置并保持停止 ---
install -d -o root -g root -m 0755 /etc/caddy
install -o root -g root -m 0644 "$tmp/Caddyfile.rendered" "$CADDY_CONF"
if [ -n "$fresh_caddy" ]; then
    # The fresh package auto-starts Caddy; stop it now so the service stays
    # down until the first controlled deployment starts the infrastructure.
    # Reruns never touch a Caddy the operator has already started.
    systemctl disable --now caddy
fi

echo "install: binflow UAT host prepared / UAT 主机就绪"
echo "install: public UAT ingress site address: $UAT_ADDRESS"
echo "install: binflow-uat.service is enabled but NOT started (no release yet)"
echo "install: caddy remains stopped until the first controlled deployment starts it"
echo "install: helper installed at $HELPER"
echo "install: activation channel: $deploy_command (stdin: one JSON line)"
