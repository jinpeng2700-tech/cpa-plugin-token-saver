import json
from pathlib import Path


REPOSITORY = Path(__file__).resolve().parents[2]
PILOT = REPOSITORY / "testdata" / "pilot"


def load(name: str):
    with (PILOT / name).open("r", encoding="utf-8") as handle:
        return json.load(handle)


schema = load("pilot-report.schema.json")
report = load("pilot-report.example.json")

assert schema["$schema"].endswith("2020-12/schema")
assert schema["properties"]["schema_version"]["const"] == 1
assert report["schema_version"] == 1

assert report["configuration"]["model_allowlist"]
assert report["configuration"]["priority"] == -100
assert report["configuration"]["lower_priority_normalizer_audit"]["enabled_lower_priority_count"] == 0
headroom = report["configuration"]["headroom_security"]
assert set(headroom["listen_addresses"]) <= {"127.0.0.1:8787", "[::1]:8787"}
assert headroom["public_egress_default_denied"] is True
assert headroom["telemetry_disabled"] is True
assert headroom["raw_prompt_logging_disabled"] is True

expected = [
    ("baseline_all_off", []),
    ("rtk", ["rtk"]),
    ("headroom", ["rtk", "headroom"]),
    ("caveman_lite", ["rtk", "headroom", "caveman:lite"]),
    ("ponytail_lite", ["rtk", "headroom", "caveman:lite", "ponytail:lite"]),
]
assert [(stage["name"], stage["enabled_stages"]) for stage in report["stages"]] == expected
for stage in report["stages"]:
    assert 10 <= stage["sample_size"] <= 20
    assert stage["config_generation"] > 0
    assert len(stage["config_digest"]) == 64
    assert "provider_usage" in stage
    assert "byte_measurements" in stage
    assert "resources" in stage
    assert stage["behavior_quality"]["fixed_tasks"] >= 10
    assert not any(stage["technical_red_lines"].values())

baseline = report["stages"][0]
assert baseline["baseline_byte_identical_count"] == baseline["sample_size"]
assert baseline["headroom_call_count"] == 0

stop = report["stop_drill"]
assert stop["confirmation_seconds"] <= 30
assert stop["pipeline"] == "all_bypassed"
assert stop["old_generation_inflight_zero"] is True
assert stop["byte_identical_fixture"] is True
assert stop["headroom_calls_after_disable"] == 0
assert not any(report["privacy_attestation"].values())

serialized = json.dumps(report, ensure_ascii=False)
for sentinel in ("Bearer ", '"sk-', "sk-proj-", "BEGIN PRIVATE KEY", "production prompt text"):
    assert sentinel not in serialized

print("pilot fixture contracts: PASS")
