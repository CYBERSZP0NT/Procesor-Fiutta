package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SetUserValue sets one scalar at keys (e.g. ["agentic", "mode"]) in the
// user config file at path, creating the file and any missing mappings.
// Everything else in the file — other keys, order, comments — is kept,
// unlike re-marshalling the merged Config.
func SetUserValue(path string, keys []string, value string) error {
	return setUserNode(path, keys, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// SetUserAny is SetUserValue for any YAML-encodable value (lists, maps,
// numbers, bools). A nil value removes nothing; it writes null.
func SetUserAny(path string, keys []string, value any) error {
	var n yaml.Node
	if err := n.Encode(value); err != nil {
		return fmt.Errorf("config: encode %v: %w", keys, err)
	}
	return setUserNode(path, keys, &n)
}

func setUserNode(path string, keys []string, value *yaml.Node) error {
	if len(keys) == 0 {
		return fmt.Errorf("config: no key given")
	}
	var doc yaml.Node
	data, err := os.ReadFile(path)
	switch {
	case err == nil && len(bytes.TrimSpace(data)) > 0:
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("config: parse %s: %w", path, err)
		}
	case err == nil || os.IsNotExist(err):
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	default:
		return err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("config: %s: top level is not a mapping", path)
	}

	node := doc.Content[0]
	for i, k := range keys {
		last := i == len(keys)-1
		var child *yaml.Node
		for j := 0; j+1 < len(node.Content); j += 2 {
			if node.Content[j].Value == k {
				child = node.Content[j+1]
				break
			}
		}
		if child == nil {
			child = &yaml.Node{Kind: yaml.MappingNode}
			if last {
				child = &yaml.Node{Kind: yaml.ScalarNode}
			}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, child)
		}
		if last {
			*child = *value
			break
		}
		if child.Kind != yaml.MappingNode {
			// e.g. "agentic:" with no value — make it a mapping.
			child.Kind, child.Tag, child.Value, child.Content = yaml.MappingNode, "", "", nil
		}
		node = child
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// UserPath returns the user config file path that Load reads (explicit,
// else $FREELLM_CONFIG, else ~/.config/freellm/config.yaml), whether or
// not it exists yet.
func UserPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv("FREELLM_CONFIG"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "freellm", "config.yaml")
}
