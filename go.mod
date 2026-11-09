module github.com/meteor-edge/meteor-imager

go 1.24.0

require (
	github.com/godbus/dbus/v5 v5.1.0
	github.com/soypat/fat v0.2.1
	github.com/ulikunitz/xz v0.5.15
	golang.org/x/sys v0.35.0
	gopkg.in/yaml.v3 v3.0.1
)

require golang.org/x/term v0.34.0

replace github.com/soypat/fat => ./third_party/soypat-fat
