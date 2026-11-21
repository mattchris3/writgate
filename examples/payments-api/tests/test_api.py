from datetime import UTC, datetime, timedelta

import pytest
from fastapi.testclient import TestClient

from payments_api import create_app

API_KEY = "sk_test_123"


class FakeClock:
    def __init__(self) -> None:
        self.now = datetime(2026, 11, 21, 12, 0, tzinfo=UTC)

    def __call__(self) -> datetime:
        return self.now

    def advance(self, **kwargs: float) -> None:
        self.now += timedelta(**kwargs)


@pytest.fixture
def clock() -> FakeClock:
    return FakeClock()


@pytest.fixture
def client(clock: FakeClock) -> TestClient:
    c = TestClient(create_app(api_key=API_KEY, clock=clock))
    c.headers["Authorization"] = f"Bearer {API_KEY}"
    return c


_keys = iter(range(1_000_000))


def key() -> dict[str, str]:
    return {"Idempotency-Key": f"key-{next(_keys)}"}


def charge(client: TestClient, amount: int = 1250, customer: str = "cus_42", **extra) -> dict:
    r = client.post(
        "/charges", json={"amount": amount, "customer_id": customer, **extra}, headers=key()
    )
    assert r.status_code == 201, r.text
    return r.json()


# ---- auth ---------------------------------------------------------------


@pytest.mark.parametrize("auth", [None, "Bearer wrong", "Basic abc"])
def test_requires_api_key(client: TestClient, auth: str | None) -> None:
    headers = {"Authorization": auth} if auth else {}
    client.headers.pop("Authorization")
    r = client.get("/charges", headers=headers)
    assert r.status_code == 401
    assert r.headers["WWW-Authenticate"] == "Bearer"


def test_empty_api_key_refused() -> None:
    with pytest.raises(ValueError):
        create_app(api_key="")


# ---- charges ------------------------------------------------------------


def test_create_and_get_charge(client: TestClient, clock: FakeClock) -> None:
    c = charge(client, description="Coffee beans")
    assert c["id"].startswith("ch_")
    assert c["status"] == "succeeded"
    assert c["currency"] == "GBP"
    assert c["amount_refunded"] == 0
    assert c["created_at"] == "2026-11-21T12:00:00Z"

    r = client.get(f"/charges/{c['id']}")
    assert r.status_code == 200
    assert r.json() == c


def test_get_unknown_charge(client: TestClient) -> None:
    r = client.get("/charges/ch_nope")
    assert r.status_code == 404
    assert r.json()["detail"] == "No charge with id ch_nope."


@pytest.mark.parametrize(
    "body",
    [
        {"amount": 0, "customer_id": "cus_1"},
        {"amount": 12.5, "customer_id": "cus_1"},
        {"amount": 100, "customer_id": "bob"},
        {"amount": 100, "customer_id": "cus_1", "currency": "gbp"},
        {"amount": 100, "customer_id": "cus_1", "surprise": True},
    ],
)
def test_invalid_charge(client: TestClient, body: dict) -> None:
    assert client.post("/charges", json=body, headers=key()).status_code == 422


def test_idempotency_key_required(client: TestClient) -> None:
    r = client.post("/charges", json={"amount": 100, "customer_id": "cus_1"})
    assert r.status_code == 422
    assert r.json()["detail"][0]["loc"] == ["header", "Idempotency-Key"]


@pytest.mark.parametrize(
    ("customer", "reason"),
    [("cus_test_declined", "card_declined"), ("cus_test_insufficient_funds", "insufficient_funds")],
)
def test_test_customers_fail(client: TestClient, customer: str, reason: str) -> None:
    r = client.post("/charges", json={"amount": 100, "customer_id": customer}, headers=key())
    assert r.status_code == 402
    assert reason in r.json()["detail"]
    failed = client.get("/charges", params={"status": "failed"}).json()["data"]
    assert [c["failure_reason"] for c in failed] == [reason]


def test_list_charges_newest_first_with_pagination(client: TestClient, clock: FakeClock) -> None:
    ids = []
    for _ in range(5):
        ids.append(charge(client)["id"])
        clock.advance(seconds=1)
    newest_first = ids[::-1]

    page1 = client.get("/charges", params={"limit": 2}).json()
    assert [c["id"] for c in page1["data"]] == newest_first[:2]
    assert page1["has_more"] is True

    page2 = client.get("/charges", params={"limit": 2, "starting_after": newest_first[1]}).json()
    assert [c["id"] for c in page2["data"]] == newest_first[2:4]

    last = client.get("/charges", params={"limit": 2, "starting_after": newest_first[3]}).json()
    assert [c["id"] for c in last["data"]] == newest_first[4:]
    assert last["has_more"] is False


def test_list_charges_filters(client: TestClient) -> None:
    a = charge(client, customer="cus_a")
    charge(client, customer="cus_b")
    r = client.get("/charges", params={"customer_id": "cus_a"}).json()
    assert [c["id"] for c in r["data"]] == [a["id"]]


def test_bad_cursor(client: TestClient) -> None:
    r = client.get("/charges", params={"starting_after": "ch_unknown"})
    assert r.status_code == 400


@pytest.mark.parametrize("limit", [0, 101])
def test_limit_bounds(client: TestClient, limit: int) -> None:
    assert client.get("/charges", params={"limit": limit}).status_code == 422


# ---- idempotency --------------------------------------------------------


def test_same_key_same_body_replays(client: TestClient) -> None:
    body = {"amount": 500, "customer_id": "cus_1"}
    first = client.post("/charges", json=body, headers={"Idempotency-Key": "abc"})
    again = client.post("/charges", json=body, headers={"Idempotency-Key": "abc"})
    assert again.status_code == first.status_code == 201
    assert again.json() == first.json()
    assert again.headers["Idempotent-Replayed"] == "true"
    assert "Idempotent-Replayed" not in first.headers
    assert len(client.get("/charges").json()["data"]) == 1


def test_same_key_different_body_conflicts(client: TestClient) -> None:
    client.post(
        "/charges", json={"amount": 500, "customer_id": "cus_1"}, headers={"Idempotency-Key": "k"}
    )
    r = client.post(
        "/charges", json={"amount": 501, "customer_id": "cus_1"}, headers={"Idempotency-Key": "k"}
    )
    assert r.status_code == 409
    assert "different request" in r.json()["detail"]


def test_same_key_on_another_endpoint_conflicts(client: TestClient) -> None:
    c = charge(client)
    client.post(
        "/charges", json={"amount": 1, "customer_id": "cus_1"}, headers={"Idempotency-Key": "x"}
    )
    r = client.post(f"/charges/{c['id']}/refunds", json={}, headers={"Idempotency-Key": "x"})
    assert r.status_code == 409


def test_declined_charge_replays_the_decline(client: TestClient) -> None:
    body = {"amount": 100, "customer_id": "cus_test_declined"}
    first = client.post("/charges", json=body, headers={"Idempotency-Key": "d"})
    again = client.post("/charges", json=body, headers={"Idempotency-Key": "d"})
    assert first.status_code == again.status_code == 402
    assert again.json() == first.json()
    assert len(client.get("/charges").json()["data"]) == 1


def test_replay_window_expires(client: TestClient, clock: FakeClock) -> None:
    body = {"amount": 100, "customer_id": "cus_1"}
    first = client.post("/charges", json=body, headers={"Idempotency-Key": "old"}).json()
    clock.advance(hours=24, seconds=1)
    second = client.post("/charges", json=body, headers={"Idempotency-Key": "old"}).json()
    assert second["id"] != first["id"]


def test_lookup_by_idempotency_key_outlives_replay_window(
    client: TestClient, clock: FakeClock
) -> None:
    created = client.post(
        "/charges",
        json={"amount": 100, "customer_id": "cus_1"},
        headers={"Idempotency-Key": "find-me"},
    ).json()
    charge(client)
    clock.advance(hours=48)
    found = client.get("/charges", params={"idempotency_key": "find-me"}).json()["data"]
    assert [c["id"] for c in found] == [created["id"]]
    assert client.get("/charges", params={"idempotency_key": "never-used"}).json()["data"] == []


# ---- refunds ------------------------------------------------------------


def refund(client: TestClient, charge_id: str, **body) -> dict:
    r = client.post(f"/charges/{charge_id}/refunds", json=body, headers=key())
    assert r.status_code == 201, r.text
    return r.json()


def test_full_refund_by_default(client: TestClient) -> None:
    c = charge(client, amount=1250)
    r = refund(client, c["id"], reason="duplicate")
    assert r["id"].startswith("re_")
    assert (r["amount"], r["currency"], r["status"], r["reason"]) == (
        1250,
        "GBP",
        "pending",
        "duplicate",
    )
    assert client.get(f"/charges/{c['id']}").json()["amount_refunded"] == 1250


def test_partial_refunds_and_over_refund(client: TestClient) -> None:
    c = charge(client, amount=1000)
    refund(client, c["id"], amount=600)
    r = client.post(f"/charges/{c['id']}/refunds", json={"amount": 500}, headers=key())
    assert r.status_code == 409
    assert (
        r.json()["detail"]
        == f"Refund of 500 is more than the 400 left to refund on charge {c['id']}."
    )
    refund(client, c["id"], amount=400)
    r = client.post(f"/charges/{c['id']}/refunds", json={}, headers=key())
    assert r.status_code == 409
    assert "fully refunded" in r.json()["detail"]


def test_refund_unknown_or_failed_charge(client: TestClient) -> None:
    assert client.post("/charges/ch_nope/refunds", json={}, headers=key()).status_code == 404
    client.post("/charges", json={"amount": 100, "customer_id": "cus_test_declined"}, headers=key())
    failed = client.get("/charges", params={"status": "failed"}).json()["data"][0]
    assert (
        client.post(f"/charges/{failed['id']}/refunds", json={}, headers=key()).status_code == 409
    )


def test_refund_settles_after_30_seconds(client: TestClient, clock: FakeClock) -> None:
    c = charge(client)
    r = refund(client, c["id"])
    clock.advance(seconds=29)
    assert client.get(f"/charges/{c['id']}/refunds").json()["data"][0]["status"] == "pending"
    clock.advance(seconds=1)
    assert client.get(f"/charges/{c['id']}/refunds").json()["data"][0]["status"] == "succeeded"
    assert r["settles_at"] == "2026-11-21T12:00:30Z"


def test_cancel_pending_refund(client: TestClient) -> None:
    c = charge(client, amount=1000)
    r = refund(client, c["id"])
    assert client.delete(f"/refunds/{r['id']}").status_code == 204
    assert client.delete(f"/refunds/{r['id']}").status_code == 204  # safe to retry
    listed = client.get(f"/charges/{c['id']}/refunds").json()["data"]
    assert listed[0]["status"] == "cancelled"
    # A cancelled refund frees the amount again.
    assert client.get(f"/charges/{c['id']}").json()["amount_refunded"] == 0
    refund(client, c["id"], amount=1000)


def test_cannot_cancel_settled_refund(client: TestClient, clock: FakeClock) -> None:
    c = charge(client)
    r = refund(client, c["id"])
    clock.advance(seconds=31)
    resp = client.delete(f"/refunds/{r['id']}")
    assert resp.status_code == 409
    assert client.delete("/refunds/re_nope").status_code == 404


def test_lookup_refund_by_idempotency_key(client: TestClient) -> None:
    c = charge(client)
    r = client.post(
        f"/charges/{c['id']}/refunds", json={"amount": 100}, headers={"Idempotency-Key": "refund-1"}
    ).json()
    refund(client, c["id"], amount=100)
    found = client.get(
        f"/charges/{c['id']}/refunds", params={"idempotency_key": "refund-1"}
    ).json()["data"]
    assert [x["id"] for x in found] == [r["id"]]


def test_list_refunds_unknown_charge(client: TestClient) -> None:
    assert client.get("/charges/ch_nope/refunds").status_code == 404
