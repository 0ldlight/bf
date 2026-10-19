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
# Usage / 用法: sudo ./install.sh <upload-public-key-file> <deploy-public-key-file>
set -eu

fail() {
    echo "install: $*" >&2
    exit 1
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

# --- Arguments: exactly two trusted local PUBLIC key files ---
[ "$#" -eq 2 ] || fail "usage: $0 <upload-public-key-file> <deploy-public-key-file>"
UPLOAD_KEY_FILE=$1
DEPLOY_KEY_FILE=$2

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

tmp=$(mktemp -d "${TMPDIR:-/tmp}/binflow-uat-install.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

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

# --- Caddy: validate, install config, keep stopped / 校验后安装并保持停止 ---
"$caddy_bin" validate --config "$script_dir/Caddyfile" --adapter caddyfile >/dev/null \
    || fail "Caddyfile failed validation"
install -d -o root -g root -m 0755 /etc/caddy
install -o root -g root -m 0644 "$script_dir/Caddyfile" "$CADDY_CONF"
if [ -n "$fresh_caddy" ]; then
    # The fresh package auto-starts Caddy; stop it now so the service stays
    # down until the first controlled deployment starts the infrastructure.
    # Reruns never touch a Caddy the operator has already started.
    systemctl disable --now caddy
fi

echo "install: binflow UAT host prepared / UAT 主机就绪"
echo "install: binflow-uat.service is enabled but NOT started (no release yet)"
echo "install: caddy remains stopped until the first controlled deployment starts it"
echo "install: helper installed at $HELPER"
echo "install: activation channel: $deploy_command (stdin: one JSON line)"
