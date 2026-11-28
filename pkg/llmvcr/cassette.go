package llmvcr

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Cassette represents a go-vcr v3 cassette file.
type Cassette struct {
	Interactions []Interaction `yaml:"interactions"`
	Version      int           `yaml:"version"`
}

// Interaction represents a single recorded HTTP request/response cycle.
type Interaction struct {
	ID       int             `yaml:"id"`
	Request  InteractionReq  `yaml:"request"`
	Response InteractionResp `yaml:"response"`
}

// InteractionReq is the recorded HTTP request.
type InteractionReq struct {
	Body             string              `yaml:"body"`
	ContentLength    int                 `yaml:"content_length"`
	Form             map[string][]string `yaml:"form"`
	Headers          map[string][]string `yaml:"headers"`
	Host             string              `yaml:"host"`
	Method           string              `yaml:"method"`
	Proto            string              `yaml:"proto"`
	ProtoMajor       int                 `yaml:"proto_major"`
	ProtoMinor       int                 `yaml:"proto_minor"`
	RemoteAddr       string              `yaml:"remote_addr"`
	RequestURI       string              `yaml:"request_uri"`
	Trailer          map[string][]string `yaml:"trailer"`
	TransferEncoding []string            `yaml:"transfer_encoding"`
	URL              string              `yaml:"url"`
}

// InteractionResp is the recorded HTTP response.
type InteractionResp struct {
	Body             string              `yaml:"body"`
	Code             int                 `yaml:"code"`
	ContentLength    int                 `yaml:"content_length"`
	Duration         string              `yaml:"duration"` // go-vcr stores duration as string, e.g. "653ms"
	Headers          map[string][]string `yaml:"headers"`
	Proto            string              `yaml:"proto"`
	ProtoMajor       int                 `yaml:"proto_major"`
	ProtoMinor       int                 `yaml:"proto_minor"`
	Status           string              `yaml:"status"`
	Trailer          map[string][]string `yaml:"trailer"`
	TransferEncoding []string            `yaml:"transfer_encoding"`
	Uncompressed     bool                `yaml:"uncompressed"`
}

// LoadCassette reads and parses a YAML cassette file.
func LoadCassette(path string) (*Cassette, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}
	var c Cassette
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("unmarshaling yaml: %w", err)
	}
	return &c, nil
}

// SaveCassette writes a cassette to a YAML file.
func SaveCassette(path string, c *Cassette) error {
	out, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling yaml: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}
	return nil
}
