import json
import os
from typing import Any, Dict


def scenario_file() -> str:
    return os.getenv("SCENARIO_FILE", "/app/scenarios/scenarios.json")


def load_scenarios() -> Dict[str, Dict[str, Any]]:
    with open(scenario_file(), "r", encoding="utf-8") as handle:
        payload = json.load(handle)
    return {item["id"]: item for item in payload}


def fault_for_headers(headers: Dict[str, str], scenarios: Dict[str, Dict[str, Any]], service_name: str) -> Dict[str, Any]:
    scenario_id = headers.get("X-Trace-Lab-Scenario", "")
    scenario = scenarios.get(scenario_id, {})
    return scenario.get("services", {}).get(service_name, {"mode": "baseline", "repeat": 1})

