// GORM: те же пять кейсов на общем домене. Компилируется и запускается как есть
// (после `make up && make migrate`). Проверено на GORM v2.
package main

import (
	"fmt"
	"log"

	"gorm.io/datatypes"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const dsn = "postgres://cookbook:cookbook@localhost:5432/cookbook?sslmode=disable"

type User struct {
	ID    uint
	Name  string
	Posts []Post // has-many, FK по умолчанию UserID
}

type Post struct {
	ID            uint
	UserID        uint
	Title         string
	Metadata      datatypes.JSON // jsonb
	Likes         int
	CommentsCount int
	Tags          []Tag `gorm:"many2many:post_tags"`
}

type Tag struct {
	ID   uint
	Name string
}

func main() {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}

	// 1. CRUD
	post := Post{UserID: 1, Title: "Hello (gorm)", Metadata: datatypes.JSON(`{"source":"blog"}`)}
	db.Create(&post)
	var got Post
	db.First(&got, post.ID)
	fmt.Printf("CRUD: created id=%d title=%q\n", got.ID, got.Title)

	// 2. Список пользователей с постами (Preload — 2 запроса; без него был бы N+1)
	var users []User
	db.Preload("Posts").Find(&users)
	for _, u := range users {
		fmt.Printf("user %s: %d posts\n", u.Name, len(u.Posts))
	}

	// 3. Агрегат COUNT/GROUP BY (строковый Select + Scan — типобезопасность теряется)
	type Row struct {
		UserID uint
		Cnt    int
	}
	var rows []Row
	db.Model(&Post{}).Select("user_id, count(*) AS cnt").Group("user_id").Scan(&rows)
	fmt.Printf("aggregate: %+v\n", rows)

	// 4. Фильтр по jsonb через datatypes.JSONQuery
	var blogPosts []Post
	db.Where(datatypes.JSONQuery("metadata").Equals("blog", "source")).Find(&blogPosts)
	fmt.Printf("jsonb filter: %d posts with source=blog\n", len(blogPosts))

	// 5. Partial update, где важен ноль.
	// НЕ сработает: db.Model(&got).Updates(Post{Likes: 0}) — 0 это zero-value, пропустится.
	db.Model(&Post{}).Where("id = ?", got.ID).Select("likes").Updates(Post{Likes: 0})
	fmt.Println(`partial update: likes = 0 через Select("likes")`)
}
