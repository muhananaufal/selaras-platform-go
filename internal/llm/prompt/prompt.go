// Package prompt stores the prompt templates together with their versions.
//
// The version is not decoration. A stored result without its prompt version
// cannot be explained once the prompt changes: when an old report looks
// strange, there is no way to know whether the model answered like that or the
// template has been replaced since (F3-09).
package prompt

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/template"
)

// templates loads the template files into the binary.
//
// They are embedded, not read from disk at run time. Templates read from disk
// mean the same binary can behave differently depending on the files around
// it - and the version recorded on the result stops meaning anything.
//
//go:embed templates/*.tmpl
var templates embed.FS

// Template is one prompt template at one version.
type Template struct {
	// Name is the name of its use case, for example "personalization".
	Name string

	// Version goes up every time the content changes.
	Version int

	// Checksum is the SHA-256 of the template's content.
	//
	// It is what keeps the version from lying: a template changed without
	// bumping its version still looks different here, and the test that
	// compares it fails.
	Checksum string

	tmpl *template.Template
}

// ID is the marker stored with the result, for example "personalization@3".
func (t Template) ID() string {
	return t.Name + "@" + strconv.Itoa(t.Version)
}

// Render fills the template with its data.
func (t Template) Render(data any) (string, error) {
	var buf bytes.Buffer
	if err := t.tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering %s: %w", t.ID(), err)
	}

	out := strings.TrimSpace(buf.String())
	if out == "" {
		// A template that renders to empty text would waste one request to the
		// provider, and the answer would relate to nothing.
		return "", fmt.Errorf("%s rendered to nothing", t.ID())
	}
	return out, nil
}

// Library is every available template.
type Library struct {
	byName map[string]Template
}

// Load reads every embedded template.
//
// The file name determines the name and the version:
// "personalization.v1.tmpl". The version is in the file name, not inside the
// content, so bumping the version means creating a new file - and the old
// file stays to explain the results it produced.
func Load() (*Library, error) {
	entries, err := fs.Glob(templates, "templates/*.tmpl")
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("no prompt templates were embedded")
	}

	// Sorted so the highest version wins deterministically, rather than
	// depending on whatever order Glob happens to return.
	sort.Strings(entries)

	lib := &Library{byName: make(map[string]Template, len(entries))}
	for _, entry := range entries {
		name, version, err := parseName(path.Base(entry))
		if err != nil {
			return nil, err
		}

		raw, err := templates.ReadFile(entry)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", entry, err)
		}

		parsed, err := template.New(name).Option("missingkey=error").Parse(string(raw))
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", entry, err)
		}

		sum := sha256.Sum256(raw)
		candidate := Template{
			Name:     name,
			Version:  version,
			Checksum: hex.EncodeToString(sum[:]),
			tmpl:     parsed,
		}

		if existing, ok := lib.byName[name]; ok && existing.Version > version {
			// Old versions stay in the repo to explain old results, but the one used
			// is always the highest.
			continue
		}
		lib.byName[name] = candidate
	}
	return lib, nil
}

// Latest returns the highest version of a template.
func (l *Library) Latest(name string) (Template, error) {
	t, ok := l.byName[name]
	if !ok {
		return Template{}, fmt.Errorf("no prompt template named %q", name)
	}
	return t, nil
}

// Names returns the names of the available templates, sorted.
func (l *Library) Names() []string {
	out := make([]string, 0, len(l.byName))
	for name := range l.byName {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// parseName membaca "personalization.v3.tmpl" menjadi ("personalization", 3).
func parseName(base string) (name string, version int, err error) {
	trimmed := strings.TrimSuffix(base, ".tmpl")

	dot := strings.LastIndex(trimmed, ".v")
	if dot < 1 {
		return "", 0, fmt.Errorf("template %q is not named <name>.v<n>.tmpl", base)
	}

	version, err = strconv.Atoi(trimmed[dot+2:])
	if err != nil {
		return "", 0, fmt.Errorf("template %q has no readable version: %w", base, err)
	}
	if version < 1 {
		return "", 0, fmt.Errorf("template %q has version %d; versions start at 1", base, version)
	}
	return trimmed[:dot], version, nil
}
