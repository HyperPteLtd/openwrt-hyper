module github.com/HyperPteLtd/openwrt-hyper

go 1.25.0

require (
	aead.dev/minisign v0.3.0
	github.com/klauspost/compress v1.17.9
	golang.org/x/sys v0.12.0
	google.golang.org/protobuf v1.36.11
)

require github.com/dlclark/regexp2 v1.12.0 // indirect

require (
	github.com/openweir/subconv v0.5.0
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/crypto v0.13.0 // indirect
)

ignore ./build
