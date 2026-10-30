"""SparkSession с каталогом Iceberg REST поверх S3 стенда."""

import os

from pyspark.sql import SparkSession


def session(name="stand"):
    return (SparkSession.builder.appName(name).master("local[2]")
            .config("spark.sql.extensions", "org.apache.iceberg.spark.extensions.IcebergSparkSessionExtensions")
            .config("spark.sql.catalog.lake", "org.apache.iceberg.spark.SparkCatalog")
            .config("spark.sql.catalog.lake.type", "rest")
            .config("spark.sql.catalog.lake.uri", "http://rest:8181")
            .config("spark.sql.catalog.lake.io-impl", "org.apache.iceberg.aws.s3.S3FileIO")
            .config("spark.sql.catalog.lake.s3.endpoint", "http://s3:8333")
            .config("spark.sql.catalog.lake.s3.path-style-access", "true")
            .config("spark.sql.shuffle.partitions", "2")
            .config("spark.ui.enabled", "false")
            .getOrCreate())
