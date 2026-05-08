"""
Simple FastAPI demo: a task list REST API.
Shows how molt manages a real web framework project with dev/test/lint tasks.
"""
from __future__ import annotations

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

app = FastAPI(title="Task API", version="0.1.0")

_tasks: dict[int, dict] = {}
_next_id = 1


class TaskIn(BaseModel):
    title: str
    done: bool = False


class Task(BaseModel):
    id: int
    title: str
    done: bool


@app.get("/")
def root():
    return {"service": "task-api", "version": "0.1.0"}


@app.get("/tasks", response_model=list[Task])
def list_tasks():
    return list(_tasks.values())


@app.post("/tasks", response_model=Task, status_code=201)
def create_task(body: TaskIn):
    global _next_id
    task = {"id": _next_id, "title": body.title, "done": body.done}
    _tasks[_next_id] = task
    _next_id += 1
    return task


@app.get("/tasks/{task_id}", response_model=Task)
def get_task(task_id: int):
    if task_id not in _tasks:
        raise HTTPException(status_code=404, detail="Task not found")
    return _tasks[task_id]


@app.patch("/tasks/{task_id}", response_model=Task)
def update_task(task_id: int, body: TaskIn):
    if task_id not in _tasks:
        raise HTTPException(status_code=404, detail="Task not found")
    _tasks[task_id].update({"title": body.title, "done": body.done})
    return _tasks[task_id]


@app.delete("/tasks/{task_id}", status_code=204)
def delete_task(task_id: int):
    if task_id not in _tasks:
        raise HTTPException(status_code=404, detail="Task not found")
    del _tasks[task_id]
