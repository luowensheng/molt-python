import pytest
from httpx import AsyncClient, ASGITransport

from taskapi.app import app, _tasks


@pytest.fixture(autouse=True)
def clear_tasks():
    _tasks.clear()
    yield
    _tasks.clear()


@pytest.mark.anyio
async def test_root():
    async with AsyncClient(transport=ASGITransport(app=app), base_url="http://test") as client:
        resp = await client.get("/")
    assert resp.status_code == 200
    assert resp.json()["service"] == "task-api"


@pytest.mark.anyio
async def test_create_and_list():
    async with AsyncClient(transport=ASGITransport(app=app), base_url="http://test") as client:
        r = await client.post("/tasks", json={"title": "Buy milk"})
        assert r.status_code == 201
        task = r.json()
        assert task["title"] == "Buy milk"
        assert task["done"] is False

        r2 = await client.get("/tasks")
        assert len(r2.json()) == 1


@pytest.mark.anyio
async def test_update_task():
    async with AsyncClient(transport=ASGITransport(app=app), base_url="http://test") as client:
        r = await client.post("/tasks", json={"title": "Read docs"})
        task_id = r.json()["id"]

        r2 = await client.patch(f"/tasks/{task_id}", json={"title": "Read docs", "done": True})
        assert r2.json()["done"] is True


@pytest.mark.anyio
async def test_not_found():
    async with AsyncClient(transport=ASGITransport(app=app), base_url="http://test") as client:
        r = await client.get("/tasks/999")
    assert r.status_code == 404
