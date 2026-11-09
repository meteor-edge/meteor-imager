package bootfs

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/meteor-edge/meteor-imager/internal/profile"
)

// FirstbootURL is MeteorCloud's first-boot installer, as referenced by
// MeteorCloud's src/device-plane/agent/packaging/firstboot/cloud-init-user-data.yaml.
// It reads METEOR_DOMAIN and METEOR_API_KEY from .meteorenv on the boot partition.
const FirstbootURL = "https://raw.githubusercontent.com/meteor-edge/meteor-cloud/master/src/device-plane/agent/packaging/firstboot/install-meteorcli-firstboot.sh"

// FirstbootInstallCommand runs the installer from cloud-init. Every interface
// is optional, so cloud-init may get here before Wi-Fi has joined: the
// download is retried for up to five minutes.
const FirstbootInstallCommand = `for i in $(seq 1 30); do curl -fsSL ` + FirstbootURL +
	` -o /run/meteor-firstboot.sh && exec bash /run/meteor-firstboot.sh --now; sleep 10; done; ` +
	`echo "meteor-imager: could not download MeteorCloud first boot" >&2; exit 1`

const firstbootMarker = "install-meteorcli-firstboot.sh"

// MergeUserData adds the profile's settings to an existing cloud-config, keeping
// everything already there (an organization's golden image may carry its own).
func MergeUserData(existing []byte, p *profile.FlashProfile) ([]byte, error) {
	doc, root, err := parseMapping(existing, "user-data")
	if err != nil {
		return nil, err
	}
	setDefault(root, "manage_resolv_conf", boolNode(false))
	if p.Hostname != nil {
		set(root, "hostname", quoted(*p.Hostname))
		set(root, "manage_etc_hosts", boolNode(true))
	}
	if p.Timezone != nil {
		set(root, "timezone", quoted(*p.Timezone))
	}
	if p.Enrollment != nil {
		packages := sequence(root, "packages")
		for _, pkg := range []string{"curl", "ca-certificates"} {
			if !sequenceHas(packages, pkg) {
				packages.Content = append(packages.Content, plain(pkg))
			}
		}
		runcmd := sequence(root, "runcmd")
		if !sequenceContains(runcmd, firstbootMarker) {
			runcmd.Content = append(runcmd.Content, literal(FirstbootInstallCommand))
		}
	}
	return encode(doc, "#cloud-config")
}

// NetworkConfig returns a netplan v2 network-config joining the profile's Wi-Fi,
// in the shape Raspberry Pi Imager writes: eth0 keeps DHCP (this file replaces
// the distro default), wlan0 gets the regulatory domain and one access point.
// Other interfaces already in the file are kept.
func NetworkConfig(existing []byte, w *profile.WiFi) ([]byte, error) {
	doc, root, err := parseMapping(existing, "network-config")
	if err != nil {
		return nil, err
	}
	network := mapping(root, "network")
	set(network, "version", plain("2"))
	ethernets := mapping(network, "ethernets")
	if len(ethernets.Content) == 0 {
		set(ethernets, "eth0", mappingOf(
			"dhcp4", boolNode(true), "dhcp6", boolNode(true), "optional", boolNode(true),
		))
	}
	ap := mappingOf("password", quoted(w.PSK))
	if w.Hidden {
		ap = mappingOf("hidden", boolNode(true), "password", quoted(w.PSK))
	}
	accessPoints := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{quoted(w.SSID), ap}}
	wlan := mappingOf(
		"dhcp4", boolNode(true),
		"regulatory-domain", quoted(w.Country),
		"access-points", accessPoints,
		"optional", boolNode(true),
	)
	set(mapping(network, "wifis"), "wlan0", wlan)
	return encode(doc, "# Written by Meteor Imager (netplan v2, read by cloud-init on first boot).")
}

// parseMapping parses a YAML document whose root must be a mapping (or empty).
func parseMapping(data []byte, name string) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("%s on the boot partition is not valid YAML: %w", name, err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, nil, fmt.Errorf("%s on the boot partition has an unexpected layout", name)
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		root.Kind, root.Tag, root.Value = yaml.MappingNode, "", ""
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil, errors.New(name + " on the boot partition is not a YAML mapping")
	}
	return &doc, root, nil
}

func encode(doc *yaml.Node, header string) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	body := buf.String()
	// cloud-init only reads user-data whose first line is exactly "#cloud-config".
	lines := strings.SplitN(body, "\n", 2)
	if strings.TrimSpace(lines[0]) == strings.TrimSpace(header) {
		return []byte(body), nil
	}
	body = strings.TrimPrefix(body, "#cloud-config\n")
	return []byte(header + "\n" + body), nil
}

func find(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func set(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// Keep comments attached to the old value.
			value.HeadComment, value.LineComment = m.Content[i+1].HeadComment, m.Content[i+1].LineComment
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, plain(key), value)
}

func setDefault(m *yaml.Node, key string, value *yaml.Node) {
	if find(m, key) == nil {
		m.Content = append(m.Content, plain(key), value)
	}
}

func mapping(m *yaml.Node, key string) *yaml.Node {
	if v := find(m, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode}
	set(m, key, v)
	return v
}

func sequence(m *yaml.Node, key string) *yaml.Node {
	if v := find(m, key); v != nil && v.Kind == yaml.SequenceNode {
		return v
	}
	v := &yaml.Node{Kind: yaml.SequenceNode}
	set(m, key, v)
	return v
}

func sequenceHas(seq *yaml.Node, value string) bool {
	for _, item := range seq.Content {
		if item.Kind == yaml.ScalarNode && item.Value == value {
			return true
		}
	}
	return false
}

func sequenceContains(seq *yaml.Node, substr string) bool {
	for _, item := range seq.Content {
		if item.Kind == yaml.ScalarNode && strings.Contains(item.Value, substr) {
			return true
		}
		if item.Kind == yaml.SequenceNode && sequenceContains(item, substr) {
			return true
		}
	}
	return false
}

func mappingOf(kv ...any) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for i := 0; i+1 < len(kv); i += 2 {
		m.Content = append(m.Content, plain(kv[i].(string)), kv[i+1].(*yaml.Node))
	}
	return m
}

func plain(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

// quoted always double-quotes, so SSIDs, passwords and names stay strings
// whatever they contain ("yes", "0123", ": ", "#").
func quoted(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}

func literal(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

func boolNode(v bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(v)}
}
