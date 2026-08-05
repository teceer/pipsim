// Hand-written, not generated: buf emits .pb.go files but no module metadata.
// Kept as its own module so services depend on the contracts explicitly rather
// than through a repo-wide package path.
module github.com/teceer/pipsim/gen/go

go 1.23

require (
	connectrpc.com/connect v1.17.0
	google.golang.org/protobuf v1.35.1
)
