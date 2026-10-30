"""Общее для заданий tools: каталог Iceberg (PyIceberg), S3 через pyarrow и boto3.

«Папка» — паркет-файлы в s3://warehouse/folder/<таблица>/, без метаданных
таблицы: что лежит в каталоге, то и таблица.
"""

import json
import os

import boto3
import pyarrow as pa
import pyarrow.dataset as ds
import pyarrow.fs as pafs
import pyarrow.parquet as pq
from pyiceberg.catalog import load_catalog

BUCKET = "warehouse"
SCHEMA = pa.schema([("id", pa.int64()), ("customer_id", pa.int32()), ("status", pa.string()),
                    ("amount", pa.int64()), ("created_at", pa.timestamp("us"))])


def catalog():
    return load_catalog("rest", **{"uri": os.environ["STAND_REST"], "s3.endpoint": os.environ["STAND_S3"],
                                   "s3.path-style-access": "true",
                                   "s3.access-key-id": os.environ["AWS_ACCESS_KEY_ID"],
                                   "s3.secret-access-key": os.environ["AWS_SECRET_ACCESS_KEY"],
                                   "s3.region": os.environ["AWS_REGION"]})


def s3fs():
    return pafs.S3FileSystem(endpoint_override=os.environ["STAND_S3"].replace("http://", ""), scheme="http",
                             access_key=os.environ["AWS_ACCESS_KEY_ID"], secret_key=os.environ["AWS_SECRET_ACCESS_KEY"],
                             region=os.environ["AWS_REGION"])


def s3():
    return boto3.client("s3", endpoint_url=os.environ["STAND_S3"])


def objects(prefix):
    """Число объектов в бакете под префиксом."""
    n, token = 0, None
    while True:
        kw = {"Bucket": BUCKET, "Prefix": prefix}
        if token:
            kw["ContinuationToken"] = token
        page = s3().list_objects_v2(**kw)
        n += page.get("KeyCount", 0)
        if not page.get("IsTruncated"):
            return n
        token = page["NextContinuationToken"]


def clear(prefix):
    keys, token = [], None
    while True:
        kw = {"Bucket": BUCKET, "Prefix": prefix}
        if token:
            kw["ContinuationToken"] = token
        page = s3().list_objects_v2(**kw)
        keys += [o["Key"] for o in page.get("Contents", [])]
        if not page.get("IsTruncated"):
            break
        token = page["NextContinuationToken"]
    for k in range(0, len(keys), 500):
        s3().delete_objects(Bucket=BUCKET, Delete={"Objects": [{"Key": x} for x in keys[k:k + 500]]})


def table(rows, schema=SCHEMA):
    return pa.Table.from_pylist(rows, schema=schema)


def write_file(path, rows, schema=SCHEMA):
    pq.write_table(table(rows, schema), "%s/%s" % (BUCKET, path), filesystem=s3fs())


def read_folder(prefix, schema=None, partitioning=None, filter=None):
    """Папка как таблица: все parquet-файлы под префиксом; схема — по имени колонок."""
    d = ds.dataset("%s/%s" % (BUCKET, prefix), format="parquet", filesystem=s3fs(), schema=schema,
                   partitioning=partitioning)
    return d.to_table(filter=filter), d


def emit(obj):
    print("RESULT " + json.dumps(obj, ensure_ascii=False, default=str), flush=True)


def physical(prefix, order_id):
    """Сколько копий строки id = order_id физически лежит в паркет-объектах под
    префиксом — в том числе в файлах, на которые таблица уже не ссылается.
    Файлы удалений (без колонки id) пропускаются."""
    fs, n, token = s3fs(), 0, None
    while True:
        kw = {"Bucket": BUCKET, "Prefix": prefix}
        if token:
            kw["ContinuationToken"] = token
        page = s3().list_objects_v2(**kw)
        for o in page.get("Contents", []):
            if not o["Key"].endswith(".parquet"):
                continue
            t = pq.read_table("%s/%s" % (BUCKET, o["Key"]), filesystem=fs)
            if "id" in t.column_names:
                n += sum(1 for v in t.column("id").to_pylist() if v == order_id)
        if not page.get("IsTruncated"):
            return n
        token = page["NextContinuationToken"]
