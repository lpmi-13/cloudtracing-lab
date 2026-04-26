#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
cd "${repo_root}"

if ! command -v npm >/dev/null 2>&1; then
  echo "error: npm is required for the coach UI dev server." >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "error: curl is required for the coach UI dev server health check." >&2
  exit 1
fi

backend_url="${COACH_BACKEND_URL:-http://127.0.0.1:9000}"
ui_port="${COACH_UI_PORT:-5173}"
ui_host="${COACH_UI_HOST:-127.0.0.1}"
dev_url="http://${ui_host}:${ui_port}"
pid_file="/tmp/cloudtracing-coach-ui-dev-${ui_port}.pid"
log_file="/tmp/cloudtracing-coach-ui-dev-${ui_port}.log"
mode="foreground"
vite_args=()

while (($# > 0)); do
  case "$1" in
    --ensure)
      mode="ensure"
      shift
      ;;
    --stop)
      mode="stop"
      shift
      ;;
    --)
      shift
      vite_args+=("$@")
      break
      ;;
    *)
      vite_args+=("$1")
      shift
      ;;
  esac
done

dev_server_ready() {
  curl -fsS --max-time 2 "${dev_url}" 2>/dev/null | grep -q "<title>Trace Coach</title>"
}

remove_stale_pid_file() {
  local pid

  [[ -f "${pid_file}" ]] || return 0
  pid="$(<"${pid_file}")"
  if [[ -z "${pid}" ]] || ! kill -0 "${pid}" 2>/dev/null; then
    rm -f "${pid_file}"
  fi
}

install_dev_dependencies() {
  if [[ ! -d node_modules/vite ]]; then
    echo "Installing coach UI dev dependencies..."
    npm ci
  fi
}

run_vite() {
  local cmd=(npm run coach-ui:dev)

  if (( ${#vite_args[@]} > 0 )); then
    cmd+=(-- "${vite_args[@]}")
  fi

  COACH_BACKEND_URL="${backend_url}" \
  COACH_UI_PORT="${ui_port}" \
  COACH_UI_HOST="${ui_host}" \
  "${cmd[@]}"
}

ensure_dev_server() {
  local cmd=(npm run coach-ui:dev)
  local pid
  local attempt

  remove_stale_pid_file
  if dev_server_ready; then
    echo "Coach UI dev server already running on ${dev_url}"
    return 0
  fi

  install_dev_dependencies

  echo "Starting coach UI dev server on ${dev_url}"
  echo "Proxying coach API requests to ${backend_url}"

  if (( ${#vite_args[@]} > 0 )); then
    cmd+=(-- "${vite_args[@]}")
  fi

  COACH_BACKEND_URL="${backend_url}" \
  COACH_UI_PORT="${ui_port}" \
  COACH_UI_HOST="${ui_host}" \
  nohup "${cmd[@]}" </dev/null >"${log_file}" 2>&1 &
  pid=$!
  echo "${pid}" >"${pid_file}"

  for attempt in $(seq 1 50); do
    if dev_server_ready; then
      echo "Coach UI dev server ready on ${dev_url}"
      echo "Coach UI dev server log: ${log_file}"
      return 0
    fi
    if ! kill -0 "${pid}" 2>/dev/null; then
      break
    fi
    sleep 0.2
  done

  echo "error: coach UI dev server failed to start. Recent log output:" >&2
  tail -n 40 "${log_file}" >&2 || true
  rm -f "${pid_file}"
  return 1
}

stop_dev_server() {
  local pid

  remove_stale_pid_file
  if [[ ! -f "${pid_file}" ]]; then
    echo "Coach UI dev server is not running."
    return 0
  fi

  pid="$(<"${pid_file}")"
  if kill -0 "${pid}" 2>/dev/null; then
    kill "${pid}"
    for _ in $(seq 1 25); do
      if ! kill -0 "${pid}" 2>/dev/null; then
        break
      fi
      sleep 0.2
    done
  fi

  rm -f "${pid_file}"
  echo "Stopped coach UI dev server on ${dev_url}"
}

case "${mode}" in
  ensure)
    ensure_dev_server
    exit 0
    ;;
  stop)
    stop_dev_server
    exit 0
    ;;
esac

install_dev_dependencies

echo "Starting coach UI dev server on ${dev_url}"
echo "Proxying coach API requests to ${backend_url}"

run_vite
