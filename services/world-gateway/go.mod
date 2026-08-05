module github.com/teceer/pipsim/services/world-gateway

go 1.23

require (
	connectrpc.com/connect v1.17.0
	connectrpc.com/otelconnect v0.7.1
	github.com/twmb/franz-go v1.17.1
	go.opentelemetry.io/otel v1.31.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.31.0
	go.opentelemetry.io/otel/sdk v1.31.0
	golang.org/x/net v0.30.0
	google.golang.org/protobuf v1.35.1
)
