// BIN-124 Jenkins build slice / Jenkins 构建切片.
// BIN-125 adds the binary sha256 manifest line and an explicit DEPLOY_UAT
// staging upload over the forced-command SSH channel.
// Ephemeral Kubernetes-plugin pod on the existing cloud 'kubernetes'; the
// cloud config already sets its pod settings and connection, and the plugin
// injects the jnlp agent. GitHub Actions stays validation-only; CircleCI
// only deploys UAT. The plain archived tar.gz is the initial P0 build
// output — not a product release and not proof of CircleCI delivery.
pipeline {
    agent {
        kubernetes {
            cloud 'kubernetes'
            // Reuse the configured JDK 25 agent / 复用已配置的 JDK 25 Agent。
            inheritFrom 'default'
            defaultContainer 'go'
            yaml '''
apiVersion: v1
kind: Pod
spec:
  automountServiceAccountToken: false
  containers:
  - name: jnlp
    image: docker.m.daocloud.io/jenkins/inbound-agent@sha256:1ee9f71dad02da6598672fd31e9823ec01c0da1976125eb612a5593131096d9f
  - name: go
    image: docker.m.daocloud.io/library/golang@sha256:8d48e12ec56735e9358640898b9d9b9fcca110612ed8a5567438c0a1baa24e66
    command: ['sleep']
    args: ['infinity']
    tty: true
    resources:
      requests:
        cpu: 250m
        memory: 512Mi
      limits:
        cpu: '2'
        memory: 2Gi
'''
        }
    }

    environment {
        // Cluster-local reachability; signed checksum verification stays on.
        // 适配集群网络，保留 Go 签名校验。
        GOPROXY = 'https://goproxy.cn,direct'
        GOSUMDB = 'sum.golang.org'
        // The agent checks out as uid 1000; the package-installing Go
        // container runs as root. Trust only this disposable job workspace,
        // through process configuration, while retaining Go VCS stamping.
        // 仅信任本次临时工作目录，保留 Git 所有权保护与 Go 版本元数据。
        GIT_CONFIG_COUNT = '1'
        GIT_CONFIG_KEY_0 = 'safe.directory'
        GIT_CONFIG_VALUE_0 = "${WORKSPACE}"
    }

    options {
        timeout(time: 15, unit: 'MINUTES')
        disableConcurrentBuilds()
        buildDiscarder(logRotator(numToKeepStr: '10'))
        skipDefaultCheckout()
    }

    parameters {
        // Explicit, operator-selected staging upload only. Never a default
        // step and never taken from PR runs (the stage below refuses those).
        // CircleCI trigger wiring follows account-capability verification;
        // this file does not invent v2 trigger authentication.
        // 仅为显式人工选择的暂存上传,绝不默认执行,PR 构建一律拒绝。
        booleanParam(
            name: 'DEPLOY_UAT',
            defaultValue: false,
            description: 'BIN-125: upload this archived build to the UAT host over the forced-command SSH channel (staging only, never a product release)'
        )
    }

    stages {
        stage('Checkout') {
            steps {
                checkout scm
            }
        }

        stage('Shared check') {
            steps {
                sh label: 'install python3 (pod-local only)', script: '''#!/bin/sh
set -eu
apt-get update -qq
apt-get install -y -qq --no-install-recommends python3
'''
                sh label: 'run shared check', script: './scripts/check.sh'
            }
        }

        stage('Build artifact') {
            steps {
                sh label: 'build, package, checksum', script: '''#!/bin/sh
set -eu
rm -rf "${WORKSPACE:?}"/artifact.*
commit="$(git rev-parse HEAD)"
if [ "${#commit}" -ne 40 ]; then
    echo "unexpected git commit id length: ${#commit}" >&2
    exit 1
fi
case "$commit" in
    *[!0-9a-f]*)
        echo "git commit id is not lowercase hex: $commit" >&2
        exit 1
        ;;
esac
out="$(mktemp -d "${WORKSPACE:?}/artifact.XXXXXX")"
# mktemp -d creates a 0700 dir owned by root (this go container), while
# archiveArtifacts runs in the jnlp container as uid 1000 and needs r-x on
# the directory to traverse into it and read these public build artifacts.
# 0755 adds world read/traverse only — no world write, nothing else changes.
# mktemp -d 生成 root 属主的 0700 目录（go 容器），而 archiveArtifacts 在
# jnlp 容器以 uid 1000 运行，需要目录的可读可进入权限才能遍历并读取这些
# 公开构建制品。0755 仅放开全局读取与进入，不可写，其余均不变。
chmod 0755 "$out"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$out/binflow" ./cmd/binflow
binsha="$(sha256sum "$out/binflow" | cut -d' ' -f1)"
printf 'module: github.com/0ldlight/bf\\ncommit: %s\\ngo: %s\\nplatform: linux/amd64\\ncgo: disabled\\nsha256: %s\\n' \
    "$commit" "$(go env GOVERSION)" "$binsha" > "$out/manifest.txt"
tarball="binflow-${commit}.tar.gz"
tar -czf "$out/$tarball" -C "$out" binflow manifest.txt
( cd "$out" && sha256sum "$tarball" > "$tarball.sha256" )
echo "packaged $tarball"
'''
                archiveArtifacts artifacts: 'artifact.*/binflow-*.tar.gz*', fingerprint: true
            }
        }

        // Explicit controlled staging upload. The workspace copy of the
        // archived tar.gz stays intact through this stage; post/always
        // removes artifact.* only after every stage has finished.
        stage('Upload UAT') {
            when { expression { params.DEPLOY_UAT } }
            steps {
                script {
                    // DEPLOY_UAT is never allowed on PR runs.
                    if (env.CHANGE_ID) {
                        error('DEPLOY_UAT is not allowed on pull request runs')
                    }
                }
                withCredentials([
                    sshUserPrivateKey(
                        credentialsId: 'binflow-uat-upload',
                        keyFileVariable: 'UAT_UPLOAD_KEY'
                    ),
                    file(
                        credentialsId: 'binflow-uat-known-hosts',
                        variable: 'UAT_KNOWN_HOSTS'
                    )
                ]) {
                    sh label: 'upload archived tar.gz to UAT staging', script: '''#!/bin/sh
set -eu
# Exactly one archived tar.gz must be present.
set -- "${WORKSPACE:?}"/artifact.*/binflow-*.tar.gz
if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
    echo "upload-uat: expected exactly one archived binflow-*.tar.gz, found $#" >&2
    exit 1
fi
tarball=$1
commit=${tarball##*/}
commit=${commit#binflow-}
commit=${commit%.tar.gz}
if [ "${#commit}" -ne 40 ]; then
    echo "upload-uat: unexpected commit id length in archive name: ${#commit}" >&2
    exit 1
fi
case "$commit" in
    *[!0-9a-f]*)
        echo "upload-uat: commit id is not lowercase hex: $commit" >&2
        exit 1
        ;;
esac
# Digest of the exact archived bytes; any mismatch fails the build, and a
# failed upload is never treated as success.
digest=$(sha256sum "$tarball" | cut -d' ' -f1)
sidecar="${tarball}.sha256"
if [ ! -f "$sidecar" ]; then
    echo "upload-uat: missing checksum sidecar for $tarball" >&2
    exit 1
fi
expected=$(cut -d' ' -f1 "$sidecar")
if [ "$digest" != "$expected" ]; then
    echo "upload-uat: archive digest mismatch (sidecar $expected, recomputed $digest)" >&2
    exit 1
fi
header=$(printf '{"commit":"%s","sha256":"%s"}' "$commit" "$digest")
# One JSON line, then the raw tar.gz bytes on stdin. The remote account is
# a forced command (python3 -I uat-release.py upload); no scp/SFTP protocol
# and no remote shell are used or available. Keys are never printed.
{ printf '%s\\n' "$header"; cat "$tarball"; } | ssh \\
    -o BatchMode=yes \\
    -o StrictHostKeyChecking=yes \\
    -o IdentitiesOnly=yes \\
    -o IdentityFile="${UAT_UPLOAD_KEY:?}" \\
    -o UserKnownHostsFile="${UAT_KNOWN_HOSTS:?}" \\
    -o ConnectTimeout=10 \\
    -o ConnectionAttempts=1 \\
    binflow-upload@uat.binflow.org
# Record what left the build for the audit trail only.
echo "upload-uat: recorded commit=$commit sha256=$digest"
'''
                }
            }
        }
    }

    post {
        always {
            sh label: 'remove temporary build output', script: '''#!/bin/sh
set -eu
rm -rf "${WORKSPACE:?}"/artifact.*
'''
        }
    }
}
