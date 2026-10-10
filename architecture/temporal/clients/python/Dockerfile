# Сборка внутри образа: на хосте нет ни одного из четырёх тулчейнов
# профиля, и ставить их туда ради стенда незачем.
FROM python:3.13-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY worker.py .
ENTRYPOINT ["python", "worker.py"]
