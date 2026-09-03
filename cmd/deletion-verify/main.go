// Command deletion-verify membuktikan tidak ada sisa data setelah akun dihapus.
//
// Ia BUKAN bagian dari saga. Saga sudah menyatakan dirinya selesai lewat enam
// konfirmasi, dan perkakas ini menanyakan hal yang berbeda: apakah pernyataan
// itu benar. Keduanya harus terpisah - verifikasi yang memakai jalur yang sama
// dengan yang diverifikasi hanya mengulang keyakinan yang sama.
//
// Setiap skema ditanyai dengan PERAN LOGIN-nya sendiri, bukan dengan superuser.
// Menanyainya sebagai superuser akan menemukan baris yang tidak bisa dilihat
// service-nya sendiri, dan itu menjawab pertanyaan yang tidak sedang diajukan.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// probe adalah satu pertanyaan: berapa baris milik pengguna ini di tabel ini.
type probe struct {
	schema string
	table  string

	// query memakai $1 untuk kuncinya. Kuncinya BERBEDA per unit - sebagian
	// menyimpan user_id, sebagian user_profile_id - dan itu bukan
	// ketidakkonsistenan yang perlu diseragamkan di sini: yang penting adalah
	// menanyakan dengan kunci yang benar-benar dipakai tabelnya.
	query string

	// byProfile menandai probe yang memakai id profil, bukan id pengguna.
	byProfile bool
}

// Nama skema sebagai konstanta: daftar probe menyebut sebagiannya beberapa
// kali, dan salah ketik di salah satunya akan menghasilkan koneksi ke skema
// yang tidak ada - kegagalan yang terbaca sebagai masalah jaringan.
const (
	schemaIdentity   = "identity"
	schemaProfile    = "profile"
	schemaAssessment = "assessment"
	schemaCoaching   = "coaching"
	schemaChat       = "chat"
	schemaNutrition  = "nutrition"
	schemaDashboard  = "dashboard"
)

// probes menyebutkan SETIAP tabel yang bisa memuat data pengguna.
//
// Daftar ini ditulis tangan dengan sengaja. Menurunkannya otomatis dari
// information_schema akan ikut membawa tabel outbox, idempotensi, dan migrasi -
// dan yang lebih buruk, ia akan terlihat lengkap tanpa seorang pun pernah
// memutuskan tabel mana yang memuat data pribadi.
//
// Tabel yang isinya menggantung lewat ON DELETE CASCADE ikut disebut. Cascade
// memang menghapusnya, tetapi verifikasi yang hanya memeriksa induknya
// membuktikan cascade-nya berjalan hanya kalau kita sudah percaya ia berjalan.
var probes = []probe{
	{schema: schemaIdentity, table: "users", query: `SELECT count(*) FROM users WHERE id = $1`},

	{schema: schemaProfile, table: "user_profiles", query: `SELECT count(*) FROM user_profiles WHERE user_id = $1`},

	{schema: schemaAssessment, table: "risk_assessments", byProfile: true,
		query: `SELECT count(*) FROM risk_assessments WHERE user_profile_id = $1`},
	{schema: schemaAssessment, table: "profile_snapshots",
		query: `SELECT count(*) FROM profile_snapshots WHERE user_id = $1`},

	{schema: schemaCoaching, table: "coaching_programs",
		query: `SELECT count(*) FROM coaching_programs WHERE user_id = $1`},
	{schema: schemaCoaching, table: "coaching_weeks",
		query: `SELECT count(*) FROM coaching_weeks w
		        JOIN coaching_programs p ON p.id = w.coaching_program_id
		        WHERE p.user_id = $1`},
	{schema: schemaCoaching, table: "coaching_threads",
		query: `SELECT count(*) FROM coaching_threads t
		        JOIN coaching_programs p ON p.id = t.coaching_program_id
		        WHERE p.user_id = $1`},

	{schema: schemaChat, table: "conversations",
		query: `SELECT count(*) FROM conversations WHERE user_id = $1`},
	{schema: schemaChat, table: "chat_messages",
		query: `SELECT count(*) FROM chat_messages m
		        JOIN conversations c ON c.id = m.conversation_id
		        WHERE c.user_id = $1`},

	{schema: schemaNutrition, table: "culinary_preferences",
		query: `SELECT count(*) FROM culinary_preferences WHERE user_id = $1`},
	{schema: schemaNutrition, table: "daily_meal_guides",
		query: `SELECT count(*) FROM daily_meal_guides WHERE user_id = $1`},
	{schema: schemaNutrition, table: "user_languages",
		query: `SELECT count(*) FROM user_languages WHERE user_id = $1`},

	{schema: schemaDashboard, table: "dashboards",
		query: `SELECT count(*) FROM dashboards WHERE user_id = $1`},
	{schema: schemaDashboard, table: "dashboard_assessments",
		query: `SELECT count(*) FROM dashboard_assessments WHERE user_id = $1`},
}

func main() {
	leftovers, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "verification failed:", err)
		os.Exit(2)
	}

	// Kode 1 saat ada sisa, bukan 0 dengan peringatan. Verifikasi yang keluar
	// dengan sukses sambil melaporkan masalah akan lolos di pipeline mana pun.
	if leftovers > 0 {
		os.Exit(1)
	}
}

func run() (int, error) {
	var (
		userID    = flag.String("user-id", "", "the deleted user's id")
		profileID = flag.String("profile-id", "", "the deleted user's profile id, if it had one")
		dsnPrefix = flag.String("dsn", os.Getenv("VERIFY_DSN"),
			"postgres dsn template; {schema} and {password} are replaced per schema")
	)
	flag.Parse()

	if *userID == "" {
		return 0, errors.New("-user-id is required")
	}
	if _, err := uuid.Parse(*userID); err != nil {
		return 0, fmt.Errorf("-user-id %q is not a uuid", *userID)
	}
	if *profileID != "" {
		if _, err := uuid.Parse(*profileID); err != nil {
			return 0, fmt.Errorf("-profile-id %q is not a uuid", *profileID)
		}
	}
	if *dsnPrefix == "" {
		return 0, errors.New("no dsn: pass -dsn or set VERIFY_DSN")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Probe dikelompokkan per skema supaya tiap koneksi dibuka sekali.
	bySchema := map[string][]probe{}
	for _, p := range probes {
		bySchema[p.schema] = append(bySchema[p.schema], p)
	}

	schemas := make([]string, 0, len(bySchema))
	for name := range bySchema {
		schemas = append(schemas, name)
	}
	sort.Strings(schemas)

	var total int
	for _, schema := range schemas {
		found, err := checkSchema(ctx, schema, bySchema[schema], *dsnPrefix, *userID, *profileID)
		if err != nil {
			return total, err
		}
		total += found
	}

	if total == 0 {
		fmt.Println("no rows left anywhere")
		return 0, nil
	}
	fmt.Printf("\n%d rows are still there\n", total)
	return total, nil
}

func checkSchema(
	ctx context.Context, schema string, list []probe,
	dsnPrefix, userID, profileID string,
) (int, error) {
	dsn, err := dsnFor(dsnPrefix, schema)
	if err != nil {
		return 0, err
	}

	pool, err := pg.Open(ctx, pg.DefaultConfig(dsn))
	if err != nil {
		return 0, fmt.Errorf("connecting to schema %s: %w", schema, err)
	}
	defer pool.Close()

	var found int
	for _, p := range list {
		key := userID
		if p.byProfile {
			if profileID == "" {
				// Profil yang tidak pernah ada berarti tidak ada baris yang
				// bisa berkunci padanya. Melewatinya jauh lebih jujur daripada
				// menanyakannya dengan string kosong, yang akan ditolak
				// Postgres dan terbaca sebagai kegagalan verifikasi.
				fmt.Printf("  %-12s %-24s skipped (no profile id was given)\n", schema, p.table)
				continue
			}
			key = profileID
		}

		var n int
		if err := pool.QueryRow(ctx, p.query, key).Scan(&n); err != nil {
			return found, fmt.Errorf("querying %s.%s: %w", schema, p.table, err)
		}

		mark := "clean"
		if n > 0 {
			mark = "LEFTOVER"
			found += n
		}
		fmt.Printf("  %-12s %-24s %5d  %s\n", schema, p.table, n, mark)
	}
	return found, nil
}

// dsnFor menyisipkan peran login, kata sandinya, dan search_path satu skema.
//
// Peran per skema, bukan satu superuser: menanyainya sebagai superuser akan
// menemukan baris yang tidak bisa dilihat service-nya sendiri, dan itu
// menjawab pertanyaan yang tidak sedang diajukan.
//
// Kata sandinya dibaca dari SVC_<SKEMA>_PASSWORD, konvensi yang sama dengan
// seluruh platform - dan TANPA nilai bawaan (ADR-016). Verifikasi yang jatuh ke
// kata sandi tebakan hanya bisa gagal menyambung, dan kegagalan itu akan
// terbaca sebagai "tidak ada sisa data".
func dsnFor(prefix, schema string) (string, error) {
	envVar := "SVC_" + strings.ToUpper(schema) + "_PASSWORD"
	password := os.Getenv(envVar)
	if password == "" {
		return "", fmt.Errorf("%s is not set; verification cannot reach schema %s", envVar, schema)
	}

	dsn := strings.ReplaceAll(prefix, "{schema}", schema)
	dsn = strings.ReplaceAll(dsn, "{password}", password)

	if strings.Contains(dsn, "?") {
		return dsn + "&search_path=" + schema, nil
	}
	return dsn + "?search_path=" + schema, nil
}
