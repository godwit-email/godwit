module github.com/godwit-email/godwit

go 1.27.3

require (
	github.com/Calcium-Ion/moejs v0.1.0-alpha.10
	github.com/ebitengine/purego v0.11.2
	github.com/egoist/mygo v0.7.2
	github.com/emersion/go-imap/v2 v2.0.0-beta.8
	github.com/emersion/go-message v0.18.2
	github.com/emersion/go-sasl v0.0.0-20241020182733-b788ff22d5a6
	github.com/emersion/go-smtp v0.25.0
	github.com/evanw/esbuild v0.28.2
	github.com/fyne-io/image v0.1.1
	github.com/goccy/go-yaml v1.19.2
	github.com/godwit-email/markitdown-go v0.0.0
	github.com/google/jsonschema-go v0.4.3
	github.com/gorilla/css v1.0.1
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/pelletier/go-toml/v2 v2.2.4
	github.com/stretchr/testify v1.12.1
	github.com/tdewolff/parse/v2 v2.8.20
	github.com/zalando/go-keyring v0.2.8
	golang.org/x/image v0.47.0
	golang.org/x/net v0.61.0
	golang.org/x/oauth2 v0.37.0
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.43.0
)

require (
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.2.0 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/emersion/go-msgauth v0.7.0 // indirect
	github.com/extrame/ole2 v0.0.0-20160812065207-d69429661ad7 // indirect
	github.com/extrame/xls v0.0.1 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/jsummers/gobmp v0.0.0-20230614200233-a9de23ed2e25 // indirect
	github.com/ledongthuc/pdf v0.0.0-20260907135840-6c8c28e0e8a0 // indirect
	github.com/richardlehane/mscfb v1.0.4 // indirect
	github.com/richardlehane/msoleps v1.0.4 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/tiendc/go-deepcopy v1.6.0 // indirect
	github.com/xuri/efp v0.0.1 // indirect
	github.com/xuri/excelize/v2 v2.9.1 // indirect
	github.com/xuri/nfp v0.0.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

ignore ./web/node_modules

tool github.com/egoist/mygo/cmd/mygo

replace github.com/emersion/go-imap/v2 => github.com/godwit-email/go-imap/v2 v2.0.0-beta.8.godwit.1
