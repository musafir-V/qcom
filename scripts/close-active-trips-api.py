#!/usr/bin/env python3
"""Close all active trips via production API (driver flow + admin assign for pool)."""

import json
import os
import subprocess
import sys
import urllib.error
import urllib.request

BASE = os.environ.get("API_BASE", "https://api.bunzodelivery.com/api/v1")
OTP = "112233"
QR_CODE = os.environ.get("STORE_QR", "2212026061411")
DEFAULT_POOL_DRIVERS = [
    "+260991535000",
    "+260942141000",
    "+260987926000",
    "+260988197000",
    "+260978271000",
    "+260933473000",
    "+260966311000",
    "+260944414000",
    "+919515365236",
]
REGION = os.environ.get("AWS_REGION", "ap-southeast-2")
TABLE = os.environ.get("DDB_TABLE", "QComTable")

FILTER_VALUES = json.dumps(
    {
        ":pk": {"S": "TRIP!"},
        ":sk": {"S": "METADATA"},
        ":completed": {"S": "completed"},
        ":cancelled": {"S": "cancelled"},
    }
)
FILTER_NAMES = json.dumps({"#s": "status"})


def aws_scan_trips():
    items = []
    start_key = None
    while True:
        cmd = [
            "aws", "dynamodb", "scan",
            "--region", REGION,
            "--table-name", TABLE,
            "--filter-expression", "begins_with(PK, :pk) AND SK = :sk AND #s <> :completed AND #s <> :cancelled",
            "--expression-attribute-names", FILTER_NAMES,
            "--expression-attribute-values", FILTER_VALUES,
            "--output", "json",
        ]
        if start_key:
            cmd.extend(["--exclusive-start-key", json.dumps(start_key)])
        page = json.loads(subprocess.check_output(cmd, text=True))
        items.extend(page.get("Items", []))
        start_key = page.get("LastEvaluatedKey")
        if not start_key:
            break
    return items


def aws_de_phone_by_id(de_id):
    cmd = [
        "aws", "dynamodb", "scan",
        "--region", REGION,
        "--table-name", TABLE,
        "--filter-expression", "begins_with(PK, :pk) AND SK = :sk AND de_id = :id",
        "--expression-attribute-values", json.dumps(
            {":pk": {"S": "DE!"}, ":sk": {"S": "METADATA"}, ":id": {"S": de_id}}
        ),
        "--output", "json",
    ]
    page = json.loads(subprocess.check_output(cmd, text=True))
    items = page.get("Items", [])
    if not items:
        return ""
    return items[0]["PK"]["S"].replace("DE!", "")


def parse_trip(item):
    def s(k):
        return item.get(k, {}).get("S", "")

    pickup = drop = None
    for t in item.get("tasks", {}).get("L", []):
        m = t["M"]
        task = {
            "id": m.get("task_id", {}).get("S", ""),
            "type": m.get("type", {}).get("S", ""),
            "status": m.get("status", {}).get("S", ""),
            "otp": m.get("otp", {}).get("S", ""),
        }
        if task["type"] == "pickup":
            pickup = task
        elif task["type"] == "drop":
            drop = task
    return {
        "trip_id": s("trip_id"),
        "order_id": s("order_id"),
        "status": s("status"),
        "de_phone": s("de_phone"),
        "de_id": s("de_id"),
        "pickup": pickup,
        "drop": drop,
    }


def api(method, path, token=None, body=None, extra_headers=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if extra_headers:
        headers.update(extra_headers)
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            raw = resp.read().decode()
            return resp.status, json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            payload = json.loads(raw)
        except json.JSONDecodeError:
            payload = {"raw": raw}
        return e.code, payload


def auth_de(phone):
    api("POST", "/auth/initiate-otp", body={"phone_number": phone})
    status, body = api(
        "POST",
        "/auth/verify-otp",
        body={"phone_number": phone, "otp": OTP},
        extra_headers={"X-App-Type": "de"},
    )
    if status != 200:
        raise RuntimeError(f"auth failed {status}: {body}")
    return body["access_token"]


def duty_start(token):
    return api("POST", "/de/duty/start", token=token, body={"qr_code": QR_CODE})


def admin_assign(order_id, driver_phone):
    return api("POST", "/admin/assign", body={"order_id": order_id, "driver_phone": driver_phone})


def accept(token, trip_id):
    return api("POST", f"/trip/{trip_id}/accept", token=token)


def task_update(token, trip_id, task_id, status, otp=""):
    body = {"status": status}
    if otp:
        body["otp"] = otp
    return api("POST", f"/trip/{trip_id}/task/{task_id}/status/update", token=token, body=body)


def de_status(phone):
    token = auth_de(phone)
    _, body = api("GET", "/de/me", token=token)
    return body.get("status", ""), token, body


def ensure_eligible(driver_phone):
    status, token, _ = de_status(driver_phone)
    if status == "eligible":
        return token
    if status in ("free", "offline"):
        st, b = duty_start(token)
        if st not in (200, 409):
            raise RuntimeError(f"duty/start failed {st}: {b}")
        return auth_de(driver_phone)
    if status == "busy":
        raise RuntimeError(f"driver {driver_phone} is busy")
    raise RuntimeError(f"unexpected DE status {status}")


def fetch_current_trip(token):
    _, body = api("GET", "/de/trip", token=token)
    trip = body.get("trip")
    if not trip:
        return None
    pickup = drop = None
    for task in trip.get("tasks", []):
        t = {
            "id": task.get("task_id", ""),
            "type": task.get("type", ""),
            "status": task.get("status", ""),
            "otp": task.get("otp", ""),
        }
        if t["type"] == "pickup":
            pickup = t
        elif t["type"] == "drop":
            drop = t
    return {
        "trip_id": trip.get("trip_id", ""),
        "order_id": trip.get("order_id", ""),
        "status": trip.get("status", ""),
        "pickup": pickup,
        "drop": drop,
    }


def complete_via_token(token, trip):
    trip_id = trip["trip_id"]
    status = trip["status"]
    if status == "assigned":
        st, b = accept(token, trip_id)
        if st != 200:
            return f"accept {st}: {b}"
    pickup, drop = trip["pickup"], trip["drop"]
    if pickup and pickup["status"] != "completed":
        st, b = task_update(token, trip_id, pickup["id"], "completed")
        if st != 200:
            return f"pickup {st}: {b}"
        # Refresh after pickup so trip status mirrors out_for_delivery.
        refreshed = fetch_current_trip(token)
        if refreshed:
            trip = refreshed
            drop = trip["drop"]
            status = trip["status"]
    if drop and drop["status"] != "completed":
        if status not in ("out_for_delivery",):
            return f"cannot drop: trip status={status} (pickup done but not out_for_delivery — API blocked)"
        st, b = task_update(token, trip_id, drop["id"], "completed", drop["otp"])
        if st != 200:
            return f"drop {st}: {b}"
    return None


def pick_pool_driver(drivers):
    for phone in drivers:
        status, token, _ = de_status(phone)
        if status == "eligible":
            return phone, token
        if status in ("free", "offline"):
            try:
                return phone, ensure_eligible(phone)
            except RuntimeError:
                continue
    raise RuntimeError("no eligible pool driver available")


def close_trip(trip, pool_drivers):
    trip_id = trip["trip_id"]
    order_id = trip["order_id"]
    status = trip["status"]
    phone = trip["de_phone"]

    if not phone and trip["de_id"]:
        phone = aws_de_phone_by_id(trip["de_id"])

    if status == "created" and not phone:
        phone, token = pick_pool_driver(pool_drivers)
        st, b = admin_assign(order_id, phone)
        if st != 200:
            return "fail", f"admin_assign {st}: {b}"
        live = fetch_current_trip(token) or trip
    else:
        if not phone:
            return "fail", "no driver on trip"
        token = auth_de(phone)
        live = {
            "trip_id": trip_id,
            "order_id": order_id,
            "status": status,
            "pickup": trip["pickup"],
            "drop": trip["drop"],
        }

    err = complete_via_token(token, live)
    if err:
        return "fail", err
    return "ok", "completed"


def main():
    trips = [parse_trip(i) for i in aws_scan_trips()]
    # Process assigned/legacy trips first, then pool
    order = {"in_transit": 0, "out_for_delivery": 1, "accepted": 2, "assigned": 3, "created": 4}
    trips.sort(key=lambda t: (order.get(t["status"], 9), t["trip_id"]))
    pool_drivers = [p.strip() for p in os.environ.get("POOL_DRIVERS", "").split(",") if p.strip()] or DEFAULT_POOL_DRIVERS
    print(f"Found {len(trips)} active trips ({len(pool_drivers)} pool drivers)")
    ok = fail = 0
    for i, trip in enumerate(trips, 1):
        label = (
            f"[{i}/{len(trips)}] {trip['status']:15} "
            f"order={trip['order_id'][:12]} trip={trip['trip_id'][:8]}..."
        )
        try:
            result, detail = close_trip(trip, pool_drivers)
        except Exception as e:
            result, detail = "fail", str(e)
        print(f"{label} -> {result}: {detail}")
        if result == "ok":
            ok += 1
        else:
            fail += 1
    print(f"\nDone: {ok} closed, {fail} failed")
    return 1 if fail else 0


if __name__ == "__main__":
    sys.exit(main())
