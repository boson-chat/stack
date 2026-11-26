module github.com/boson-chat/stack/images/setup

go 1.26.0

require (
	golang.org/x/crypto v0.59.0
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/boson-chat/stack/pkg/probe v0.0.0

replace github.com/boson-chat/stack/pkg/probe => ../../pkg/probe
