# OpenSearch 3.5.0 + repository-s3 (не входит в стандартный образ) — для снапшотов в MinIO/S3.
# Демо-креды S3-клиента запекаются в keystore (DEMO-ONLY) — чтобы стенд поднимался одним `docker compose up`
# без ручных opensearch-keystore шагов. В проде креды кладут в keystore на месте, не в образ.
FROM opensearchproject/opensearch:3.5.0
RUN /usr/share/opensearch/bin/opensearch-plugin install --batch repository-s3 \
 && printf 'minioadmin'   | /usr/share/opensearch/bin/opensearch-keystore add --stdin --force s3.client.default.access_key \
 && printf 'IsmDemo#2026' | /usr/share/opensearch/bin/opensearch-keystore add --stdin --force s3.client.default.secret_key
