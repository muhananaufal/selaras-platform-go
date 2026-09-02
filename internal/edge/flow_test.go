package edge_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func uniqueEmail() string {
	return fmt.Sprintf("edge-%d@user.co", time.Now().UnixNano())
}

// F1-18. Alur lengkap lewat HTTP sungguhan: daftar, pakai token, keluar,
// lalu buktikan token yang sama sudah tidak berlaku.
func TestTheWholeAuthFlowOverHTTP(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()

	token := s.registerUser(t, email)

	status, body := s.do(t, http.MethodGet, "/api/v1/me", token, nil)
	if status != http.StatusOK {
		t.Fatalf("me status = %d; want 200 (%v)", status, body)
	}

	status, _ = s.do(t, http.MethodPost, "/api/v1/logout", token, nil)
	if status != http.StatusOK {
		t.Fatalf("logout status = %d; want 200", status)
	}

	// Menutup separuh ADR-012: logout yang tidak benar-benar mencabut adalah
	// tipuan, dan tokennya tetap berlaku sampai kedaluwarsa sendiri.
	status, _ = s.do(t, http.MethodGet, "/api/v1/me", token, nil)
	if status != http.StatusUnauthorized {
		t.Errorf("the token still works after logout: status = %d", status)
	}
}

// D1: login berhasil mengakhiri sesi sebelumnya.
func TestLoggingInAgainEndsTheEarlierSession(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()

	first := s.registerUser(t, email)

	status, body := s.do(t, http.MethodPost, "/api/v1/login", "", map[string]string{
		"email": email, "password": "a-long-enough-password",
	})
	if status != http.StatusOK {
		t.Fatalf("login status = %d; want 200 (%v)", status, body)
	}
	second, _ := body["access_token"].(string)

	if status, _ := s.do(t, http.MethodGet, "/api/v1/me", second, nil); status != http.StatusOK {
		t.Errorf("the new token does not work: status = %d", status)
	}
	if status, _ := s.do(t, http.MethodGet, "/api/v1/me", first, nil); status != http.StatusUnauthorized {
		t.Errorf("the earlier token still works: status = %d", status)
	}
}

// F1-32. Profil dibuat saat pendaftaran, dibaca kosong, diisi, dibaca lagi.
func TestTheProfileFlowOverHTTP(t *testing.T) {
	s := newStack(t)
	token := s.registerUser(t, uniqueEmail())

	status, body := s.do(t, http.MethodGet, "/api/v1/profile", token, nil)
	if status != http.StatusOK {
		t.Fatalf("profile status = %d; want 200 (%v)", status, body)
	}

	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %v; want an object", body["data"])
	}

	// Menutup B6 di ujung yang dilihat klien. Sistem lama mengirim tanggal
	// hari ini dan umur 0 di sini.
	for _, field := range []string{"first_name", "last_name", "date_of_birth", "age", "sex", "country_of_residence"} {
		if value, present := data[field]; !present || value != nil {
			t.Errorf("%s = %v; want null on an untouched profile", field, value)
		}
	}
	if data["language"] != "id" {
		t.Errorf("language = %v; want the default", data["language"])
	}

	status, body = s.do(t, http.MethodPatch, "/api/v1/profile", token, map[string]any{
		"first_name":    "Sri",
		"date_of_birth": "1990-05-17",
		"sex":           "female",
	})
	if status != http.StatusOK {
		t.Fatalf("patch status = %d; want 200 (%v)", status, body)
	}

	status, body = s.do(t, http.MethodGet, "/api/v1/profile", token, nil)
	if status != http.StatusOK {
		t.Fatalf("profile status = %d; want 200", status)
	}
	data, _ = body["data"].(map[string]any)

	if data["first_name"] != "Sri" {
		t.Errorf("first name = %v; want Sri", data["first_name"])
	}
	// ISO-8601, bukan d/m/Y. Perubahan yang dinyatakan sebagai temuan B13.
	if data["date_of_birth"] != "1990-05-17" {
		t.Errorf("date of birth = %v; want ISO-8601", data["date_of_birth"])
	}
	if data["age"] == nil {
		t.Error("age is null although a birth date was set")
	}
	if data["last_name"] != nil {
		t.Errorf("last name = %v; a field that was never sent should stay null", data["last_name"])
	}
}

// Setiap rute terproteksi harus benar-benar terproteksi. Rute yang lupa
// dipasangi middleware tidak menghasilkan galat apa pun - hanya endpoint
// terbuka yang tidak ada yang menyadarinya.
func TestEveryProtectedRouteRefusesAnAnonymousCaller(t *testing.T) {
	s := newStack(t)

	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/logout"},
		{http.MethodGet, "/api/v1/me"},
		{http.MethodGet, "/api/v1/profile"},
		{http.MethodPatch, "/api/v1/profile"},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			status, body := s.do(t, c.method, c.path, "", nil)
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d; want 401", status)
			}
			if body["code"] != "UNAUTHENTICATED" {
				t.Errorf("code = %v; want UNAUTHENTICATED", body["code"])
			}
		})
	}
}

func TestMalformedTokensAreRefusedTheSameWay(t *testing.T) {
	s := newStack(t)

	for name, header := range map[string]string{
		"garbage":      "not-a-token",
		"empty bearer": "",
		"wrong scheme": "Basic abcdef",
		"three parts":  "a.b.c",
	} {
		t.Run(name, func(t *testing.T) {
			status, _ := s.do(t, http.MethodGet, "/api/v1/me", header, nil)
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d; want 401", status)
			}
		})
	}
}

// ADR-020 gagal-tertutup, dilihat dari luar. Pemeriksa yang tidak bisa
// menjawab menghasilkan 503, bukan 401: kliennya tidak melakukan kesalahan,
// dan 401 akan membuat aplikasi mengeluarkan penggunanya karena gangguan
// sesaat di sisi kami.
func TestAnUnanswerableRevocationCheckRefusesWithoutBlamingTheClient(t *testing.T) {
	s := newStack(t)
	token := s.registerUser(t, uniqueEmail())

	s.revocations.fail = true
	status, body := s.do(t, http.MethodGet, "/api/v1/me", token, nil)

	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503", status)
	}
	if body["code"] != "UNAVAILABLE" {
		t.Errorf("code = %v; want UNAVAILABLE", body["code"])
	}
}

// F1-10 lewat HTTP: minta reset, pakai tokennya, kata sandi lama mati.
func TestThePasswordResetFlowOverHTTP(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()
	s.registerUser(t, email)

	status, _ := s.do(t, http.MethodPost, "/api/v1/password-reset/request", "", map[string]string{
		"email": email,
	})
	if status != http.StatusAccepted {
		t.Fatalf("request status = %d; want 202", status)
	}
	if len(s.links.sent) != 1 {
		t.Fatalf("%d links sent; want 1", len(s.links.sent))
	}

	status, body := s.do(t, http.MethodPost, "/api/v1/password-reset/confirm", "", map[string]string{
		"token":                 s.links.sent[0].Expose(),
		"password":              "a-brand-new-password",
		"password_confirmation": "a-brand-new-password",
	})
	if status != http.StatusOK {
		t.Fatalf("confirm status = %d; want 200 (%v)", status, body)
	}

	if status, _ := s.do(t, http.MethodPost, "/api/v1/login", "", map[string]string{
		"email": email, "password": "a-brand-new-password",
	}); status != http.StatusOK {
		t.Errorf("the new password does not work: status = %d", status)
	}
	if status, _ := s.do(t, http.MethodPost, "/api/v1/login", "", map[string]string{
		"email": email, "password": "a-long-enough-password",
	}); status != http.StatusUnauthorized {
		t.Errorf("the old password still works: status = %d", status)
	}
}

// Meminta reset untuk alamat yang tidak terdaftar menjawab persis sama.
func TestRequestingAResetNeverRevealsWhetherTheAddressExists(t *testing.T) {
	s := newStack(t)

	known := uniqueEmail()
	s.registerUser(t, known)

	statusKnown, bodyKnown := s.do(t, http.MethodPost, "/api/v1/password-reset/request", "", map[string]string{
		"email": known,
	})
	statusUnknown, bodyUnknown := s.do(t, http.MethodPost, "/api/v1/password-reset/request", "", map[string]string{
		"email": "nobody-" + known,
	})

	if statusKnown != statusUnknown {
		t.Errorf("status differs: %d vs %d", statusKnown, statusUnknown)
	}
	if fmt.Sprint(bodyKnown) != fmt.Sprint(bodyUnknown) {
		t.Errorf("body differs:\n  %v\n  %v", bodyKnown, bodyUnknown)
	}
}

// Setiap kegagalan masuk menjawab sama, sampai ke badan jawabannya.
func TestEveryLoginFailureLooksIdenticalOverHTTP(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()
	s.registerUser(t, email)

	_, wrongPassword := s.do(t, http.MethodPost, "/api/v1/login", "", map[string]string{
		"email": email, "password": "the-wrong-password",
	})
	_, unknownEmail := s.do(t, http.MethodPost, "/api/v1/login", "", map[string]string{
		"email": "nobody-" + email, "password": "a-long-enough-password",
	})

	if fmt.Sprint(wrongPassword) != fmt.Sprint(unknownEmail) {
		t.Errorf("the two failures answer differently:\n  %v\n  %v", wrongPassword, unknownEmail)
	}
}

func TestValidationFailsWithFieldLevelErrors(t *testing.T) {
	s := newStack(t)

	status, body := s.do(t, http.MethodPost, "/api/v1/register", "", map[string]string{
		"email":                 "not-an-address",
		"password":              "short",
		"password_confirmation": "short",
	})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422 (%v)", status, body)
	}

	fields, ok := body["errors"].(map[string]any)
	if !ok {
		t.Fatalf("errors = %v; want an object", body["errors"])
	}
	for _, field := range []string{"email", "password"} {
		if _, present := fields[field]; !present {
			t.Errorf("no error reported for %q; got %v", field, fields)
		}
	}
}

// Bentuk galat 404 dan 405 harus sama seperti selebihnya. Bawaan Gin
// mengirim badan teks kosong, sehingga klien yang mengurai JSON justru gagal
// di jalur galat.
func TestUnknownRoutesAnswerInTheSameShape(t *testing.T) {
	s := newStack(t)

	status, body := s.do(t, http.MethodGet, "/api/v1/nothing-here", "", nil)
	if status != http.StatusNotFound {
		t.Errorf("status = %d; want 404", status)
	}
	if body["code"] != "NOT_FOUND" || body["success"] != false {
		t.Errorf("body = %v; want the standard error shape", body)
	}

	status, body = s.do(t, http.MethodDelete, "/api/v1/login", "", nil)
	if status != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405", status)
	}
	if body["success"] != false {
		t.Errorf("body = %v; want the standard error shape", body)
	}
}

func TestRegisteringTheSameAddressTwiceIsAConflict(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()
	s.registerUser(t, email)

	status, body := s.do(t, http.MethodPost, "/api/v1/register", "", map[string]string{
		"email": email, "password": "a-long-enough-password", "password_confirmation": "a-long-enough-password",
	})
	if status != http.StatusConflict {
		t.Errorf("status = %d; want 409 (%v)", status, body)
	}
}

// Kontrak menjanjikan email di profil dan di /me. Ia data identity, bukan
// demografis, jadi profile-svc tidak memilikinya - dan menanyakannya ke
// identity-svc di setiap request adalah persis yang dihapus ADR-007. Ia
// dibawa di klaim, sehingga kedua endpoint mengisinya tanpa memanggil siapa
// pun.
func TestTheEmailReachesTheClientWithoutAnExtraCall(t *testing.T) {
	s := newStack(t)
	email := uniqueEmail()
	token := s.registerUser(t, email)

	status, body := s.do(t, http.MethodGet, "/api/v1/profile", token, nil)
	if status != http.StatusOK {
		t.Fatalf("profile status = %d; want 200", status)
	}
	data, _ := body["data"].(map[string]any)
	if data["email"] != email {
		t.Errorf("profile email = %v; want %q", data["email"], email)
	}

	status, body = s.do(t, http.MethodGet, "/api/v1/me", token, nil)
	if status != http.StatusOK {
		t.Fatalf("me status = %d; want 200", status)
	}
	data, _ = body["data"].(map[string]any)
	if data["email"] != email {
		t.Errorf("me email = %v; want %q", data["email"], email)
	}
}
