module tech.khorost/mongodb-cookbook/indexes

go 1.25.0

require (
	go.mongodb.org/mongo-driver/v2 v2.4.2
	tech.khorost/mongodb-cookbook/drivers/go v0.0.0-00010101000000-000000000000
)

require (
	github.com/golang/snappy v1.0.0 // indirect
	github.com/klauspost/compress v1.16.7 // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.1.2 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.37.0 // indirect
)

replace tech.khorost/mongodb-cookbook/drivers/go => ../drivers/go
