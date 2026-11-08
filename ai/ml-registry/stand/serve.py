"""Свой сервинг: грузит модель по алиасу при старте и говорит, что именно загрузил.

    python serve.py <модель> <алиас> <порт>

/invocations — вероятности по строкам матрицы (JSON-список списков);
/loaded — версия и run_id, которые разрешились из алиаса в момент загрузки.
"""

import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

import mlflow
import numpy as np
from mlflow import MlflowClient

name, alias, port = sys.argv[1], sys.argv[2], int(sys.argv[3])
mv = MlflowClient().get_model_version_by_alias(name, alias)
model = mlflow.sklearn.load_model("models:/%s/%s" % (name, mv.version))
LOADED = {"name": name, "alias": alias, "version": mv.version, "run_id": mv.run_id}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def reply(self, body):
        data = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self.reply(LOADED if self.path == "/loaded" else {})

    def do_POST(self):
        rows = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.reply(model.predict_proba(np.array(rows, float))[:, 1].tolist())


HTTPServer(("127.0.0.1", port), Handler).serve_forever()
