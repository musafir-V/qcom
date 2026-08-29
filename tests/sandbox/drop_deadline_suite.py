#!/usr/bin/env python3
"""Bunzo sandbox drop-deadline functional suite. Hits only 127.0.0.1:8080 + local Dynamo."""
from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from typing import Any

BASE = os.environ.get("BASE_URL", "http://127.0.0.1:8080")
DDB = "http://127.0.0.1:8000"
TABLE = "QComTable"
PHONE = "+15550000001"
DE_ID = "DE0458047115"
STORE = "ST0001"
AWS_ENV = {
    **os.environ,
    "AWS_ACCESS_KEY_ID": os.environ.get("AWS_ACCESS_KEY_ID", "dummy"),
    "AWS_SECRET_ACCESS_KEY": os.environ.get("AWS_SECRET_ACCESS_KEY", "dummy"),
    "AWS_DEFAULT_REGION": os.environ.get("AWS_DEFAULT_REGION", "us-east-1"),
    "AWS_EC2_METADATA_DISABLED": "true",
}

results: list[tuple[str, str, str]] = []
_seq = 0


def _now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _next_ids() -> tuple[str, str, str, str]:
    global _seq
    _seq += 1
    stamp = int(time.time()) % 100000
    tid = f"TR{stamp:05d}{_seq:03d}"
    oid = f"ORD{stamp:05d}{_seq:03d}"
    return tid, oid, f"P{_seq}", f"D{_seq}"


def http(
    method: str,
    path: str,
    *,
    token: str | None = None,
    body: Any = None,
    extra_headers: dict[str, str] | None = None,
    raw_body: bytes | None = None,
    content_type: str = "application/json",
) -> tuple[int, Any, str]:
    url = path if path.startswith("http") else BASE + path
    data = None
    headers = {"Accept": "application/json"}
    if raw_body is not None:
        data = raw_body
        headers["Content-Type"] = content_type
    elif body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if extra_headers:
        headers.update(extra_headers)
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            raw = resp.read().decode("utf-8", errors="replace")
            code = resp.getcode()
    except urllib.error.HTTPError as e:
        raw = e.read().decode("utf-8", errors="replace")
        code = e.code
    parsed: Any = raw
    if raw:
        try:
            parsed = json.loads(raw)
        except json.JSONDecodeError:
            parsed = raw
    return code, parsed, raw


def aws_ddb(args: list[str]) -> Any:
    cmd = ["aws", "dynamodb", *args, "--endpoint-url", DDB, "--output", "json"]
    p = subprocess.run(cmd, env=AWS_ENV, capture_output=True, text=True)
    if p.returncode != 0:
        raise RuntimeError(f"aws {' '.join(args[:3])} failed: {p.stderr.strip() or p.stdout.strip()}")
    return json.loads(p.stdout) if p.stdout.strip() else {}


def rec(tc: str, status: str, reason: str) -> None:
    results.append((tc, status, reason))


def admin_login() -> str:
    code, body, _ = http(
        "POST",
        "/api/v1/admin/login",
        body={"username": "sandbox", "password": "sandboxadmin"},
    )
    if code != 200 or not isinstance(body, dict) or not body.get("token"):
        raise RuntimeError(f"admin login failed {code} {body}")
    return body["token"]


def rider_token() -> str:
    http(
        "POST",
        "/api/v1/auth/initiate-otp",
        body={"phone_number": PHONE},
        extra_headers={"X-App-Type": "de"},
    )
    code, body, _ = http(
        "POST",
        "/api/v1/auth/verify-otp",
        body={"phone_number": PHONE, "otp": "112233"},
        extra_headers={"X-App-Type": "de"},
    )
    if code != 200 or not isinstance(body, dict) or not body.get("access_token"):
        raise RuntimeError(f"rider otp failed {code} {body}")
    return body["access_token"]


def customer_token() -> str:
    http("POST", "/api/v1/auth/initiate-otp", body={"phone_number": PHONE})
    code, body, _ = http(
        "POST",
        "/api/v1/auth/verify-otp",
        body={"phone_number": PHONE, "otp": "112233"},
    )
    if code != 200 or not isinstance(body, dict) or not body.get("access_token"):
        raise RuntimeError(f"customer otp failed {code} {body}")
    return body["access_token"]


def get_cfg(admin: str) -> tuple[int, Any]:
    code, body, _ = http("GET", "/api/v1/admin/config/drop-deadline", token=admin)
    return code, body


def patch_cfg(admin: str, x: Any, y: Any) -> tuple[int, Any]:
    code, body, _ = http(
        "PATCH",
        "/api/v1/admin/config/drop-deadline",
        token=admin,
        body={"minutes_per_km": x, "extra_minutes": y},
    )
    return code, body


def delete_cfg() -> None:
    aws_ddb(
        [
            "delete-item",
            "--table-name",
            TABLE,
            "--key",
            json.dumps({"PK": {"S": "CONFIG"}, "SK": {"S": "DROP_DEADLINE_V1"}}),
        ]
    )


def put_cfg_partial(*, x: float | None = None, y: float | None = None) -> None:
    item: dict[str, Any] = {"PK": {"S": "CONFIG"}, "SK": {"S": "DROP_DEADLINE_V1"}}
    if x is not None:
        item["minutes_per_km"] = {"N": str(x)}
    if y is not None:
        item["extra_minutes"] = {"N": str(y)}
    aws_ddb(["put-item", "--table-name", TABLE, "--item", json.dumps(item)])


def ddb_get_trip(trip_id: str) -> dict[str, Any] | None:
    out = aws_ddb(
        [
            "get-item",
            "--table-name",
            TABLE,
            "--key",
            json.dumps({"PK": {"S": f"TRIP!{trip_id}"}, "SK": {"S": "METADATA"}}),
        ]
    )
    return out.get("Item")


def ddb_n(item: dict[str, Any] | None, key: str) -> float | None:
    if not item or key not in item:
        return None
    v = item[key]
    if "N" in v:
        return float(v["N"])
    return None


def ddb_s(item: dict[str, Any] | None, key: str) -> str | None:
    if not item or key not in item:
        return None
    v = item[key]
    return v.get("S")


def seed_trip(km: float, trip_id: str | None = None, order_id: str | None = None) -> tuple[str, str, str, str]:
    tid, oid, pid, did = _next_ids()
    if trip_id:
        tid = trip_id
    if order_id:
        oid = order_id
    now = _now_iso()
    item = {
        "PK": {"S": f"TRIP!{tid}"},
        "SK": {"S": "METADATA"},
        "trip_id": {"S": tid},
        "order_id": {"S": oid},
        "trip_order_id": {"S": oid},
        "status": {"S": "created"},
        "distance_km": {"N": str(km)},
        "store_id": {"S": STORE},
        "darkstore_id": {"S": STORE},
        "customer_id": {"S": "US0458047115"},
        "created_at": {"S": now},
        "updated_at": {"S": now},
        "tasks": {
            "L": [
                {
                    "M": {
                        "task_id": {"S": pid},
                        "type": {"S": "pickup"},
                        "status": {"S": "pending"},
                        "display_order": {"N": "1"},
                    }
                },
                {
                    "M": {
                        "task_id": {"S": did},
                        "type": {"S": "drop"},
                        "status": {"S": "pending"},
                        "display_order": {"N": "2"},
                    }
                },
            ]
        },
    }
    aws_ddb(["put-item", "--table-name", TABLE, "--item", json.dumps(item)])
    return tid, oid, pid, did


def set_de_eligible(clear_current: bool = False) -> None:
    if clear_current:
        expr = "SET #s = :s REMOVE current_trip_id, current_order_id"
    else:
        expr = "SET #s = :s"
    aws_ddb(
        [
            "update-item",
            "--table-name",
            TABLE,
            "--key",
            json.dumps({"PK": {"S": f"DE!{PHONE}"}, "SK": {"S": "METADATA"}}),
            "--update-expression",
            expr,
            "--expression-attribute-names",
            json.dumps({"#s": "status"}),
            "--expression-attribute-values",
            json.dumps({":s": {"S": "eligible"}}),
        ]
    )


def set_de_busy(trip_id: str, order_id: str) -> None:
    aws_ddb(
        [
            "update-item",
            "--table-name",
            TABLE,
            "--key",
            json.dumps({"PK": {"S": f"DE!{PHONE}"}, "SK": {"S": "METADATA"}}),
            "--update-expression",
            "SET #s = :s, current_trip_id = :tid, current_order_id = :oid, current_store_id = :store",
            "--expression-attribute-names",
            json.dumps({"#s": "status"}),
            "--expression-attribute-values",
            json.dumps(
                {
                    ":s": {"S": "busy"},
                    ":tid": {"S": trip_id},
                    ":oid": {"S": order_id},
                    ":store": {"S": STORE},
                }
            ),
        ]
    )


def assign(admin: str, order_id: str) -> tuple[int, Any]:
    return http(
        "POST",
        "/api/v1/admin/assign",
        token=admin,
        body={"order_id": order_id, "driver_phone": PHONE},
    )[:2]


def pickup(admin: str) -> tuple[int, Any]:
    return http(
        "POST",
        f"/api/v1/admin/drivers/{PHONE}/trip/pickup/complete",
        token=admin,
        body={},
    )[:2]


def drop(admin: str) -> tuple[int, Any]:
    return http(
        "POST",
        f"/api/v1/admin/drivers/{PHONE}/trip/drop/complete",
        token=admin,
        body={},
    )[:2]


def get_trip(rider: str) -> Any:
    code, body, _ = http(
        "GET",
        "/api/v1/de/trip",
        token=rider,
        extra_headers={"X-App-Type": "de"},
    )
    if code != 200:
        return None
    if isinstance(body, dict):
        return body.get("trip")
    return None


def parse_epoch(ts: str | None) -> float | None:
    if not ts:
        return None
    try:
        if ts.endswith("Z"):
            ts = ts[:-1] + "+00:00"
        return datetime.fromisoformat(ts).timestamp()
    except ValueError:
        return None


def pickup_epoch_from_trip(trip: dict[str, Any] | None) -> float | None:
    if not trip:
        return None
    for t in trip.get("tasks") or []:
        if t.get("type") == "pickup" and t.get("completed_at"):
            return parse_epoch(t.get("completed_at"))
    return None


def allowed_from_deadline(deadline: float | None, pickup_ep: float | None) -> float | None:
    if deadline is None or pickup_ep is None:
        return None
    return (float(deadline) - float(pickup_ep)) / 60.0


def close_enough(got: float | None, expected: float, tol: float = 0.2) -> bool:
    if got is None:
        return False
    return abs(got - expected) <= tol


def force_complete_trip(trip_id: str) -> None:
    item = ddb_get_trip(trip_id)
    if not item:
        return
    aws_ddb(
        [
            "update-item",
            "--table-name",
            TABLE,
            "--key",
            json.dumps({"PK": {"S": f"TRIP!{trip_id}"}, "SK": {"S": "METADATA"}}),
            "--update-expression",
            "SET #s = :s",
            "--expression-attribute-names",
            json.dumps({"#s": "status"}),
            "--expression-attribute-values",
            json.dumps({":s": {"S": "completed"}}),
        ]
    )


def scan_active_trips() -> list[str]:
    out = aws_ddb(
        [
            "scan",
            "--table-name",
            TABLE,
            "--filter-expression",
            "begins_with(PK, :p)",
            "--expression-attribute-values",
            json.dumps({":p": {"S": "TRIP!"}}),
        ]
    )
    ids = []
    for it in out.get("Items") or []:
        st = ddb_s(it, "status")
        if st in {"created", "assigned", "accepted", "out_for_delivery", "ofd"}:
            ids.append(ddb_s(it, "trip_id") or it["PK"]["S"].split("!", 1)[-1])
    return ids


def cleanup_active() -> None:
    for tid in scan_active_trips():
        force_complete_trip(tid)
    set_de_eligible(clear_current=True)


def seed_assign(admin: str, rider: str, km: float) -> tuple[str, str, Any]:
    """Seed CREATED trip, make DE eligible, assign, return (trip_id, order_id, trip_json)."""
    tid, oid, _, _ = seed_trip(km)
    set_de_eligible(clear_current=False)
    code, body = assign(admin, oid)
    if code != 200:
        set_de_eligible(clear_current=True)
        code, body = assign(admin, oid)
    if code != 200:
        raise RuntimeError(f"assign {oid} failed {code} {body}")
    trip = get_trip(rider)
    return tid, oid, trip


def do_pickup(admin: str, rider: str) -> tuple[int, Any, Any]:
    t0 = time.time()
    code, body = pickup(admin)
    t1 = time.time()
    trip = get_trip(rider)
    return code, body, trip


def do_drop(admin: str, trip_id: str, order_id: str) -> tuple[int, Any]:
    set_de_busy(trip_id, order_id)
    return drop(admin)


def earnings(rider: str) -> Any:
    code, body, _ = http(
        "GET",
        "/api/v1/de/earnings/summary",
        token=rider,
        extra_headers={"X-App-Type": "de"},
    )
    return body if code == 200 else None


def pay_for_trip(rider: str, trip_id: str) -> float | None:
    item = ddb_get_trip(trip_id)
    total = ddb_n(item, "total_pay_zmw")
    if total is not None:
        return total
    summ = earnings(rider)
    if isinstance(summ, dict):
        for li in summ.get("line_items") or []:
            if li.get("reference_id") == trip_id:
                return float(li.get("amount_zmw") or 0)
    return None


def seed_payout() -> None:
    # string values required by PATCH /api/v1/config/payout
    for field, value in (
        ("rate_per_km_zmw", "10"),
        ("rate_flat_zmw", "0"),
        ("base_pay_zmw", "0"),
        ("base_pay", "0"),
        ("rate_per_km", "10"),
    ):
        http("PATCH", "/api/v1/config/payout", body={"field": field, "value": value})


def deadline_of(trip: Any, trip_id: str | None = None) -> float | None:
    if isinstance(trip, dict) and trip.get("drop_deadline") is not None:
        return float(trip["drop_deadline"])
    if trip_id:
        return ddb_n(ddb_get_trip(trip_id), "drop_deadline")
    return None


def has_deadline(trip: Any) -> bool:
    if not isinstance(trip, dict) or trip is None:
        return False
    return "drop_deadline" in trip and trip["drop_deadline"] is not None


def main() -> int:
    # health
    code, body, raw = http("GET", "/health")
    if code != 200 or raw.strip() != "OK":
        print("SANDBOX health failed; aborting", file=sys.stderr)
        return 2

    admin = admin_login()
    rider = rider_token()
    customer = customer_token()
    seed_payout()
    cleanup_active()

    # ---------- Happy / Auth / Errors share X,Y progression ----------
    # TC-01
    code, body = patch_cfg(admin, 3, 10)
    code2, got = get_cfg(admin)
    if code == 200 and isinstance(got, dict) and got.get("minutes_per_km") == 3 and got.get("extra_minutes") == 10:
        rec("TC-01", "PASS", "admin PATCH X=3 Y=10 accepted and GET shows 3/10")
    else:
        rec("TC-01", "FAIL", f"PATCH {code} {body} GET {code2} {got}")

    # TC-02
    code, got = get_cfg(admin)
    if code == 200 and isinstance(got, dict) and got.get("minutes_per_km") == 3 and got.get("extra_minutes") == 10:
        rec("TC-02", "PASS", "admin GET shows X=3 Y=10")
    else:
        rec("TC-02", "FAIL", f"GET {code} {got}")

    # TC-03
    try:
        tid04, oid04, trip = seed_assign(admin, rider, 4)
        if trip and not has_deadline(trip):
            rec("TC-03", "PASS", "assigned 4km trip omits drop_deadline before pickup")
        elif trip is None:
            rec("TC-03", "FAIL", "GET /de/trip returned null after assign")
        else:
            rec("TC-03", "FAIL", f"drop_deadline present before pickup: {trip.get('drop_deadline')}")
    except Exception as e:
        rec("TC-03", "FAIL", f"setup/assign failed: {e}")
        tid04 = oid04 = ""
        trip = None

    # TC-04
    if not tid04:
        rec("TC-04", "FAIL", "no assigned trip from TC-03")
        rec("TC-05", "FAIL", "no running trip from TC-04")
        rec("TC-06", "FAIL", "no running trip from TC-04")
    else:
        code, body, trip = do_pickup(admin, rider)
        dd = deadline_of(trip, tid04)
        pep = pickup_epoch_from_trip(trip)
        allowed = allowed_from_deadline(dd, pep)
        if code != 200:
            rec("TC-04", "FAIL", f"pickup HTTP {code} {body}")
        elif dd is None:
            rec("TC-04", "FAIL", "drop_deadline omitted after pickup")
        elif not close_enough(allowed, 22.0):
            rec("TC-04", "FAIL", f"allowed={allowed} expected 22 (4*3+10); deadline={dd} pickup_ep={pep}")
        else:
            rec("TC-04", "PASS", f"pickup set drop_deadline epoch {int(dd)}; allowed {allowed:.2f} min (4*3+10=22)")

        # TC-05
        if dd is None:
            rec("TC-05", "FAIL", "no deadline to watch")
        else:
            rem0 = dd - time.time()
            time.sleep(3)
            trip5 = get_trip(rider)
            dd5 = deadline_of(trip5, tid04)
            rem1 = (dd5 if dd5 is not None else dd) - time.time()
            stored_same = dd5 is None or abs(dd5 - dd) < 0.001
            if stored_same and rem1 < rem0 - 1.5:
                rec("TC-05", "PASS", f"remaining dropped {rem0:.1f}s -> {rem1:.1f}s; stored deadline unchanged")
            elif not stored_same:
                rec("TC-05", "FAIL", f"GET recomputed/changed deadline {dd} -> {dd5}")
            else:
                rec("TC-05", "FAIL", f"remaining did not drop enough ({rem0:.1f}s -> {rem1:.1f}s)")

        # TC-06
        if dd is None:
            rec("TC-06", "FAIL", "no live trip to complete")
        else:
            code, body = do_drop(admin, tid04, oid04)
            item = ddb_get_trip(tid04)
            st = ddb_s(item, "status")
            pay = pay_for_trip(rider, tid04)
            if code == 200 and st == "completed" and pay is not None and pay > 0:
                rec("TC-06", "PASS", f"on-time drop completed; total_pay_zmw={pay}")
                tc06_pay = pay
            elif code == 200 and st == "completed":
                rec("TC-06", "FAIL", f"drop completed but pay unverifiable (total_pay_zmw={pay})")
                tc06_pay = None
            else:
                rec("TC-06", "FAIL", f"drop HTTP {code} {body} status={st} pay={pay}")
                tc06_pay = None
    if "tc06_pay" not in locals():
        tc06_pay = None

    # TC-07
    cleanup_active()
    patch_cfg(admin, 3, 10)
    try:
        tid07, oid07, _ = seed_assign(admin, rider, 5)
        code, body, trip = do_pickup(admin, rider)
        dd = deadline_of(trip, tid07)
        pep = pickup_epoch_from_trip(trip)
        allowed = allowed_from_deadline(dd, pep)
        if close_enough(allowed, 25.0):
            rec("TC-07", "PASS", f"new 5km trip allowed {allowed:.2f} (5*3+10=25)")
        else:
            rec("TC-07", "FAIL", f"allowed={allowed} expected 25; deadline={dd}")
        if tid07:
            do_drop(admin, tid07, oid07)
    except Exception as e:
        rec("TC-07", "FAIL", f"setup failed: {e}")

    cleanup_active()
    patch_cfg(admin, 3, 10)

    # ---------- Auth ----------
    code_r, body_r = http(
        "PATCH",
        "/api/v1/admin/config/drop-deadline",
        token=rider,
        extra_headers={"X-App-Type": "de"},
        body={"minutes_per_km": 99, "extra_minutes": 10},
    )[:2]
    _, after = get_cfg(admin)
    if code_r in (401, 403) and isinstance(after, dict) and after.get("minutes_per_km") == 3:
        rec("TC-08", "PASS", f"rider PATCH rejected {code_r}; X stays 3")
    else:
        rec("TC-08", "FAIL", f"rider PATCH {code_r} {body_r}; after={after}")

    code_r, body_r = http(
        "PATCH",
        "/api/v1/admin/config/drop-deadline",
        token=rider,
        extra_headers={"X-App-Type": "de"},
        body={"minutes_per_km": 3, "extra_minutes": 99},
    )[:2]
    _, after = get_cfg(admin)
    if code_r in (401, 403) and isinstance(after, dict) and after.get("extra_minutes") == 10:
        rec("TC-09", "PASS", f"rider PATCH Y rejected {code_r}; Y stays 10")
    else:
        rec("TC-09", "FAIL", f"rider PATCH {code_r} {body_r}; after={after}")

    code_s, body_s = http(
        "PATCH",
        "/api/v1/admin/config/drop-deadline",
        body={"minutes_per_km": 99, "extra_minutes": 99},
    )[:2]
    _, after = get_cfg(admin)
    if code_s in (401, 403) and isinstance(after, dict) and after.get("minutes_per_km") == 3 and after.get("extra_minutes") == 10:
        rec("TC-10", "PASS", f"signed-out PATCH rejected {code_s}; X/Y stay 3/10")
    else:
        rec("TC-10", "FAIL", f"signed-out PATCH {code_s} {body_s}; after={after}")

    code_c, body_c = http(
        "PATCH",
        "/api/v1/admin/config/drop-deadline",
        token=customer,
        body={"minutes_per_km": 99, "extra_minutes": 99},
    )[:2]
    _, after = get_cfg(admin)
    if code_c in (401, 403) and isinstance(after, dict) and after.get("minutes_per_km") == 3 and after.get("extra_minutes") == 10:
        rec("TC-11", "PASS", f"customer PATCH rejected {code_c}; X/Y stay 3/10")
    else:
        rec("TC-11", "FAIL", f"customer PATCH {code_c} {body_c}; after={after}")

    code, body = patch_cfg(admin, 4, 2)
    _, got = get_cfg(admin)
    if code == 200 and isinstance(got, dict) and got.get("minutes_per_km") == 4 and got.get("extra_minutes") == 2:
        rec("TC-12", "PASS", "admin changed again to X=4 Y=2")
    else:
        rec("TC-12", "FAIL", f"PATCH {code} {body} GET {got}")

    # ---------- Errors ----------
    code, body = patch_cfg(admin, -3, 2)
    _, after = get_cfg(admin)
    if code >= 400 and isinstance(after, dict) and after.get("minutes_per_km") == 4:
        rec("TC-13", "PASS", f"negative X rejected ({code}); X stays 4")
    else:
        rec("TC-13", "FAIL", f"neg X PATCH {code} {body}; after={after}")

    code, body = patch_cfg(admin, 4, -5)
    _, after = get_cfg(admin)
    if code >= 400 and isinstance(after, dict) and after.get("extra_minutes") == 2:
        rec("TC-14", "PASS", f"negative Y rejected ({code}); Y stays 2")
    else:
        rec("TC-14", "FAIL", f"neg Y PATCH {code} {body}; after={after}")

    code_x, body_x = http(
        "PATCH",
        "/api/v1/admin/config/drop-deadline",
        token=admin,
        raw_body=b'{"minutes_per_km":"abc","extra_minutes":2}',
    )[:2]
    code_y, body_y = http(
        "PATCH",
        "/api/v1/admin/config/drop-deadline",
        token=admin,
        raw_body=b'{"minutes_per_km":4,"extra_minutes":"xyz"}',
    )[:2]
    _, after = get_cfg(admin)
    if (
        code_x >= 400
        and code_y >= 400
        and isinstance(after, dict)
        and after.get("minutes_per_km") == 4
        and after.get("extra_minutes") == 2
    ):
        rec("TC-15", "PASS", f"non-numeric X/Y rejected ({code_x}/{code_y}); stays 4/2")
    else:
        rec("TC-15", "FAIL", f"non-num X {code_x} {body_x} Y {code_y} {body_y}; after={after}")

    # ---------- Edges ----------
    # TC-16 never set
    cleanup_active()
    try:
        delete_cfg()
        _, g = get_cfg(admin)
        tid, oid, _ = seed_assign(admin, rider, 6)
        code, body, trip = do_pickup(admin, rider)
        dd = deadline_of(trip, tid)
        pep = pickup_epoch_from_trip(trip)
        allowed = allowed_from_deadline(dd, pep)
        if close_enough(allowed, 12.0):
            rec("TC-16", "PASS", f"deleted config; GET defaults {g}; 6km allowed {allowed:.2f} (6*2+0=12)")
        else:
            rec("TC-16", "FAIL", f"never-set 6km allowed={allowed} expected 12; GET={g}")
        do_drop(admin, tid, oid)
    except Exception as e:
        rec("TC-16", "BLOCKED", f"could not unset/run never-set case: {e}")

    # TC-17 only Y missing
    cleanup_active()
    try:
        delete_cfg()
        put_cfg_partial(x=5)
        _, g = get_cfg(admin)
        tid, oid, _ = seed_assign(admin, rider, 2)
        code, body, trip = do_pickup(admin, rider)
        allowed = allowed_from_deadline(deadline_of(trip, tid), pickup_epoch_from_trip(trip))
        if close_enough(allowed, 10.0) and isinstance(g, dict) and g.get("minutes_per_km") == 5 and g.get("extra_minutes") == 0:
            rec("TC-17", "PASS", f"only X=5 stored; Y defaults 0; 2km allowed {allowed:.2f}")
        else:
            rec("TC-17", "FAIL", f"allowed={allowed} expected 10; GET={g}")
        do_drop(admin, tid, oid)
    except Exception as e:
        rec("TC-17", "BLOCKED", f"could not set partial-X config: {e}")

    # TC-18 only X missing
    cleanup_active()
    try:
        delete_cfg()
        put_cfg_partial(y=8)
        _, g = get_cfg(admin)
        tid, oid, _ = seed_assign(admin, rider, 3)
        code, body, trip = do_pickup(admin, rider)
        allowed = allowed_from_deadline(deadline_of(trip, tid), pickup_epoch_from_trip(trip))
        if close_enough(allowed, 14.0) and isinstance(g, dict) and g.get("minutes_per_km") == 2 and g.get("extra_minutes") == 8:
            rec("TC-18", "PASS", f"only Y=8 stored; X defaults 2; 3km allowed {allowed:.2f}")
        else:
            rec("TC-18", "FAIL", f"allowed={allowed} expected 14; GET={g}")
        do_drop(admin, tid, oid)
    except Exception as e:
        rec("TC-18", "BLOCKED", f"could not set partial-Y config: {e}")

    # TC-19 / TC-20 / TC-21 shared running trip
    cleanup_active()
    patch_cfg(admin, 3, 10)
    try:
        tidA, oidA, _ = seed_assign(admin, rider, 4)
        code, body, tripA = do_pickup(admin, rider)
        ddA = deadline_of(tripA, tidA)
        allowedA = allowed_from_deadline(ddA, pickup_epoch_from_trip(tripA))
        if not close_enough(allowedA, 22.0) or ddA is None:
            rec("TC-19", "FAIL", f"setup clock not 22 (got {allowedA})")
            rec("TC-20", "FAIL", "shared trip setup failed")
            rec("TC-21", "FAIL", "shared trip setup failed")
        else:
            patch_cfg(admin, 10, 10)  # change X only; PATCH requires both
            tripA2 = get_trip(rider)
            ddA2 = deadline_of(tripA2, tidA)
            if ddA2 == ddA:
                rec("TC-19", "PASS", f"after X=10, this trip deadline stays {int(ddA)} (orig 22 min)")
            else:
                rec("TC-19", "FAIL", f"deadline moved {ddA} -> {ddA2} after X change")

            patch_cfg(admin, 10, 100)
            tripA3 = get_trip(rider)
            ddA3 = deadline_of(tripA3, tidA)
            if ddA3 == ddA:
                rec("TC-20", "PASS", f"after Y=100, this trip deadline stays {int(ddA)}")
            else:
                rec("TC-20", "FAIL", f"deadline moved {ddA} -> {ddA3} after Y change")

            # next trip uses 10/100; keep A live
            set_de_eligible(clear_current=False)
            tidB, oidB, _, _ = seed_trip(1)
            code, body = assign(admin, oidB)
            if code != 200:
                set_de_eligible(clear_current=True)
                code, body = assign(admin, oidB)
            if code != 200:
                rec("TC-21", "FAIL", f"could not assign next trip: {code} {body}")
            else:
                code, body, tripB = do_pickup(admin, rider)
                ddB = deadline_of(tripB, tidB)
                allowedB = allowed_from_deadline(ddB, pickup_epoch_from_trip(tripB))
                ddA_still = ddb_n(ddb_get_trip(tidA), "drop_deadline")
                if close_enough(allowedB, 110.0) and ddA_still == ddA:
                    rec("TC-21", "PASS", f"new 1km allowed {allowedB:.2f} (1*10+100=110); older deadline unchanged")
                else:
                    rec("TC-21", "FAIL", f"new allowed={allowedB} expected 110; old dd {ddA_still} vs {ddA}")
                if tidB:
                    do_drop(admin, tidB, oidB)
            do_drop(admin, tidA, oidA)
    except Exception as e:
        for tc in ("TC-19", "TC-20", "TC-21"):
            if not any(r[0] == tc for r in results):
                rec(tc, "FAIL", f"shared-trip flow failed: {e}")

    # TC-22 settings change BEFORE pickup
    cleanup_active()
    patch_cfg(admin, 3, 10)
    try:
        tid, oid, trip0 = seed_assign(admin, rider, 4)
        pre = has_deadline(trip0)
        patch_cfg(admin, 5, 1)
        if has_deadline(get_trip(rider)):
            rec("TC-22", "FAIL", "deadline appeared before pickup after settings change")
        else:
            code, body, trip = do_pickup(admin, rider)
            allowed = allowed_from_deadline(deadline_of(trip, tid), pickup_epoch_from_trip(trip))
            if (not pre) and close_enough(allowed, 21.0):
                rec("TC-22", "PASS", f"no deadline before pickup; after pickup allowed {allowed:.2f} (4*5+1=21)")
            else:
                rec("TC-22", "FAIL", f"pre_deadline={pre} allowed={allowed} expected 21")
        do_drop(admin, tid, oid)
    except Exception as e:
        rec("TC-22", "FAIL", f"setup failed: {e}")

    # TC-23 / TC-24 late rider
    cleanup_active()
    patch_cfg(admin, 4, 7)
    seed_payout()
    try:
        tid, oid, _ = seed_assign(admin, rider, 4)
        code, body, trip = do_pickup(admin, rider)
        if code != 200:
            rec("TC-23", "FAIL", f"pickup failed {code} {body}")
            rec("TC-24", "FAIL", "no late trip")
        else:
            # force deadline into the past
            past = str(int(time.time()) - 120)
            aws_ddb(
                [
                    "update-item",
                    "--table-name",
                    TABLE,
                    "--key",
                    json.dumps({"PK": {"S": f"TRIP!{tid}"}, "SK": {"S": "METADATA"}}),
                    "--update-expression",
                    "SET drop_deadline = :d",
                    "--expression-attribute-values",
                    json.dumps({":d": {"N": past}}),
                ]
            )
            code, body = do_drop(admin, tid, oid)
            item = ddb_get_trip(tid)
            st = ddb_s(item, "status")
            if code == 200 and st == "completed":
                rec("TC-23", "PASS", "late drop (deadline in the past) still completed")
            else:
                rec("TC-23", "FAIL", f"late drop HTTP {code} {body} status={st}")
            pay = pay_for_trip(rider, tid)
            if pay is None:
                rec("TC-24", "FAIL", "late-trip pay unverifiable (no total_pay_zmw / earnings line)")
            elif tc06_pay is None:
                # still check a positive normal-looking pay
                if pay > 0:
                    rec("TC-24", "PASS", f"late pay recorded ({pay}); TC-06 pay missing so compared as non-zero same-formula payout")
                else:
                    rec("TC-24", "FAIL", f"late pay is {pay}")
            elif abs(pay - tc06_pay) < 0.011:
                rec("TC-24", "PASS", f"late pay {pay} equals on-time pay {tc06_pay} for 4km")
            else:
                rec("TC-24", "FAIL", f"late pay {pay} != on-time pay {tc06_pay}")
    except Exception as e:
        if not any(r[0] == "TC-23" for r in results):
            rec("TC-23", "FAIL", f"{e}")
        if not any(r[0] == "TC-24" for r in results):
            rec("TC-24", "FAIL", f"{e}")

    # TC-25 0km -> Y=7
    cleanup_active()
    patch_cfg(admin, 4, 7)
    try:
        tid, oid, _ = seed_assign(admin, rider, 0)
        code, body, trip = do_pickup(admin, rider)
        allowed = allowed_from_deadline(deadline_of(trip, tid), pickup_epoch_from_trip(trip))
        if close_enough(allowed, 7.0):
            rec("TC-25", "PASS", f"0km allowed {allowed:.2f} (=Y=7)")
        else:
            rec("TC-25", "FAIL", f"0km allowed={allowed} expected 7")
        do_drop(admin, tid, oid)
    except Exception as e:
        rec("TC-25", "FAIL", f"{e}")

    # TC-26 X=0 rejected, X stays 4
    patch_cfg(admin, 4, 7)
    code, body = patch_cfg(admin, 0, 7)
    _, after = get_cfg(admin)
    x_stays = isinstance(after, dict) and after.get("minutes_per_km") == 4
    if code >= 400 and x_stays:
        rec("TC-26", "PASS", f"X=0 rejected ({code}); X stays 4; no config with X=0")
    else:
        rec("TC-26", "FAIL", f"X=0 PATCH {code} {body}; after={after}")

    # TC-27 Y=0 valid, 5km * 4 + 0 = 20
    cleanup_active()
    code, body = patch_cfg(admin, 4, 0)
    _, g = get_cfg(admin)
    try:
        tid, oid, _ = seed_assign(admin, rider, 5)
        _, _, trip = do_pickup(admin, rider)
        allowed = allowed_from_deadline(deadline_of(trip, tid), pickup_epoch_from_trip(trip))
        if code == 200 and isinstance(g, dict) and g.get("extra_minutes") == 0 and close_enough(allowed, 20.0):
            rec("TC-27", "PASS", f"Y=0 accepted; 5km allowed {allowed:.2f} (5*4+0=20)")
        else:
            rec("TC-27", "FAIL", f"PATCH {code} GET {g} allowed={allowed} expected 20")
        do_drop(admin, tid, oid)
    except Exception as e:
        rec("TC-27", "FAIL", f"{e}")

    # TC-28 1.5km * 2 + 0 = 3
    cleanup_active()
    patch_cfg(admin, 2, 0)
    try:
        tid, oid, _ = seed_assign(admin, rider, 1.5)
        _, _, trip = do_pickup(admin, rider)
        allowed = allowed_from_deadline(deadline_of(trip, tid), pickup_epoch_from_trip(trip))
        if close_enough(allowed, 3.0):
            rec("TC-28", "PASS", f"1.5km allowed {allowed:.2f} (1.5*2+0=3)")
        else:
            rec("TC-28", "FAIL", f"1.5km allowed={allowed} expected 3")
        do_drop(admin, tid, oid)
    except Exception as e:
        rec("TC-28", "FAIL", f"{e}")

    # TC-29 two live trips freeze separately
    cleanup_active()
    patch_cfg(admin, 3, 10)
    try:
        tidA, oidA, _ = seed_assign(admin, rider, 4)
        _, _, tripA = do_pickup(admin, rider)
        ddA = deadline_of(tripA, tidA)
        allowedA = allowed_from_deadline(ddA, pickup_epoch_from_trip(tripA))
        patch_cfg(admin, 8, 0)
        set_de_eligible(clear_current=False)
        tidB, oidB, _, _ = seed_trip(2)
        code, body = assign(admin, oidB)
        if code != 200:
            set_de_eligible(clear_current=True)
            code, body = assign(admin, oidB)
        if code != 200:
            rec("TC-29", "FAIL", f"could not assign trip B: {code} {body}")
        else:
            _, _, tripB = do_pickup(admin, rider)
            ddB = deadline_of(tripB, tidB)
            allowedB = allowed_from_deadline(ddB, pickup_epoch_from_trip(tripB))
            ddA_still = ddb_n(ddb_get_trip(tidA), "drop_deadline")
            a_ok = ddA is not None and ddA_still == ddA and close_enough(allowedA, 22.0)
            b_ok = close_enough(allowedB, 16.0)
            if a_ok and b_ok:
                rec("TC-29", "PASS", f"A frozen at {int(ddA)} (22 min); B allowed {allowedB:.2f} (2*8+0=16)")
            else:
                rec("TC-29", "FAIL", f"A allowed={allowedA} dd {ddA}->{ddA_still}; B allowed={allowedB} expected 16")
            if tidB:
                do_drop(admin, tidB, oidB)
        do_drop(admin, tidA, oidA)
    except Exception as e:
        rec("TC-29", "FAIL", f"{e}")

    # restore defaults
    try:
        patch_cfg(admin, 2, 0)
        cleanup_active()
    except Exception:
        pass

    # print single list
    seen = {t for t, _, _ in results}
    for i in range(1, 30):
        tc = f"TC-{i:02d}"
        if tc not in seen:
            rec(tc, "FAIL", "case did not run")

    results.sort(key=lambda r: r[0])
    # keep last write per TC
    final: dict[str, tuple[str, str]] = {}
    for tc, st, reason in results:
        final[tc] = (st, reason)

    bad = 0
    for i in range(1, 30):
        tc = f"TC-{i:02d}"
        st, reason = final.get(tc, ("FAIL", "missing"))
        print(f"{tc} {st} — {reason}")
        if st != "PASS":
            bad += 1
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
