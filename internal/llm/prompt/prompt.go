// Package prompt menyimpan templat prompt beserta versinya.
//
// Versinya bukan hiasan. Hasil yang tersimpan tanpa versi promptnya tidak bisa
// dijelaskan setelah promptnya berubah: saat sebuah laporan lama terlihat
// aneh, tidak ada cara mengetahui apakah modelnya yang menjawab begitu atau
// templatnya yang sudah diganti sejak itu (F3-09).
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

// templates memuat berkas templat ke dalam binernya.
//
// Ia di-embed, bukan dibaca dari disk saat berjalan. Templat yang dibaca dari
// disk berarti biner yang sama bisa berperilaku berbeda tergantung berkas di
// sekitarnya - dan versi yang dicatat di hasil berhenti berarti apa-apa.
//
//go:embed templates/*.tmpl
var templates embed.FS

// Template adalah satu templat prompt pada satu versi.
type Template struct {
	// Name adalah nama use case-nya, misalnya "personalization".
	Name string

	// Version naik setiap kali isinya berubah.
	Version int

	// Checksum adalah SHA-256 isi templatnya.
	//
	// Ia yang membuat versi tidak bisa berbohong: templat yang diubah tanpa
	// menaikkan versinya akan tetap terlihat berbeda di sini, dan test yang
	// membandingkannya akan gagal.
	Checksum string

	tmpl *template.Template
}

// ID adalah penanda yang disimpan bersama hasilnya, misalnya
// "personalization@3".
func (t Template) ID() string {
	return t.Name + "@" + strconv.Itoa(t.Version)
}

// Render mengisi templat dengan datanya.
func (t Template) Render(data any) (string, error) {
	var buf bytes.Buffer
	if err := t.tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering %s: %w", t.ID(), err)
	}

	out := strings.TrimSpace(buf.String())
	if out == "" {
		// Templat yang menghasilkan teks kosong akan membuang satu permintaan
		// ke penyedia, dan jawabannya tidak akan berhubungan dengan apa pun.
		return "", fmt.Errorf("%s rendered to nothing", t.ID())
	}
	return out, nil
}

// Library adalah seluruh templat yang tersedia.
type Library struct {
	byName map[string]Template
}

// Load membaca seluruh templat yang ter-embed.
//
// Nama berkasnya menentukan nama dan versinya: "personalization.v1.tmpl".
// Versinya ada di nama berkas, bukan di dalam isinya, supaya menaikkan versi
// berarti membuat berkas baru - dan berkas lama tetap ada untuk menjelaskan
// hasil yang dihasilkannya.
func Load() (*Library, error) {
	entries, err := fs.Glob(templates, "templates/*.tmpl")
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("no prompt templates were embedded")
	}

	// Diurutkan supaya versi tertinggi yang menang secara deterministik, bukan
	// bergantung pada urutan yang kebetulan dikembalikan Glob.
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
			// Versi lama tetap ada di repo untuk menjelaskan hasil lama, tetapi
			// yang dipakai selalu yang tertinggi.
			continue
		}
		lib.byName[name] = candidate
	}
	return lib, nil
}

// Latest mengembalikan versi tertinggi sebuah templat.
func (l *Library) Latest(name string) (Template, error) {
	t, ok := l.byName[name]
	if !ok {
		return Template{}, fmt.Errorf("no prompt template named %q", name)
	}
	return t, nil
}

// Names mengembalikan nama templat yang tersedia, terurut.
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
