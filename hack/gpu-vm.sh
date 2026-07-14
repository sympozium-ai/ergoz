#!/usr/bin/env bash
# Throwaway GCP spot VM (1x T4) running a single-node kind cluster with the
# GPU injected, plus ergoz and llmfit-dra — for exercising the real NVML
# path (README: "hardware validation pending").
#
#   hack/gpu-vm.sh up          # create VM + provision end to end (~6 min)
#   hack/gpu-vm.sh provision   # re-run provisioning (idempotent)
#   hack/gpu-vm.sh status      # pods + collector metrics at a glance
#   hack/gpu-vm.sh ssh         # interactive shell (kubectl needs sudo)
#   hack/gpu-vm.sh down        # delete the VM
#
# Cost: n1-standard-4 + T4, both spot — around $0.15-0.25/hr. The VM is
# spot with termination-action DELETE: nothing on it is worth keeping, and
# a preempted-but-stopped VM would silently keep billing its disk.
#
# Requirements:
#   - gcloud authenticated, with a default project (or ERGOZ_VM_PROJECT)
#   - GPU quota in the zone ("Preemptible NVIDIA T4 GPUs" >= 1)
#   - a ghcr token with read:packages for the private images
#     (GHCR_TOKEN, or `gh auth refresh -s read:packages` once and the
#     script takes it from `gh auth token`)
#
# Knobs (env): ERGOZ_VM_NAME, ERGOZ_VM_ZONE, ERGOZ_VM_PROJECT,
#   ERGOZ_VM_MACHINE, ERGOZ_VM_GPU, KIND_NODE_IMAGE, LLMFIT_CHART_VERSION,
#   GHCR_USER, GHCR_TOKEN.

set -euo pipefail

VM_NAME="${ERGOZ_VM_NAME:-ergoz-gpu-test}"
ZONE="${ERGOZ_VM_ZONE:-us-central1-a}"
MACHINE="${ERGOZ_VM_MACHINE:-n1-standard-4}"
GPU="${ERGOZ_VM_GPU:-nvidia-tesla-t4}"
DISK_GB="${ERGOZ_VM_DISK_GB:-80}"
# llmfit-dra needs Kubernetes >= 1.34 (DRA GA — no feature gates).
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.34.0}"
LLMFIT_CHART_VERSION="${LLMFIT_CHART_VERSION:-}" # empty = latest

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ERGOZ_VERSION="$(cat "$REPO_ROOT/version.txt")"

info() { printf '  \033[1;34m>\033[0m %s\n' "$*"; }
err()  { printf '  \033[1;31m!\033[0m %s\n' "$*" >&2; exit 1; }

GFLAGS=(--zone "$ZONE")
[ -n "${ERGOZ_VM_PROJECT:-}" ] && GFLAGS+=(--project "$ERGOZ_VM_PROJECT")

vm_ssh() { gcloud compute ssh "$VM_NAME" "${GFLAGS[@]}" "$@"; }

wait_ssh() {
    info "Waiting for SSH..."
    for _ in $(seq 1 30); do
        vm_ssh --command true >/dev/null 2>&1 && return 0
        sleep 10
    done
    err "VM never became reachable over SSH."
}

ghcr_creds() {
    GHCR_USER="${GHCR_USER:-$(gh api user --jq .login 2>/dev/null || true)}"
    GHCR_TOKEN="${GHCR_TOKEN:-$(gh auth token 2>/dev/null || true)}"
    [ -n "$GHCR_USER" ] && [ -n "$GHCR_TOKEN" ] \
        || err "Need GHCR_USER + GHCR_TOKEN (or an authenticated gh) for the private ghcr images."
}

cmd_up() {
    if gcloud compute instances describe "$VM_NAME" "${GFLAGS[@]}" >/dev/null 2>&1; then
        info "VM ${VM_NAME} already exists — provisioning."
    else
        info "Creating spot VM ${VM_NAME} (${MACHINE} + ${GPU}) in ${ZONE}..."
        gcloud compute instances create "$VM_NAME" "${GFLAGS[@]}" \
            --machine-type "$MACHINE" \
            --accelerator "type=${GPU},count=1" \
            --maintenance-policy TERMINATE \
            --provisioning-model SPOT \
            --instance-termination-action DELETE \
            --image-family ubuntu-2404-lts-amd64 \
            --image-project ubuntu-os-cloud \
            --boot-disk-size "${DISK_GB}GB" \
            --boot-disk-type pd-balanced \
            || err "Create failed — usually missing GPU quota in ${ZONE} (IAM & Admin → Quotas → 'Preemptible NVIDIA T4 GPUs') or no spot T4 capacity; try another zone via ERGOZ_VM_ZONE."
    fi
    wait_ssh
    cmd_provision
}

cmd_provision() {
    ghcr_creds

    local script; script="$(mktemp)"
    # Expand now: $script is function-local and gone when the EXIT trap runs.
    trap "rm -f '$script'" EXIT
    {
        printf 'GHCR_USER=%q\n' "$GHCR_USER"
        printf 'ERGOZ_VERSION=%q\n' "$ERGOZ_VERSION"
        printf 'KIND_NODE_IMAGE=%q\n' "$KIND_NODE_IMAGE"
        printf 'LLMFIT_CHART_VERSION=%q\n' "$LLMFIT_CHART_VERSION"
        cat <<'REMOTE'
set -euo pipefail
IFS= read -r GHCR_TOKEN  # fed via stdin, kept off argv/disk
export DEBIAN_FRONTEND=noninteractive

echo "--- NVIDIA driver"
if ! nvidia-smi >/dev/null 2>&1; then
    apt-get update -q
    apt-get install -yq ubuntu-drivers-common "linux-headers-$(uname -r)"
    ubuntu-drivers install --gpgpu
    # --gpgpu installs the headless driver only; nvidia-smi lives in
    # nvidia-utils, which must match the installed branch (e.g. 580-server).
    flavor="$(dpkg-query -W -f '${Package}\n' 'nvidia-headless-no-dkms-*' 2>/dev/null \
        | sed 's/^nvidia-headless-no-dkms-//; s/-open$//' | sort -V | tail -1)"
    [ -n "$flavor" ] && apt-get install -yq "nvidia-utils-${flavor}"
    modprobe nvidia 2>/dev/null || true
    # Exit 42 = driver installed but not loadable without a reboot; the
    # local side reboots and re-runs this script.
    nvidia-smi >/dev/null 2>&1 || exit 42
fi
nvidia-smi -L

echo "--- Docker + NVIDIA container toolkit"
command -v docker >/dev/null || curl -fsSL https://get.docker.com | sh
if ! command -v nvidia-ctk >/dev/null; then
    curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
        | gpg --dearmor --yes -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
    curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
        | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#' \
        > /etc/apt/sources.list.d/nvidia-container-toolkit.list
    apt-get update -q && apt-get install -yq nvidia-container-toolkit
fi
# nvkind pattern: nvidia as docker's default runtime (kind can't pick a
# runtime per node) + volume-mount device requests, so mounting /dev/null
# at /var/run/nvidia-container-devices/all in the node container makes the
# runtime inject /dev/nvidia* and the driver userspace libs into it.
nvidia-ctk runtime configure --runtime=docker --set-as-default
nvidia-ctk config --in-place --set accept-nvidia-visible-devices-as-volume-mounts=true
systemctl restart docker

echo "--- kind / kubectl / helm"
if [ ! -x /usr/local/bin/kind ]; then
    curl -fsSLo /usr/local/bin/kind https://kind.sigs.k8s.io/dl/latest/kind-linux-amd64
    chmod +x /usr/local/bin/kind
fi
if [ ! -x /usr/local/bin/kubectl ]; then
    curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
    chmod +x /usr/local/bin/kubectl
fi
command -v helm >/dev/null \
    || curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

echo "--- Pull private images (in-VM cache; kind loads them, no in-cluster ghcr pulls needed)"
printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USER" --password-stdin \
    || { echo "ghcr login failed — token needs read:packages (gh auth refresh -s read:packages)"; exit 1; }
printf '%s' "$GHCR_TOKEN" | helm registry login ghcr.io -u "$GHCR_USER" --password-stdin
docker pull "ghcr.io/sympozium-ai/ergoz:v${ERGOZ_VERSION}"
LLMFIT_APPVER="$(helm show chart oci://ghcr.io/sympozium-ai/charts/llmfit-dra \
    ${LLMFIT_CHART_VERSION:+--version "$LLMFIT_CHART_VERSION"} | awk '/^appVersion:/{print $2}')"
# llmfit-dra image tags are bare (0.3.1), unlike ergoz's v-prefixed ones.
docker pull "ghcr.io/sympozium-ai/llmfit-dra:${LLMFIT_APPVER}"

echo "--- kind cluster"
if ! kind get clusters 2>/dev/null | grep -qx ergoz-gpu; then
    cat > /tmp/kind-gpu.yaml <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    image: ${KIND_NODE_IMAGE}
    extraMounts:
      # "give this container all GPUs" in volume-mount form (see toolkit
      # config above).
      - hostPath: /dev/null
        containerPath: /var/run/nvidia-container-devices/all
EOF
    kind create cluster --name ergoz-gpu --config /tmp/kind-gpu.yaml --wait 120s
fi
echo "--- verify GPU injection into the kind node"
docker exec ergoz-gpu-control-plane sh -c 'ls /dev/nvidiactl >/dev/null && ldconfig -p | grep -i libnvidia-ml' \
    || { echo "GPU was NOT injected into the kind node"; exit 1; }
kind load docker-image --name ergoz-gpu \
    "ghcr.io/sympozium-ai/ergoz:v${ERGOZ_VERSION}" \
    "ghcr.io/sympozium-ai/llmfit-dra:${LLMFIT_APPVER}"

echo "--- deploy llmfit-dra + ergoz"
for ns in llmfit-dra ergoz-system; do
    kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
    # Both charts reference a ghcr-pull secret; images are already
    # kind-loaded, but keep the reference resolvable.
    kubectl -n "$ns" create secret docker-registry ghcr-pull \
        --docker-server=ghcr.io --docker-username="$GHCR_USER" \
        --docker-password="$GHCR_TOKEN" \
        --dry-run=client -o yaml | kubectl apply -f -
done
helm upgrade --install llmfit-dra oci://ghcr.io/sympozium-ai/charts/llmfit-dra \
    ${LLMFIT_CHART_VERSION:+--version "$LLMFIT_CHART_VERSION"} -n llmfit-dra
# libHostDir: inside kind, the agent's "host" is the node container, where
# the toolkit injected the driver libs into the standard lib dir.
helm upgrade --install ergoz /tmp/ergoz-chart -n ergoz-system \
    --set image.tag="v${ERGOZ_VERSION}" \
    --set 'imagePullSecrets[0].name=ghcr-pull' \
    --set nvidia.libHostDir=/usr/lib/x86_64-linux-gnu \
    --set nvidia.devAccess=true
kubectl -n llmfit-dra rollout status ds/llmfit-dra --timeout=300s
kubectl -n ergoz-system rollout status ds/ergoz-agent --timeout=300s
kubectl -n ergoz-system rollout status deploy/ergoz-collector --timeout=300s

# Status helper — also what `hack/gpu-vm.sh status` runs from the laptop.
cat > /usr/local/bin/ergoz-vm-status <<'EOS'
#!/bin/bash
export KUBECONFIG=/root/.kube/config
echo "=== pods ==="
kubectl get pods -A
echo "=== DRA resource slices ==="
kubectl get resourceslices 2>/dev/null || echo "(none yet)"
echo "=== agent logs (NVML lines are the interesting part) ==="
kubectl -n ergoz-system logs ds/ergoz-agent --tail=20
echo "=== collector metrics ==="
kubectl -n ergoz-system port-forward svc/ergoz-collector 9744:9744 >/dev/null 2>&1 &
pf=$!; sleep 3
curl -fsS http://127.0.0.1:9744/metrics | grep '^ergoz_' | head -20 \
    || echo "(no ergoz_ metrics yet)"
kill "$pf" 2>/dev/null
EOS
chmod +x /usr/local/bin/ergoz-vm-status
echo
/usr/local/bin/ergoz-vm-status
REMOTE
    } > "$script"

    info "Copying chart + provision script..."
    vm_ssh --command "rm -rf /tmp/ergoz-chart"
    gcloud compute scp --recurse "${GFLAGS[@]}" \
        "$REPO_ROOT/charts/ergoz" "$VM_NAME:/tmp/ergoz-chart"
    gcloud compute scp "${GFLAGS[@]}" "$script" "$VM_NAME:/tmp/ergoz-provision.sh"

    info "Provisioning (driver, docker, toolkit, kind, charts)..."
    local rc=0
    printf '%s\n' "$GHCR_TOKEN" \
        | vm_ssh --command "sudo bash /tmp/ergoz-provision.sh" || rc=$?
    if [ "$rc" -eq 42 ]; then
        info "Driver needs a reboot — rebooting and re-running..."
        # `reboot` returns while sshd is still up, so a plain wait-for-ssh
        # can reconnect to the OLD boot and get killed mid-provision when
        # the reboot lands. Wait for the boot id to actually change.
        local old_boot
        old_boot="$(vm_ssh --command "cat /proc/sys/kernel/random/boot_id" 2>/dev/null || true)"
        vm_ssh --command "sudo reboot" >/dev/null 2>&1 || true
        info "Waiting for reboot..."
        for _ in $(seq 1 30); do
            sleep 10
            new_boot="$(vm_ssh --command "cat /proc/sys/kernel/random/boot_id" 2>/dev/null || true)"
            [ -n "$new_boot" ] && [ "$new_boot" != "$old_boot" ] && break
        done
        [ -n "${new_boot:-}" ] && [ "$new_boot" != "$old_boot" ] \
            || err "VM did not come back from reboot."
        printf '%s\n' "$GHCR_TOKEN" \
            | vm_ssh --command "sudo bash /tmp/ergoz-provision.sh"
    elif [ "$rc" -ne 0 ]; then
        err "Provisioning failed (rc=$rc) — 'hack/gpu-vm.sh ssh' to inspect; re-run with 'hack/gpu-vm.sh provision'."
    fi
    info "Done. 'hack/gpu-vm.sh status' for a snapshot, 'hack/gpu-vm.sh down' when finished."
}

cmd_status() {
    vm_ssh --command "sudo /usr/local/bin/ergoz-vm-status"
}

cmd_down() {
    gcloud compute instances delete "$VM_NAME" "${GFLAGS[@]}" --quiet
    info "Deleted ${VM_NAME}."
}

case "${1:-}" in
    up)        cmd_up ;;
    provision) wait_ssh; cmd_provision ;;
    status)    cmd_status ;;
    ssh)       vm_ssh ;;
    down)      cmd_down ;;
    *)         echo "Usage: hack/gpu-vm.sh up|provision|status|ssh|down"; exit 2 ;;
esac
