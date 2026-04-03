# syntax=docker/dockerfile:1
ARG ROOTFS_RELEASE=e2771a49

FROM ghcr.io/iximiuz/labs/rootfs:ubuntu-k3s-server-${ROOTFS_RELEASE}

USER root
ENV HOME=/root

COPY k8s /opt/cloudtracing/k8s
COPY db /opt/cloudtracing/db
COPY scenarios /opt/cloudtracing/scenarios
COPY scripts/deploy.sh /opt/cloudtracing/scripts/deploy.sh
COPY scripts/deploy-preloaded-vm.sh /opt/cloudtracing/scripts/deploy-preloaded-vm.sh
COPY scripts/lib /opt/cloudtracing/scripts/lib
COPY playground/iximiuz/k3s-images.tar /opt/cloudtracing/playground/iximiuz/k3s-images.tar
COPY playground/iximiuz/image/bootstrap-trace-lab.sh /opt/iximiuz-labs/bootstrap-trace-lab.sh
COPY playground/iximiuz/image/trace-lab-bootstrap.service /etc/systemd/system/trace-lab-bootstrap.service

RUN <<'EOF'
set -eu

mkdir -p /opt/cloudtracing/scripts /opt/cloudtracing/playground/iximiuz /var/lib/cloudtracing

chmod 755 /opt/iximiuz-labs/bootstrap-trace-lab.sh
chmod 755 /opt/cloudtracing/scripts/deploy.sh
chmod 755 /opt/cloudtracing/scripts/deploy-preloaded-vm.sh
test -f /opt/cloudtracing/scripts/lib/apps.sh

for cmd in bash kubectl k3s jq yq rg task just websocat btop; do
  command -v "${cmd}" >/dev/null 2>&1 || {
    echo "missing expected tool: ${cmd}"
    exit 1
  }
done

test -x /root/.fzf/bin/fzf
test -x /home/laborant/.fzf/bin/fzf
grep -q "FZF_DEFAULT_COMMAND='rg --files'" /home/laborant/.bashrc

ln -sf /etc/systemd/system/trace-lab-bootstrap.service \
  /etc/systemd/system/multi-user.target.wants/trace-lab-bootstrap.service
EOF

USER laborant
ENV HOME=/home/laborant
