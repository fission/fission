module github.com/fission/fission

go 1.16

require (
	contrib.go.opencensus.io/exporter/jaeger v0.2.1
	github.com/Azure/azure-sdk-for-go v32.5.0+incompatible
	github.com/Azure/go-autorest/autorest v0.11.18 // indirect
	github.com/Microsoft/go-winio v0.4.16 // indirect
	github.com/Nvveen/Gotty v0.0.0-20120604004816-cd527374f1e5 // indirect
	github.com/Shopify/sarama v1.29.1
	github.com/aws/aws-sdk-go v1.36.33 // indirect
	github.com/blend/go-sdk v1.20210116.5 // indirect
	github.com/bsm/sarama-cluster v2.1.15+incompatible
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/containerd/continuity v0.0.0-20201208142359-180525291bb7 // indirect
	github.com/dchest/uniuri v0.0.0-20160212164326-8902c56451e9
	github.com/docker/go-connections v0.4.0 // indirect
	github.com/docopt/docopt-go v0.0.0-20180111231733-ee0de3bc6815
	github.com/dsnet/compress v0.0.1 // indirect
	github.com/dustin/go-humanize v1.0.0
	github.com/emicklei/go-restful v2.9.6+incompatible
	github.com/emicklei/go-restful-openapi v1.2.0
	github.com/fatih/color v1.12.0
	github.com/fsnotify/fsnotify v1.4.9
	github.com/ghodss/yaml v1.0.0
	github.com/go-git/go-git/v5 v5.2.0
	github.com/go-ini/ini v1.62.0 // indirect
	github.com/go-openapi/spec v0.19.5
	github.com/golang/freetype v0.0.0-20170609003504-e2365dfdc4a0 // indirect
	github.com/gorilla/mux v1.8.0
	github.com/gotestyourself/gotestyourself v2.2.0+incompatible // indirect
	github.com/graymeta/stow v0.2.7
	github.com/hashicorp/go-multierror v1.1.1
	github.com/imdario/mergo v0.3.12
	github.com/influxdata/influxdb v1.2.0
	github.com/mholt/archiver v0.0.0-20180417220235-e4ef56d48eb0
	github.com/minio/minio-go v6.0.14+incompatible
	github.com/nats-io/nats-streaming-server v0.22.0
	github.com/nats-io/nats.go v1.11.0
	github.com/nats-io/stan.go v0.9.0
	github.com/nwaples/rardecode v1.1.0 // indirect
	github.com/opencontainers/image-spec v1.0.1 // indirect
	github.com/opencontainers/runc v1.0.1 // indirect
	github.com/ory/dockertest v3.3.5+incompatible
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.11.0
	github.com/prometheus/common v0.26.0
	github.com/robfig/cron v0.0.0-20180505203441-b41be1df6967
	github.com/satori/go.uuid v1.2.0
	github.com/spf13/cobra v1.2.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.7.0
	github.com/ulikunitz/xz v0.5.9 // indirect
	github.com/wcharczuk/go-chart v2.0.1+incompatible
	go.opencensus.io v0.23.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.22.0
	go.opentelemetry.io/otel v1.0.0-RC2
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.0.0-RC2
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.0.0-RC2
	go.opentelemetry.io/otel/sdk v1.0.0-RC2
	go.opentelemetry.io/otel/trace v1.0.0-RC2
	go.uber.org/zap v1.18.1
	golang.org/x/net v0.0.0-20210614182718-04defd469f4e
	google.golang.org/grpc v1.39.0
	gotest.tools v2.2.0+incompatible // indirect
	k8s.io/api v0.21.4
	k8s.io/apiextensions-apiserver v0.21.4
	k8s.io/apimachinery v0.21.4
	k8s.io/client-go v0.21.4
	k8s.io/klog v1.0.0
	k8s.io/metrics v0.21.4
	sigs.k8s.io/controller-runtime v0.9.6
)
