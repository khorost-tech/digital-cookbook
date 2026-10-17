# СТРАТЕГИЯ: как получается само, если ни о чём не думать.
#
# Полный образ python, установка поверх системного интерпретатора, колёса
# остаются лежать в образе. Каждая из трёх черт добавляет вес по своей
# причине, и разделить их можно только соседними стратегиями — поэтому
# они здесь и стоят вместе.
FROM python:3.14

WORKDIR /srv
COPY index/packages /wheels
COPY images/app/requirements.txt /srv/requirements.txt

# --no-index: сеть за скобками, ставим из локальных колёс. То же самое во
# всех стратегиях, иначе сравнивались бы ещё и условия загрузки.
RUN pip install --no-cache-dir --no-index --find-links /wheels \
        -r /srv/requirements.txt

COPY images/app/service.py /srv/service.py

EXPOSE 8000
CMD ["python", "-m", "uvicorn", "--host", "0.0.0.0", "--port", "8000", "service:app"]
