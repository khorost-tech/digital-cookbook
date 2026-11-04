"""Полный: FastAPI — модели pydantic, валидация, OpenAPI, внедрение зависимостей."""
from fastapi import FastAPI
from pydantic import BaseModel

app = FastAPI()


class Item(BaseModel):
    name: str
    qty: int = 0


@app.get("/items/{item_id}")
def get_item(item_id: int):
    return {"id": item_id, "name": "item"}


@app.post("/items", status_code=201)
def create(item: Item):
    return item


@app.get("/boom")
def boom():
    raise RuntimeError("boom: секрет в сообщении marker-7f3a9c")
