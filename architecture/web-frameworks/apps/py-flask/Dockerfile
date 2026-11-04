FROM python:3.14.7-slim@sha256:51dafde81dbdb6ebde285137a295cf18a47ca95234fe388a343719cb97305b3d
WORKDIR /src
COPY requirements.txt .
RUN pip install -q --no-cache-dir -r requirements.txt
COPY . .
# сервер разработки из документации Flask; отладка выключена по умолчанию
ENTRYPOINT ["flask", "--app", "app", "run", "--host", "0.0.0.0", "--port", "8080"]
