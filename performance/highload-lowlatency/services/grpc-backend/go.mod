module khorost.tech/highload-grpc-backend

go 1.25.0

require (
	google.golang.org/grpc v1.79.3
	khorost.tech/highload-grpc-gen v0.0.0
)

require (
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
)

replace khorost.tech/highload-grpc-gen => ../../grpc/gen
