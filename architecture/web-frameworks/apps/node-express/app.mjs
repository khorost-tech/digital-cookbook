// Минимальный: Express — роутер и middleware; express.json() разбирает тело, валидации нет.
import express from "express";

const app = express();
app.use(express.json());
app.get("/items/:id", (req, res) => {
  const id = Number.parseInt(req.params.id, 10);
  if (Number.isNaN(id)) return res.status(400).json({ error: "bad id" });
  res.json({ id, name: "item" });
});
app.post("/items", (req, res) => res.status(201).json({ name: req.body.name, qty: req.body.qty ?? 0 }));
app.get("/boom", () => { throw new Error("boom: секрет в сообщении marker-7f3a9c"); });
app.listen(8080);
