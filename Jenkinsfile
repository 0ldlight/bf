// BIN-124 Jenkins build slice / Jenkins 构建切片.
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
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$out/binflow" ./cmd/binflow
printf 'module: github.com/0ldlight/bf\\ncommit: %s\\ngo: %s\\nplatform: linux/amd64\\ncgo: disabled\\n' \
    "$commit" "$(go env GOVERSION)" > "$out/manifest.txt"
tarball="binflow-${commit}.tar.gz"
tar -czf "$out/$tarball" -C "$out" binflow manifest.txt
( cd "$out" && sha256sum "$tarball" > "$tarball.sha256" )
echo "packaged $tarball"
'''
                archiveArtifacts artifacts: 'artifact.*/binflow-*.tar.gz*', fingerprint: true
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
