// Command dataset — детерминированный генератор общего e-commerce датасета
// к серии статей "MongoDB: глубокое погружение". Один и тот же seed (42) и
// фиксированные счётчики (users/products/orders) дают байт-в-байт одинаковый
// вывод при каждом запуске — не зависит от time.Now() (даты отсчитываются от
// зашитых в код опорных точек datasetEpoch/datasetHorizon, а не от даты
// запуска генератора) и не зависит от порядка обхода map (весь вывод
// строится из срезов структур, сериализуемых encoding/json в порядке полей
// структуры; map нигде не используется для данных, попадающих в вывод).
//
// Домен — e-commerce: users (профиль + встроенные addresses[]/tags[]),
// products (для $lookup из orders), orders (ссылка на user + встроенные
// items[]). Даёт одновременно embedding (addresses/tags/items),
// referencing (order.user_id, item.product_id) и массивы для multikey-
// индексов — то, что нужно стендам #1 (modeling), #3 (indexes), #4
// (aggregation/$lookup).
//
// Вывод:
//   - out/users.jsonl, out/orders.jsonl, out/products.jsonl — по документу
//     на строку, MongoDB Extended JSON (relaxed) — вход для mongoimport.
//   - out/pg-seed.sql — тот же корпус для PostgreSQL: jsonb-таблицы
//     (документный контраст) + нормализованные таблицы (реляционный
//     контраст), заливка через COPY ... FROM STDIN (быстрее и компактнее
//     построчных INSERT на сотнях тысяч строк).
//
// out/ не коммитится (см. mongodb/.gitignore: `dataset/out/`) — вывод
// регенерируется этим генератором за секунды, коммитить многие десятки
// мегабайт бинарно-нечитаемого JSONL/SQL нет смысла.
package main

import (
	"bufio"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// manifestJSON — dataset/manifest.json, встроенный в бинарь на этапе сборки
// (go:embed), а не читаемый по относительному пути в рантайме: значения
// одинаковы независимо от рабочей директории запуска. Сам manifest.json —
// ЕДИНСТВЕННЫЙ источник правды для counts/seed, коммитится (в отличие от
// dataset/out/) и читается программно любым языком (Go — этим генератором
// через encoding/json, Java-стенды — своим JSON-парсером, shell — jq) — то,
// на что ассертят все последующие стенды серии (импорт → count документов).
//
//go:embed manifest.json
var manifestJSON []byte

// manifest — форма dataset/manifest.json.
type manifest struct {
	Seed     int64 `json:"seed"`
	Users    int   `json:"users"`
	Products int   `json:"products"`
	Orders   int   `json:"orders"`
}

// Manifest — распакованные значения manifest.json, используются везде ниже
// вместо константных счётчиков.
var Manifest = mustLoadManifest()

func mustLoadManifest() manifest {
	var m manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		panic(fmt.Sprintf("parse manifest.json: %v", err))
	}
	return m
}

// Опорные точки времени — НЕ time.Now(): генератор должен быть детерминирован
// независимо от даты фактического запуска.
var (
	datasetEpoch   = time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	datasetHorizon = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
)

// Непересекающиеся диапазоны для hex-идентификаторов трёх коллекций —
// чтобы user_id/product_id внутри orders/items визуально не путались с _id
// документов других коллекций (не обязательно для корректности Mongo,
// но удобно при отладке дампов).
const (
	userIDBase    uint64 = 0x0000_0000_0000
	productIDBase uint64 = 0x1000_0000_0000
	orderIDBase   uint64 = 0x2000_0000_0000
)

// -- Пулы данных (фиксированный порядок — часть детерминированного вывода) --

var firstNames = []string{
	"James", "Mary", "Robert", "Patricia", "John", "Jennifer", "Michael", "Linda",
	"David", "Elizabeth", "William", "Barbara", "Richard", "Susan", "Joseph", "Jessica",
	"Thomas", "Sarah", "Charles", "Karen", "Christopher", "Nancy", "Daniel", "Lisa",
	"Matthew", "Margaret", "Anthony", "Betty", "Mark", "Sandra", "Donald", "Ashley",
	"Steven", "Dorothy", "Paul", "Kimberly", "Andrew", "Emily", "Joshua", "Donna",
}

var lastNames = []string{
	"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller", "Davis",
	"Rodriguez", "Martinez", "Hernandez", "Lopez", "Gonzalez", "Wilson", "Anderson",
	"Thomas", "Taylor", "Moore", "Jackson", "Martin", "Lee", "Perez", "Thompson",
	"White", "Harris", "Sanchez", "Clark", "Ramirez", "Lewis", "Robinson",
}

type cityCountry struct{ City, Country string }

var cities = []cityCountry{
	{"New York", "USA"}, {"Los Angeles", "USA"}, {"Chicago", "USA"}, {"Houston", "USA"},
	{"London", "UK"}, {"Manchester", "UK"}, {"Berlin", "Germany"}, {"Munich", "Germany"},
	{"Paris", "France"}, {"Lyon", "France"}, {"Madrid", "Spain"}, {"Barcelona", "Spain"},
	{"Rome", "Italy"}, {"Milan", "Italy"}, {"Amsterdam", "Netherlands"}, {"Warsaw", "Poland"},
	{"Prague", "Czechia"}, {"Vienna", "Austria"}, {"Stockholm", "Sweden"}, {"Oslo", "Norway"},
	{"Helsinki", "Finland"}, {"Lisbon", "Portugal"}, {"Dublin", "Ireland"}, {"Toronto", "Canada"},
	{"Vancouver", "Canada"}, {"Sydney", "Australia"}, {"Melbourne", "Australia"}, {"Tokyo", "Japan"},
	{"Osaka", "Japan"}, {"Seoul", "South Korea"},
}

var streets = []string{
	"Maple St", "Oak Ave", "Cedar Blvd", "Elm St", "Pine Rd", "Birch Ln", "Willow Way",
	"Chestnut St", "Spruce Ave", "Walnut St", "Cherry Ln", "Aspen Dr", "Magnolia Ave",
	"Sycamore St", "Poplar Rd",
}

var tagPool = []string{
	"vip", "newsletter", "wholesale", "loyalty", "referral", "beta-tester", "mobile-app",
	"desktop", "gift-cards", "subscriber", "affiliate", "early-adopter", "corporate",
	"student-discount", "seasonal-buyer", "clearance-hunter", "reviewer", "influencer",
	"bulk-buyer", "international",
}

var categories = []string{
	"electronics", "home-and-kitchen", "books", "sports-outdoors", "toys-games",
	"beauty", "clothing", "automotive", "garden", "office-supplies", "pet-supplies",
	"health", "music-instruments", "groceries", "jewelry",
}

var productAdjectives = []string{
	"Compact", "Premium", "Wireless", "Ultra", "Portable", "Classic", "Eco", "Smart",
	"Heavy-Duty", "Deluxe", "Rapid", "Silent", "Adjustable", "Foldable", "Rugged",
}

var productNouns = []string{
	"Blender", "Backpack", "Headphones", "Lamp", "Keyboard", "Monitor", "Charger",
	"Notebook", "Speaker", "Bottle", "Jacket", "Sneakers", "Watch", "Camera", "Router",
	"Mouse", "Desk", "Chair", "Grill", "Tent",
}

var orderStatuses = []string{"pending", "paid", "shipped", "delivered", "cancelled", "refunded"}

// pick — детерминированный выбор элемента среза через переданный *rand.Rand
// (потребляет ровно один Intn-вызов состояния генератора).
func pick[T any](r *rand.Rand, xs []T) T {
	return xs[r.Intn(len(xs))]
}

// -- Extended JSON wire-типы (MongoDB Extended JSON v2, relaxed mode) --

type oidWire struct {
	Oid string `json:"$oid"`
}

// ObjectID — 24-символьный hex-идентификатор документа. Сериализуется как
// {"$oid": "..."} — формат, который mongoimport распознаёт как ObjectId.
type ObjectID string

func (o ObjectID) MarshalJSON() ([]byte, error) {
	return json.Marshal(oidWire{Oid: string(o)})
}

type dateWire struct {
	Date string `json:"$date"`
}

// ExtDate — дата/время, сериализуемая как {"$date": "ISO-8601"}.
type ExtDate time.Time

func (d ExtDate) MarshalJSON() ([]byte, error) {
	return json.Marshal(dateWire{Date: time.Time(d).UTC().Format("2006-01-02T15:04:05.000Z")})
}

func hexID(base uint64, i int) ObjectID {
	return ObjectID(fmt.Sprintf("%024x", base+uint64(i)+1))
}

// -- Доменные структуры --

type Address struct {
	Street    string `json:"street"`
	City      string `json:"city"`
	Country   string `json:"country"`
	Zip       string `json:"zip"`
	IsDefault bool   `json:"is_default"`
}

type User struct {
	ID        ObjectID  `json:"_id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt ExtDate   `json:"created_at"`
	Addresses []Address `json:"addresses"`
	Tags      []string  `json:"tags"`
}

// plainUser — тот же документ без Mongo-специфичных Extended JSON обёрток
// ($oid/$date) — для jsonb-колонки PG-контраста: _id/created_at как
// обычные JSON-строки, а не {"$oid":...}/{"$date":...} (PG про них ничего
// не знает — это ЧАСТЬ контраста «документ в Mongo vs jsonb в PG»).
type plainUser struct {
	ID        string    `json:"_id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt string    `json:"created_at"`
	Addresses []Address `json:"addresses"`
	Tags      []string  `json:"tags"`
}

func (u User) Plain() plainUser {
	return plainUser{
		ID: string(u.ID), Name: u.Name, Email: u.Email,
		CreatedAt: time.Time(u.CreatedAt).UTC().Format(time.RFC3339),
		Addresses: u.Addresses, Tags: u.Tags,
	}
}

type Product struct {
	ID       ObjectID `json:"_id"`
	SKU      string   `json:"sku"`
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Price    float64  `json:"price"`
}

type plainProduct struct {
	ID       string  `json:"_id"`
	SKU      string  `json:"sku"`
	Name     string  `json:"name"`
	Category string  `json:"category"`
	Price    float64 `json:"price"`
}

func (p Product) Plain() plainProduct {
	return plainProduct{ID: string(p.ID), SKU: p.SKU, Name: p.Name, Category: p.Category, Price: p.Price}
}

type OrderItem struct {
	ProductID   ObjectID `json:"product_id"`
	ProductName string   `json:"product_name"`
	Qty         int      `json:"qty"`
	Price       float64  `json:"price"`
}

type plainOrderItem struct {
	ProductID   string  `json:"product_id"`
	ProductName string  `json:"product_name"`
	Qty         int     `json:"qty"`
	Price       float64 `json:"price"`
}

type Order struct {
	ID        ObjectID    `json:"_id"`
	UserID    ObjectID    `json:"user_id"`
	Status    string      `json:"status"`
	CreatedAt ExtDate     `json:"created_at"`
	Items     []OrderItem `json:"items"`
	Total     float64     `json:"total"`
}

type plainOrder struct {
	ID        string           `json:"_id"`
	UserID    string           `json:"user_id"`
	Status    string           `json:"status"`
	CreatedAt string           `json:"created_at"`
	Items     []plainOrderItem `json:"items"`
	Total     float64          `json:"total"`
}

func (o Order) Plain() plainOrder {
	items := make([]plainOrderItem, len(o.Items))
	for i, it := range o.Items {
		items[i] = plainOrderItem{
			ProductID: string(it.ProductID), ProductName: it.ProductName,
			Qty: it.Qty, Price: it.Price,
		}
	}
	return plainOrder{
		ID: string(o.ID), UserID: string(o.UserID), Status: o.Status,
		CreatedAt: time.Time(o.CreatedAt).UTC().Format(time.RFC3339),
		Items:     items, Total: o.Total,
	}
}

// round2 — округление до 2 знаков (денежные суммы).
func round2(x float64) float64 { return math.Round(x*100) / 100 }

// -- Генерация: строго последовательно, один общий *rand.Rand — порядок --
// -- вызовов Intn/Float64/Perm ЕСТЬ часть детерминированного контракта. --

func genUsers(r *rand.Rand) []User {
	horizonSpan := int64(datasetHorizon.Sub(datasetEpoch))
	users := make([]User, 0, Manifest.Users)
	for i := 0; i < Manifest.Users; i++ {
		first := pick(r, firstNames)
		last := pick(r, lastNames)
		name := first + " " + last
		email := fmt.Sprintf("%s.%s%d@example.com", strings.ToLower(first), strings.ToLower(last), i)
		createdAt := datasetEpoch.Add(time.Duration(r.Int63n(horizonSpan)))

		addrCount := 1 + r.Intn(3) // 1..3
		defaultIdx := r.Intn(addrCount)
		addrs := make([]Address, addrCount)
		for a := 0; a < addrCount; a++ {
			cc := pick(r, cities)
			addrs[a] = Address{
				Street:    fmt.Sprintf("%d %s", 1+r.Intn(9999), pick(r, streets)),
				City:      cc.City,
				Country:   cc.Country,
				Zip:       fmt.Sprintf("%05d", r.Intn(100000)),
				IsDefault: a == defaultIdx,
			}
		}

		tagCount := 2 + r.Intn(4) // 2..5
		perm := r.Perm(len(tagPool))[:tagCount]
		tags := make([]string, tagCount)
		for k, idx := range perm {
			tags[k] = tagPool[idx]
		}

		users = append(users, User{
			ID: hexID(userIDBase, i), Name: name, Email: email,
			CreatedAt: ExtDate(createdAt), Addresses: addrs, Tags: tags,
		})
	}
	return users
}

func genProducts(r *rand.Rand) []Product {
	products := make([]Product, 0, Manifest.Products)
	for i := 0; i < Manifest.Products; i++ {
		adj := pick(r, productAdjectives)
		noun := pick(r, productNouns)
		name := adj + " " + noun
		cat := pick(r, categories)
		price := round2(5 + r.Float64()*495) // 5.00..500.00
		sku := fmt.Sprintf("SKU-%05d", i+1)
		products = append(products, Product{
			ID: hexID(productIDBase, i), SKU: sku, Name: name, Category: cat, Price: price,
		})
	}
	return products
}

func genOrders(r *rand.Rand, users []User, products []Product) []Order {
	orders := make([]Order, 0, Manifest.Orders)
	for i := 0; i < Manifest.Orders; i++ {
		u := users[r.Intn(len(users))]
		userCreated := time.Time(u.CreatedAt)
		// span всегда > 0: genUsers ограничивает CreatedAt интервалом
		// [datasetEpoch, datasetHorizon), т.е. userCreated < datasetHorizon.
		span := int64(datasetHorizon.Sub(userCreated))
		createdAt := userCreated.Add(time.Duration(r.Int63n(span)))

		itemCount := 1 + r.Intn(5) // 1..5
		items := make([]OrderItem, itemCount)
		var total float64
		for k := 0; k < itemCount; k++ {
			p := products[r.Intn(len(products))]
			qty := 1 + r.Intn(4) // 1..4
			items[k] = OrderItem{ProductID: p.ID, ProductName: p.Name, Qty: qty, Price: p.Price}
			total += p.Price * float64(qty)
		}

		orders = append(orders, Order{
			ID: hexID(orderIDBase, i), UserID: u.ID, Status: pick(r, orderStatuses),
			CreatedAt: ExtDate(createdAt), Items: items, Total: round2(total),
		})
	}
	return orders
}

// -- Вывод: JSONL (Extended JSON) для mongoimport --

func writeJSONL[T any](path string, items []T) (int64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	enc := json.NewEncoder(w)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			f.Close()
			return 0, fmt.Errorf("encode %s: %w", path, err)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return 0, err
	}
	info, statErr := f.Stat()
	if closeErr := f.Close(); closeErr != nil {
		return 0, closeErr
	}
	if statErr != nil {
		return 0, statErr
	}
	return info.Size(), nil
}

// -- Вывод: pg-seed.sql (jsonb-контраст + нормализованные таблицы) --

const ddl = `-- Схема для PostgreSQL-контраста (см. dataset/main.go).
-- (1) *_doc — jsonb-таблицы: тот же документ, что в out/*.jsonl, но БЕЗ
--     Mongo Extended JSON обёрток ($oid/$date) — PG про них не знает,
--     это часть контраста "документ в Mongo vs jsonb в PG".
-- (2) users/addresses/user_tags/products/orders/order_items —
--     нормализованные таблицы для реляционного контраста.
CREATE TABLE IF NOT EXISTS users_doc (
    id  text PRIMARY KEY,
    doc jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS products_doc (
    id  text PRIMARY KEY,
    doc jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS orders_doc (
    id  text PRIMARY KEY,
    doc jsonb NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id         text PRIMARY KEY,
    name       text NOT NULL,
    email      text NOT NULL,
    created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS addresses (
    id         bigserial PRIMARY KEY,
    user_id    text NOT NULL REFERENCES users(id),
    street     text NOT NULL,
    city       text NOT NULL,
    country    text NOT NULL,
    zip        text NOT NULL,
    is_default boolean NOT NULL
);
CREATE TABLE IF NOT EXISTS user_tags (
    user_id text NOT NULL REFERENCES users(id),
    tag     text NOT NULL,
    PRIMARY KEY (user_id, tag)
);
CREATE TABLE IF NOT EXISTS products (
    id       text PRIMARY KEY,
    sku      text NOT NULL,
    name     text NOT NULL,
    category text NOT NULL,
    price    numeric(12,2) NOT NULL
);
CREATE TABLE IF NOT EXISTS orders (
    id         text PRIMARY KEY,
    user_id    text NOT NULL REFERENCES users(id),
    status     text NOT NULL,
    created_at timestamptz NOT NULL,
    total      numeric(12,2) NOT NULL
);
CREATE TABLE IF NOT EXISTS order_items (
    id           bigserial PRIMARY KEY,
    order_id     text NOT NULL REFERENCES orders(id),
    product_id   text NOT NULL REFERENCES products(id),
    product_name text NOT NULL,
    qty          integer NOT NULL,
    price        numeric(12,2) NOT NULL
);

`

// copyBlock открывает "COPY table (cols) FROM STDIN WITH (FORMAT csv);",
// пишет rows через encoding/csv (корректно экранирует запятые/кавычки в
// jsonb-тексте) и закрывает блок терминатором "\." — синтаксис, который
// psql -f распознаёт как встроенные COPY-данные (тот же приём, что в
// выводе pg_dump).
func copyBlock(bw *bufio.Writer, table string, cols []string, rows func(w *csv.Writer) error) error {
	if _, err := fmt.Fprintf(bw, "COPY %s (%s) FROM STDIN WITH (FORMAT csv);\n", table, strings.Join(cols, ", ")); err != nil {
		return err
	}
	cw := csv.NewWriter(bw)
	if err := rows(cw); err != nil {
		return err
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return err
	}
	if _, err := fmt.Fprint(bw, "\\.\n\n"); err != nil {
		return err
	}
	return nil
}

func writePGSeed(path string, users []User, products []Product, orders []Order) (int64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	bw := bufio.NewWriterSize(f, 1<<20)

	fmt.Fprintf(bw, "-- pg-seed.sql — детерминированный датасет (seed=%d) для PostgreSQL-контраста.\n", Manifest.Seed)
	fmt.Fprintln(bw, "-- Сгенерировано dataset/main.go — НЕ редактировать вручную, перегенерировать `go run .`.")
	fmt.Fprintf(bw, "-- Тот же корпус, что out/*.jsonl: users=%d orders=%d products=%d.\n\n", Manifest.Users, Manifest.Orders, Manifest.Products)
	fmt.Fprint(bw, ddl)

	writeErr := func() error {
		if err := copyBlock(bw, "users_doc", []string{"id", "doc"}, func(cw *csv.Writer) error {
			for _, u := range users {
				doc, err := json.Marshal(u.Plain())
				if err != nil {
					return err
				}
				if err := cw.Write([]string{string(u.ID), string(doc)}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}

		if err := copyBlock(bw, "users", []string{"id", "name", "email", "created_at"}, func(cw *csv.Writer) error {
			for _, u := range users {
				rec := []string{string(u.ID), u.Name, u.Email, time.Time(u.CreatedAt).UTC().Format(time.RFC3339)}
				if err := cw.Write(rec); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}

		if err := copyBlock(bw, "addresses", []string{"user_id", "street", "city", "country", "zip", "is_default"}, func(cw *csv.Writer) error {
			for _, u := range users {
				for _, a := range u.Addresses {
					rec := []string{string(u.ID), a.Street, a.City, a.Country, a.Zip, strconvBool(a.IsDefault)}
					if err := cw.Write(rec); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}

		if err := copyBlock(bw, "user_tags", []string{"user_id", "tag"}, func(cw *csv.Writer) error {
			for _, u := range users {
				for _, t := range u.Tags {
					if err := cw.Write([]string{string(u.ID), t}); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}

		if err := copyBlock(bw, "products_doc", []string{"id", "doc"}, func(cw *csv.Writer) error {
			for _, p := range products {
				doc, err := json.Marshal(p.Plain())
				if err != nil {
					return err
				}
				if err := cw.Write([]string{string(p.ID), string(doc)}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}

		if err := copyBlock(bw, "products", []string{"id", "sku", "name", "category", "price"}, func(cw *csv.Writer) error {
			for _, p := range products {
				rec := []string{string(p.ID), p.SKU, p.Name, p.Category, fmt.Sprintf("%.2f", p.Price)}
				if err := cw.Write(rec); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}

		if err := copyBlock(bw, "orders_doc", []string{"id", "doc"}, func(cw *csv.Writer) error {
			for _, o := range orders {
				doc, err := json.Marshal(o.Plain())
				if err != nil {
					return err
				}
				if err := cw.Write([]string{string(o.ID), string(doc)}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}

		if err := copyBlock(bw, "orders", []string{"id", "user_id", "status", "created_at", "total"}, func(cw *csv.Writer) error {
			for _, o := range orders {
				rec := []string{string(o.ID), string(o.UserID), o.Status, time.Time(o.CreatedAt).UTC().Format(time.RFC3339), fmt.Sprintf("%.2f", o.Total)}
				if err := cw.Write(rec); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}

		if err := copyBlock(bw, "order_items", []string{"order_id", "product_id", "product_name", "qty", "price"}, func(cw *csv.Writer) error {
			for _, o := range orders {
				for _, it := range o.Items {
					rec := []string{string(o.ID), string(it.ProductID), it.ProductName, fmt.Sprintf("%d", it.Qty), fmt.Sprintf("%.2f", it.Price)}
					if err := cw.Write(rec); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
		return nil
	}()

	if writeErr != nil {
		f.Close()
		return 0, writeErr
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return 0, err
	}
	info, statErr := f.Stat()
	if closeErr := f.Close(); closeErr != nil {
		return 0, closeErr
	}
	if statErr != nil {
		return 0, statErr
	}
	return info.Size(), nil
}

func strconvBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func main() {
	t0 := time.Now()
	r := rand.New(rand.NewSource(Manifest.Seed))

	users := genUsers(r)
	products := genProducts(r)
	orders := genOrders(r, users, products)

	outDir := "out"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir out:", err)
		os.Exit(1)
	}

	usersSize, err := writeJSONL(filepath.Join(outDir, "users.jsonl"), users)
	if err != nil {
		fmt.Fprintln(os.Stderr, "write users.jsonl:", err)
		os.Exit(1)
	}
	productsSize, err := writeJSONL(filepath.Join(outDir, "products.jsonl"), products)
	if err != nil {
		fmt.Fprintln(os.Stderr, "write products.jsonl:", err)
		os.Exit(1)
	}
	ordersSize, err := writeJSONL(filepath.Join(outDir, "orders.jsonl"), orders)
	if err != nil {
		fmt.Fprintln(os.Stderr, "write orders.jsonl:", err)
		os.Exit(1)
	}
	sqlSize, err := writePGSeed(filepath.Join(outDir, "pg-seed.sql"), users, products, orders)
	if err != nil {
		fmt.Fprintln(os.Stderr, "write pg-seed.sql:", err)
		os.Exit(1)
	}

	elapsed := time.Since(t0)
	fmt.Printf("seed=%d users=%d products=%d orders=%d\n", Manifest.Seed, len(users), len(products), len(orders))
	fmt.Printf("out/users.jsonl    %10d bytes\n", usersSize)
	fmt.Printf("out/products.jsonl %10d bytes\n", productsSize)
	fmt.Printf("out/orders.jsonl   %10d bytes\n", ordersSize)
	fmt.Printf("out/pg-seed.sql    %10d bytes\n", sqlSize)
	fmt.Printf("generated in %s\n", elapsed)
}
