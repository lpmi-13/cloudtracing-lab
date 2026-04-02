#!/usr/bin/env bash

cloudtracing_all_apps=(
  coach
  edge
  catalog
  inventory
  orders
  shop-web
  payments
  jaeger-ui
)

trim_whitespace() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

canonical_app_name() {
  local value
  value="$(trim_whitespace "$1")"

  case "${value}" in
    coach)
      echo "coach"
      ;;
    edge|edge-api)
      echo "edge"
      ;;
    catalog|catalog-api)
      echo "catalog"
      ;;
    inventory|inventory-api)
      echo "inventory"
      ;;
    orders|orders-api)
      echo "orders"
      ;;
    payments|payments-api)
      echo "payments"
      ;;
    shop|shop-web|web)
      echo "shop-web"
      ;;
    jaeger|jaeger-ui|trace-ui)
      echo "jaeger-ui"
      ;;
    *)
      return 1
      ;;
  esac
}

deployment_name_for_app() {
  case "$1" in
    coach)
      echo "coach"
      ;;
    edge)
      echo "edge-api"
      ;;
    catalog)
      echo "catalog-api"
      ;;
    inventory)
      echo "inventory-api"
      ;;
    orders)
      echo "orders-api"
      ;;
    payments)
      echo "payments-api"
      ;;
    shop-web)
      echo "shop-web"
      ;;
    jaeger-ui)
      echo "jaeger"
      ;;
    *)
      return 1
      ;;
  esac
}

resolve_requested_apps() {
  local raw_apps=()
  local item canonical

  resolved_apps=()

  if (($# > 0)); then
    raw_apps=("$@")
  elif [[ -n "${APPS:-}" ]]; then
    IFS=',' read -r -a raw_apps <<< "${APPS}"
  else
    raw_apps=("${cloudtracing_all_apps[@]}")
  fi

  for item in "${raw_apps[@]}"; do
    item="$(trim_whitespace "${item}")"
    [[ -z "${item}" ]] && continue

    if ! canonical="$(canonical_app_name "${item}")"; then
      echo "error: unknown app '${item}'." >&2
      echo "valid apps: ${cloudtracing_all_apps[*]}" >&2
      exit 1
    fi

    if [[ " ${resolved_apps[*]} " != *" ${canonical} "* ]]; then
      resolved_apps+=("${canonical}")
    fi
  done

  if ((${#resolved_apps[@]} == 0)); then
    echo "error: no apps were selected." >&2
    echo "pass app names as arguments or set APPS, for example APPS=coach or 'bash scripts/refresh-local-app.sh coach'." >&2
    exit 1
  fi
}
