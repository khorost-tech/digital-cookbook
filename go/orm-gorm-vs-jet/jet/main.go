// go-jet: те же пять кейсов на общем домене.
// Компилируется ТОЛЬКО после кодогена: `make gen` создаёт пакеты table/model в jet/.gen.
// Исполнение — через database/sql; pgx подключён в режиме stdlib.
package main

import (
	"database/sql"
	"fmt"
	"log"

	_ "github.com/jackc/pgx/v5/stdlib"

	. "github.com/go-jet/jet/v2/postgres"

	"orm-gorm-vs-jet/jet/.gen/cookbook/public/model"
	. "orm-gorm-vs-jet/jet/.gen/cookbook/public/table"
)

const dsn = "postgres://cookbook:cookbook@localhost:5432/cookbook?sslmode=disable"

func main() {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// 1. CRUD
	insert := Posts.INSERT(Posts.UserID, Posts.Title, Posts.Metadata).
		VALUES(int64(1), "Hello (jet)", `{"source":"blog"}`).
		RETURNING(Posts.ID)
	var created model.Posts
	if err := insert.Query(db, &created); err != nil {
		log.Fatal(err)
	}
	var got model.Posts
	if err := SELECT(Posts.AllColumns).FROM(Posts).
		WHERE(Posts.ID.EQ(Int(created.ID))).Query(db, &got); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("CRUD: created id=%d title=%q\n", got.ID, got.Title)

	// 2. Список пользователей с постами (LEFT JOIN + вложенный маппинг)
	var users []struct {
		model.Users
		Posts []model.Posts
	}
	if err := SELECT(Users.AllColumns, Posts.AllColumns).
		FROM(Users.LEFT_JOIN(Posts, Posts.UserID.EQ(Users.ID))).
		ORDER_BY(Users.ID.ASC()).Query(db, &users); err != nil {
		log.Fatal(err)
	}
	for _, u := range users {
		fmt.Printf("user %s: %d posts\n", u.Name, len(u.Posts))
	}

	// 3. Агрегат COUNT/GROUP BY (типобезопасно)
	var rows []struct {
		UserID int64
		Cnt    int64
	}
	if err := SELECT(Posts.UserID.AS("user_id"), COUNT(Posts.ID).AS("cnt")).
		FROM(Posts).GROUP_BY(Posts.UserID).Query(db, &rows); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("aggregate: %+v\n", rows)

	// 4. Фильтр по jsonb (raw — типобезопасного DSL у go-jet для jsonb нет)
	var blogPosts []model.Posts
	if err := SELECT(Posts.AllColumns).FROM(Posts).
		WHERE(RawBool("posts.metadata ->> 'source' = #source", RawArgs{"#source": "blog"})).
		Query(db, &blogPosts); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("jsonb filter: %d posts with source=blog\n", len(blogPosts))

	// 5. Partial update, где важен ноль — явный SET, обнуление проходит
	if _, err := Posts.UPDATE(Posts.Likes).SET(Int(0)).
		WHERE(Posts.ID.EQ(Int(created.ID))).Exec(db); err != nil {
		log.Fatal(err)
	}
	fmt.Println("partial update: likes = 0 (явный SET)")
}
