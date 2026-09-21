//! Rust-угол стенда. По решению автора Rust идёт только через PostgreSQL:
//! Apache AGE (граф поверх PG) и honest baseline на рекурсивных CTE. Нативный
//! Neo4j демонстрируется в Go и Java. То, что тот же граф-в-PG работает через
//! обычный PG-драйвер (`tokio-postgres`), усиливает тезис «graph layer поверх PG».

use tokio_postgres::{Client, NoTls};

#[derive(Clone, Copy)]
pub enum Backend {
    Age,
    Baseline,
}

pub struct PgQueries {
    client: Client,
    backend: Backend,
}

impl PgQueries {
    pub async fn connect(conn: &str, backend: Backend) -> Result<Self, tokio_postgres::Error> {
        let (client, connection) = tokio_postgres::connect(conn, NoTls).await?;
        tokio::spawn(async move {
            let _ = connection.await;
        });
        if let Backend::Age = backend {
            client
                .batch_execute("LOAD 'age'; SET search_path = ag_catalog, \"$user\", public;")
                .await?;
        }
        Ok(Self { client, backend })
    }

    pub fn backend_name(&self) -> &'static str {
        match self.backend {
            Backend::Age => "age",
            Backend::Baseline => "baseline",
        }
    }

    // ── AGE: тело cypher оборачивается в SQL, agtype приводится к тексту ──────
    async fn age_ids(&self, body: &str) -> Result<Vec<i64>, tokio_postgres::Error> {
        let sql = format!(
            "SELECT (v)::text FROM cypher('platform', $$ {} $$) AS t(v agtype)",
            body
        );
        let rows = self.client.query(&sql, &[]).await?;
        Ok(sorted_unique(
            rows.iter()
                .map(|r| {
                    r.get::<_, String>(0)
                        .trim()
                        .parse::<i64>()
                        .unwrap_or_default()
                })
                .collect(),
        ))
    }

    async fn sql_ids(
        &self,
        sql: &str,
        params: &[&(dyn tokio_postgres::types::ToSql + Sync)],
    ) -> Result<Vec<i64>, tokio_postgres::Error> {
        let rows = self.client.query(sql, params).await?;
        Ok(sorted_unique(
            rows.iter().map(|r| r.get::<_, i64>(0)).collect(),
        ))
    }

    pub async fn accessible_resources(
        &self,
        user_id: i64,
    ) -> Result<Vec<i64>, tokio_postgres::Error> {
        match self.backend {
            Backend::Age => {
                let via_team = format!(
                    "MATCH (u:User)-[:MEMBER_OF]->(:Team)-[:HAS_ROLE]->(:Role)-[:GRANTS]->(res:Resource) WHERE u.id = {} RETURN res.id",
                    user_id
                );
                let direct = format!(
                    "MATCH (u:User)-[:HAS_ROLE]->(:Role)-[:GRANTS]->(res:Resource) WHERE u.id = {} RETURN res.id",
                    user_id
                );
                let mut out = self.age_ids(&via_team).await?;
                out.extend(self.age_ids(&direct).await?);
                Ok(sorted_unique(out))
            }
            Backend::Baseline => {
                let sql = "
                    SELECT g.resource_id
                    FROM member_of m
                    JOIN has_role hr ON hr.subject_id = m.team_id AND hr.subject_kind = 'team'
                    JOIN grants g   ON g.role_id = hr.role_id
                    WHERE m.user_id = $1
                    UNION
                    SELECT g.resource_id
                    FROM has_role hr
                    JOIN grants g ON g.role_id = hr.role_id
                    WHERE hr.subject_id = $1 AND hr.subject_kind = 'user'";
                self.sql_ids(sql, &[&user_id]).await
            }
        }
    }

    pub async fn impact_of(
        &self,
        service_id: i64,
        max_depth: i32,
    ) -> Result<Vec<i64>, tokio_postgres::Error> {
        match self.backend {
            Backend::Age => {
                let body = format!(
                    "MATCH (dep:Service)-[:DEPENDS_ON*1..{}]->(t:Service) WHERE t.id = {} RETURN DISTINCT dep.id",
                    max_depth, service_id
                );
                self.age_ids(&body).await
            }
            Backend::Baseline => {
                let sql = "
                    WITH RECURSIVE up AS (
                        SELECT src_id, 1 AS depth
                        FROM depends_on WHERE dst_id = $1 AND kind = 'service'
                        UNION
                        SELECT d.src_id, up.depth + 1
                        FROM depends_on d JOIN up ON d.dst_id = up.src_id
                        WHERE d.kind = 'service' AND up.depth < $2
                    )
                    SELECT DISTINCT src_id FROM up";
                self.sql_ids(sql, &[&service_id, &max_depth]).await
            }
        }
    }

    pub async fn cyclic_services(&self, max_len: i32) -> Result<Vec<i64>, tokio_postgres::Error> {
        match self.backend {
            Backend::Age => {
                let body = format!(
                    "MATCH (s:Service)-[:DEPENDS_ON*1..{}]->(s) RETURN DISTINCT s.id",
                    max_len
                );
                self.age_ids(&body).await
            }
            Backend::Baseline => {
                let sql = "
                    WITH RECURSIVE reach AS (
                        SELECT src_id AS start, dst_id AS cur, 1 AS depth
                        FROM depends_on WHERE kind = 'service'
                        UNION ALL
                        SELECT r.start, d.dst_id, r.depth + 1
                        FROM depends_on d JOIN reach r ON d.src_id = r.cur
                        WHERE d.kind = 'service' AND r.depth < $1
                    )
                    SELECT DISTINCT start FROM reach WHERE cur = start";
                self.sql_ids(sql, &[&max_len]).await
            }
        }
    }

    pub async fn distance(
        &self,
        a_id: i64,
        b_id: i64,
        max_len: i32,
    ) -> Result<i64, tokio_postgres::Error> {
        match self.backend {
            Backend::Age => {
                let body = format!(
                    "MATCH p=(a:User)-[:COLLABORATES*1..{}]-(b:User) WHERE a.id = {} AND b.id = {} RETURN length(p) ORDER BY length(p) LIMIT 1",
                    max_len, a_id, b_id
                );
                let sql = format!(
                    "SELECT (v)::text FROM cypher('platform', $$ {} $$) AS t(v agtype)",
                    body
                );
                let rows = self.client.query(&sql, &[]).await?;
                Ok(rows
                    .first()
                    .map(|r| r.get::<_, String>(0).trim().parse::<i64>().unwrap_or(-1))
                    .unwrap_or(-1))
            }
            Backend::Baseline => {
                let sql = "
                    WITH RECURSIVE bfs AS (
                        SELECT $1::bigint AS node, 0 AS dist
                        UNION
                        SELECT CASE WHEN c.a_id = b.node THEN c.b_id ELSE c.a_id END, b.dist + 1
                        FROM bfs b
                        JOIN collaborates c ON (c.a_id = b.node OR c.b_id = b.node)
                        WHERE b.dist < $3
                    )
                    SELECT COALESCE(MIN(dist), -1) FROM bfs WHERE node = $2";
                let row = self
                    .client
                    .query_one(sql, &[&a_id, &b_id, &max_len])
                    .await?;
                Ok(row.get::<_, i32>(0) as i64)
            }
        }
    }

    pub async fn ring_members(&self, length: i32) -> Result<Vec<i64>, tokio_postgres::Error> {
        match self.backend {
            Backend::Age => {
                let body = format!(
                    "MATCH (u:User)-[:COLLABORATES*{}..{}]-(u) RETURN DISTINCT u.id",
                    length, length
                );
                self.age_ids(&body).await
            }
            Backend::Baseline => {
                let sql = "
                    WITH RECURSIVE walk AS (
                        SELECT u.id AS start, ARRAY[u.id] AS path, u.id AS cur, 0 AS len
                        FROM users u
                        UNION ALL
                        SELECT w.start, w.path || e.nb, e.nb, w.len + 1
                        FROM walk w
                        CROSS JOIN LATERAL (
                            SELECT CASE WHEN c.a_id = w.cur THEN c.b_id ELSE c.a_id END AS nb
                            FROM collaborates c WHERE w.cur IN (c.a_id, c.b_id)
                        ) e
                        WHERE w.len < $1 - 1
                          AND e.nb > w.start
                          AND NOT e.nb = ANY(w.path)
                    )
                    SELECT DISTINCT unnest(path) AS id
                    FROM walk w
                    WHERE w.len = $1 - 1
                      AND EXISTS (
                        SELECT 1 FROM collaborates c
                        WHERE (c.a_id = w.cur AND c.b_id = w.start)
                           OR (c.b_id = w.cur AND c.a_id = w.start)
                      )";
                self.sql_ids(sql, &[&length]).await
            }
        }
    }
}

fn sorted_unique(mut v: Vec<i64>) -> Vec<i64> {
    v.sort_unstable();
    v.dedup();
    v
}
