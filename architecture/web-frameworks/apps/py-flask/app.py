"""Минимальный: Flask — роутинг и контекст запроса; валидации тела нет, JSON — словарь."""
from flask import Flask, request

app = Flask(__name__)


@app.get("/items/<int:item_id>")
def get_item(item_id):
    return {"id": item_id, "name": "item"}


@app.post("/items")
def create():
    item = request.get_json()
    return {"name": item["name"], "qty": item.get("qty", 0)}, 201


@app.get("/boom")
def boom():
    raise RuntimeError("boom: секрет в сообщении marker-7f3a9c")
