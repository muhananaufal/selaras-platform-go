// Package partition memelihara tabel yang dipartisi menurut bulan.
//
// Dua pekerjaan, keduanya idempoten sehingga aman dijalankan sesering apa pun
// (F9-29):
//
//   - MEMBUAT partisi untuk bulan berjalan dan bulan berikutnya, supaya baris
//     baru tidak jatuh ke partisi DEFAULT. Bulan berikutnya dibuat sekarang,
//     bukan pada tanggal satu: pemelihara yang tidak sempat berjalan di
//     pergantian bulan tidak boleh membuat INSERT gagal.
//   - MELEPAS partisi yang seluruh rentangnya lebih tua dari retensi tabel.
//     Dilepas utuh (DETACH lalu DROP), bukan dihapus baris per baris: satu
//     perintah metadata, bukan jutaan tuple mati yang harus di-vacuum.
//
// Baris yang terlanjur berada di partisi DEFAULT - misalnya dari sebelum
// pemelihara pertama kali berjalan - dipangkas dengan DELETE menurut retensi.
// Itu satu-satunya jalur baris-per-baris, dan ia hanya menyentuh sisa
// transisi.
package partition

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Table adalah satu tabel terpartisi yang dipelihara.
type Table struct {
	Schema string
	Name   string

	// Column adalah kunci partisinya; created_at di seluruh tabel proyek ini.
	Column string

	// Retention adalah berapa lama baris disimpan. Nol berarti SELAMANYA:
	// partisi dibuat tetapi tidak pernah dilepas. Pesan pengguna memakai nol -
	// menghapusnya adalah keputusan produk, bukan pemeliharaan.
	Retention time.Duration

	// Prune adalah predikat tambahan untuk baris yang boleh dipangkas dari
	// partisi DEFAULT, misalnya "published_at IS NOT NULL" untuk outbox.
	// Kosong berarti seluruh baris yang lebih tua dari retensi.
	Prune string
}

// Report adalah yang terjadi pada satu jalankan.
type Report struct {
	Created []string
	Dropped []string
	Pruned  int64
	Skipped []string
}

// Maintainer menjalankan pemeliharaan atas daftar tabel.
type Maintainer struct {
	db  pg.Querier
	log *slog.Logger
}

func New(db pg.Querier, log *slog.Logger) (*Maintainer, error) {
	switch {
	case db == nil:
		return nil, errors.New("nil database")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Maintainer{db: db, log: log}, nil
}

var safeIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Run memelihara setiap tabel sekali, relatif terhadap now.
//
// now diberikan, bukan dibaca dari jam: test harus bisa memutar waktu, dan
// operator yang menjalankannya untuk "bulan depan" harus bisa mengatakannya.
func (m *Maintainer) Run(ctx context.Context, tables []Table, now time.Time) (Report, error) {
	var report Report
	for _, t := range tables {
		if err := t.validate(); err != nil {
			return report, err
		}
		if err := m.ensure(ctx, t, now, &report); err != nil {
			return report, fmt.Errorf("%s.%s: %w", t.Schema, t.Name, err)
		}
		if err := m.retire(ctx, t, now, &report); err != nil {
			return report, fmt.Errorf("%s.%s: %w", t.Schema, t.Name, err)
		}
	}
	return report, nil
}

func (t Table) validate() error {
	for _, s := range []string{t.Schema, t.Name, t.Column} {
		if !safeIdent.MatchString(s) {
			// Nama tabel masuk ke DDL lewat penggabungan string - tidak ada
			// parameter untuk pengenal - jadi bentuknya dibatasi ketat.
			return fmt.Errorf("identifier %q is not a plain lowercase identifier", s)
		}
	}
	return nil
}

// PartitionName menamai partisi satu bulan: <tabel>_y2026m09.
func PartitionName(table string, month time.Time) string {
	return fmt.Sprintf("%s_y%04dm%02d", table, month.Year(), int(month.Month()))
}

var partitionSuffix = regexp.MustCompile(`_y(\d{4})m(\d{2})$`)

// monthOf membaca bulan dari nama partisi; false bila bukan partisi bulanan
// (partisi DEFAULT, misalnya).
func monthOf(name string) (time.Time, bool) {
	match := partitionSuffix.FindStringSubmatch(name)
	if match == nil {
		return time.Time{}, false
	}
	// Regex di atas sudah menjamin keduanya angka; galat di sini mustahil,
	// tetapi diperiksa supaya nama yang aneh dilewati, bukan ditebak.
	year, err := strconv.Atoi(match[1])
	if err != nil {
		return time.Time{}, false
	}
	month, err := strconv.Atoi(match[2])
	if err != nil || month < 1 || month > 12 {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC), true
}

func startOfMonth(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// ensure membuat partisi bulan ini dan bulan depan bila belum ada.
func (m *Maintainer) ensure(ctx context.Context, t Table, now time.Time, report *Report) error {
	this := startOfMonth(now)
	for _, month := range []time.Time{this, this.AddDate(0, 1, 0)} {
		name := PartitionName(t.Name, month)
		exists, err := m.exists(ctx, t.Schema, name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		from := month.Format("2006-01-02")
		to := month.AddDate(0, 1, 0).Format("2006-01-02")

		// Partisi DEFAULT yang sudah memuat baris untuk bulan ini membuat
		// PostgreSQL menolak CREATE: baris itu akan melanggar batasan default
		// yang baru. Diperiksa LEBIH DULU, bukan ditangkap dari galatnya:
		// galat membatalkan transaksi yang sedang berjalan, dan dry-run
		// berjalan di dalam satu transaksi. Bulan itu dilewati dan dicatat -
		// baris di default dipangkas menurut retensi, dan bulan berikutnya
		// akan punya partisinya sendiri karena belum ada satu pun barisnya.
		crowded, err := m.defaultHasRows(ctx, t, from, to)
		if err != nil {
			return err
		}
		if crowded {
			m.log.WarnContext(ctx, "a monthly partition was not created; the default partition already holds rows for that month",
				"table", t.Schema+"."+t.Name, "partition", name)
			report.Skipped = append(report.Skipped, t.Schema+"."+name)
			continue
		}

		ddl := fmt.Sprintf(`CREATE TABLE %s.%s PARTITION OF %s.%s FOR VALUES FROM ('%s') TO ('%s')`,
			t.Schema, name, t.Schema, t.Name, from, to)
		if _, err := m.db.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("creating %s: %w", name, err)
		}

		// Pemiliknya disamakan dengan tabel induk. Pemelihara berjalan dengan
		// peran admin yang menjangkau semua skema; tanpa ini, partisi baru
		// menjadi milik admin dan peran service - pemilik tabel induknya -
		// tidak bisa mengubah atau membuangnya (ADR-006: satu peran per
		// skema, dan skemanya miliknya).
		owner, err := m.ownerOf(ctx, t.Schema, t.Name)
		if err != nil {
			return err
		}
		if _, err := m.db.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s.%s OWNER TO %s`, t.Schema, name, owner)); err != nil {
			return fmt.Errorf("handing %s to %s: %w", name, owner, err)
		}
		report.Created = append(report.Created, t.Schema+"."+name)
	}
	return nil
}

// retire melepas partisi yang seluruh rentangnya lebih tua dari retensi, dan
// memangkas sisa di partisi DEFAULT.
func (m *Maintainer) retire(ctx context.Context, t Table, now time.Time, report *Report) error {
	if t.Retention <= 0 {
		return nil
	}
	cutoff := now.UTC().Add(-t.Retention)

	rows, err := m.db.Query(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = $1 AND p.relname = $2`, t.Schema, t.Name)
	if err != nil {
		return fmt.Errorf("listing partitions: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, name := range names {
		month, ok := monthOf(name)
		if !ok {
			continue
		}
		// Seluruh rentang harus lebih tua dari cutoff: akhir bulan (eksklusif)
		// sebelum atau sama dengan cutoff.
		if month.AddDate(0, 1, 0).After(cutoff) {
			continue
		}
		// DETACH dulu, baru DROP: bila DROP gagal, partisinya sudah keluar
		// dari tabel dan bisa diperiksa sebagai tabel biasa.
		if _, err := m.db.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s.%s DETACH PARTITION %s.%s`,
			t.Schema, t.Name, t.Schema, name)); err != nil {
			return fmt.Errorf("detaching %s: %w", name, err)
		}
		if _, err := m.db.Exec(ctx, fmt.Sprintf(`DROP TABLE %s.%s`, t.Schema, name)); err != nil {
			return fmt.Errorf("dropping %s: %w", name, err)
		}
		report.Dropped = append(report.Dropped, t.Schema+"."+name)
	}

	// Sisa di partisi DEFAULT: satu-satunya jalur baris-per-baris.
	where := fmt.Sprintf(`%s < $1`, t.Column)
	if t.Prune != "" {
		where += " AND (" + t.Prune + ")"
	}
	tag, err := m.db.Exec(ctx, fmt.Sprintf(`DELETE FROM %s.%s_default WHERE %s`, t.Schema, t.Name, where), cutoff)
	if err != nil {
		return fmt.Errorf("pruning the default partition: %w", err)
	}
	report.Pruned += tag.RowsAffected()
	return nil
}

// ownerOf membaca pemilik tabel induk; namanya dipakai dalam DDL, jadi
// bentuknya dibatasi seketat nama tabel.
func (m *Maintainer) ownerOf(ctx context.Context, schema, name string) (string, error) {
	var owner string
	err := m.db.QueryRow(ctx, `
		SELECT pg_get_userbyid(c.relowner)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`, schema, name).Scan(&owner)
	if err != nil {
		return "", fmt.Errorf("reading the owner of %s.%s: %w", schema, name, err)
	}
	if !safeIdent.MatchString(owner) {
		return "", fmt.Errorf("owner %q of %s.%s is not a plain identifier", owner, schema, name)
	}
	return owner, nil
}

// defaultHasRows menjawab apakah partisi DEFAULT memuat baris dalam rentang
// [from, to) - keadaan yang membuat partisi bulan itu tidak bisa dibuat.
func (m *Maintainer) defaultHasRows(ctx context.Context, t Table, from, to string) (bool, error) {
	var found bool
	err := m.db.QueryRow(ctx, fmt.Sprintf(
		`SELECT EXISTS (SELECT 1 FROM %s.%s_default WHERE %s >= $1 AND %s < $2)`,
		t.Schema, t.Name, t.Column, t.Column), from, to).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("inspecting the default partition: %w", err)
	}
	return found, nil
}

func (m *Maintainer) exists(ctx context.Context, schema, name string) (bool, error) {
	var found bool
	err := m.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2)`, schema, name).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("checking %s.%s: %w", schema, name, err)
	}
	return found, nil
}
