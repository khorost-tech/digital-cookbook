//! Интеграционный тест Rust-клиента: грузит ту же крошечную фикстуру, что Go/Java,
//! и проверяет, что AGE и baseline дают те же руками посчитанные ответы.
//!
//! ВНИМАНИЕ: сбрасывает содержимое PostgreSQL (реляционные таблицы и AGE-граф).
//! Требует поднятого postgres (docker compose up -d postgres).

use graph_client_rust::queries::{Backend, PgQueries};
use tokio_postgres::NoTls;

const SCHEMA: &str = include_str!("../../../dataset/schema.sql");

fn conn_str() -> String {
    std::env::var("PG_CONN").unwrap_or_else(|_| {
        "host=localhost port=5432 user=graph password=graph dbname=graphlab".into()
    })
}

async fn load_fixture() {
    let (client, connection) = tokio_postgres::connect(&conn_str(), NoTls).await.unwrap();
    tokio::spawn(async move {
        let _ = connection.await;
    });

    // batch_execute использует простой протокол — корректно разбирает комментарии
    // и несколько выражений, поэтому schema.sql грузится как есть.
    client.batch_execute(SCHEMA).await.unwrap();

    let fixture = "
        INSERT INTO users(id,name) VALUES (1,'u'),(2,'u'),(3,'u'),(5,'u'),(6,'u'),(7,'u'),(8,'u');
        INSERT INTO teams(id,name) VALUES (10,'t');
        INSERT INTO roles(id,name) VALUES (20,'r');
        INSERT INTO resources(id,name) VALUES (30,'a'),(31,'b');
        INSERT INTO services(id,name) VALUES (40,'s'),(41,'s'),(42,'s'),(43,'s'),(44,'s'),(45,'s');
        INSERT INTO member_of(user_id,team_id) VALUES (1,10);
        INSERT INTO has_role(subject_id,subject_kind,role_id) VALUES (10,'team',20),(2,'user',20);
        INSERT INTO grants(role_id,resource_id) VALUES (20,30),(20,31);
        INSERT INTO collaborates(a_id,b_id) VALUES (1,2),(2,3),(5,6),(6,7),(7,8),(5,8);
        INSERT INTO depends_on(src_id,dst_id,kind) VALUES (40,41,'service'),(41,42,'service'),(43,44,'service'),(44,45,'service'),(45,43,'service');
    ";
    client.batch_execute(fixture).await.unwrap();

    let age = r#"
        LOAD 'age';
        SET search_path = ag_catalog, "$user", public;
        DO $$ BEGIN
          IF EXISTS (SELECT 1 FROM ag_catalog.ag_graph WHERE name='platform') THEN
            PERFORM ag_catalog.drop_graph('platform', true);
          END IF;
          PERFORM ag_catalog.create_graph('platform');
        END $$;
        SELECT * FROM cypher('platform', $$ UNWIND [1,2,3,5,6,7,8] AS id CREATE (:User {id: id}) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ CREATE (:Team {id: 10}) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ CREATE (:Role {id: 20}) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ UNWIND [30,31] AS id CREATE (:Resource {id: id}) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ UNWIND [40,41,42,43,44,45] AS id CREATE (:Service {id: id}) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (u:User),(t:Team) WHERE u.id=1 AND t.id=10 CREATE (u)-[:MEMBER_OF]->(t) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (t:Team),(r:Role) WHERE t.id=10 AND r.id=20 CREATE (t)-[:HAS_ROLE]->(r) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (u:User),(r:Role) WHERE u.id=2 AND r.id=20 CREATE (u)-[:HAS_ROLE]->(r) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (r:Role),(x:Resource) WHERE r.id=20 AND x.id=30 CREATE (r)-[:GRANTS]->(x) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (r:Role),(x:Resource) WHERE r.id=20 AND x.id=31 CREATE (r)-[:GRANTS]->(x) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (a:Service),(b:Service) WHERE a.id=40 AND b.id=41 CREATE (a)-[:DEPENDS_ON]->(b) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (a:Service),(b:Service) WHERE a.id=41 AND b.id=42 CREATE (a)-[:DEPENDS_ON]->(b) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (a:Service),(b:Service) WHERE a.id=43 AND b.id=44 CREATE (a)-[:DEPENDS_ON]->(b) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (a:Service),(b:Service) WHERE a.id=44 AND b.id=45 CREATE (a)-[:DEPENDS_ON]->(b) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (a:Service),(b:Service) WHERE a.id=45 AND b.id=43 CREATE (a)-[:DEPENDS_ON]->(b) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (a:User),(b:User) WHERE a.id=1 AND b.id=2 CREATE (a)-[:COLLABORATES]->(b) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (a:User),(b:User) WHERE a.id=2 AND b.id=3 CREATE (a)-[:COLLABORATES]->(b) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (a:User),(b:User) WHERE a.id=5 AND b.id=6 CREATE (a)-[:COLLABORATES]->(b) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (a:User),(b:User) WHERE a.id=6 AND b.id=7 CREATE (a)-[:COLLABORATES]->(b) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (a:User),(b:User) WHERE a.id=7 AND b.id=8 CREATE (a)-[:COLLABORATES]->(b) $$) AS (v agtype);
        SELECT * FROM cypher('platform', $$ MATCH (a:User),(b:User) WHERE a.id=5 AND b.id=8 CREATE (a)-[:COLLABORATES]->(b) $$) AS (v agtype);
    "#;
    client.batch_execute(age).await.unwrap();
}

#[tokio::test]
async fn all_pg_backends_agree() {
    load_fixture().await;

    for backend in [Backend::Age, Backend::Baseline] {
        let q = PgQueries::connect(&conn_str(), backend).await.unwrap();
        let b = q.backend_name();

        assert_eq!(
            q.accessible_resources(1).await.unwrap(),
            vec![30, 31],
            "{} access(1)",
            b
        );
        assert_eq!(
            q.accessible_resources(2).await.unwrap(),
            vec![30, 31],
            "{} access(2)",
            b
        );
        assert_eq!(
            q.accessible_resources(3).await.unwrap(),
            Vec::<i64>::new(),
            "{} access(3)",
            b
        );
        assert_eq!(
            q.impact_of(42, 10).await.unwrap(),
            vec![40, 41],
            "{} impact(42)",
            b
        );
        assert_eq!(
            q.impact_of(41, 10).await.unwrap(),
            vec![40],
            "{} impact(41)",
            b
        );
        assert_eq!(
            q.cyclic_services(10).await.unwrap(),
            vec![43, 44, 45],
            "{} cyclic",
            b
        );
        assert_eq!(
            q.ring_members(4).await.unwrap(),
            vec![5, 6, 7, 8],
            "{} ring(4)",
            b
        );
        assert_eq!(
            q.ring_members(3).await.unwrap(),
            Vec::<i64>::new(),
            "{} ring(3)",
            b
        );
        assert_eq!(q.distance(1, 3, 10).await.unwrap(), 2, "{} dist(1,3)", b);
        assert_eq!(q.distance(5, 7, 10).await.unwrap(), 2, "{} dist(5,7)", b);
        assert_eq!(q.distance(5, 8, 10).await.unwrap(), 1, "{} dist(5,8)", b);
        assert_eq!(q.distance(1, 7, 10).await.unwrap(), -1, "{} dist(1,7)", b);
    }
}
