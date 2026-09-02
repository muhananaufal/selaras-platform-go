# Selaras Platform (Go)

Platform Go multi-service, hasil migrasi dari `selaras-backend-api` (Laravel 12).

**Status:** fase fondasi. Belum ada logika domain yang ditulis.

## Bentuk sistem

Sembilan unit deployable: satu gateway, tujuh service domain, satu worker.

```
edge-gateway    REST publik /api/v1, authn, authz, agregasi
identity-svc    users, access_tokens, password_reset_tokens
profile-svc     user_profiles  (hub yang di-FK seluruh domain)
assessment-svc  risk_assessments + mesin SCORE2 / SCORE2-OP / SCORE2-Diabetes
coaching-svc    program, week, task, thread, message
chat-svc        conversation, chat_message
nutrition-svc   culinary_preferences, daily_meal_guides
dashboard-svc   read-model yang dimaterialisasi dari event
llm-worker      seluruh panggilan Gemini, idempoten, versioned prompt
```

Antar unit memakai gRPC. Ke luar memakai REST sesuai kontrak OpenAPI yang sudah dipakai
frontend. Konsistensi lintas unit memakai transactional outbox di atas Apache Kafka (KRaft).

## Prinsip yang mengikat

| | |
| :--- | :--- |
| **Bangun seolah produksi** | "Belum ada pengguna" bukan alasan menurunkan standar rekayasa |
| **Jangan naikkan skala buatan** | Kompleksitas hanya sah bila ada masalah yang melahirkannya |
| **Domain bebas library** | `domain/` tidak mengimpor apa pun dari `adapter/`. Ini yang membuat pilihan library tetap murah dibalik |
| **Paritas dibuktikan, bukan diklaim** | Port SCORE2 diuji terhadap golden vector yang di-generate dari sistem lama |
| **Nol** | Nol `TODO`, nol kredensial hardcode, nol `interface{}` telanjang, nol error yang ditelan |

## Layout

```
cmd/<unit>/            entrypoint tiap unit
internal/<domain>/     domain · app · adapter
api/proto/             kontrak gRPC (sumber kebenaran)
api/openapi/           kontrak REST publik
migrations/<svc>/      migrasi per service, schema terpisah
deploy/                compose, helm, k8s
test/                  e2e, golden vector, k6
docs/                  RFC dan ADR
```

## Menjalankan

```bash
task --list          # seluruh perintah yang tersedia
task lint            # golangci-lint
task proto           # generate dari .proto
task test            # unit + integrasi
task up              # nyalakan dependensi lokal (profil core)
```

## Dokumen rencana

Rencana migrasi lengkap — master plan, 19 ADR beserta Evidence Ledger, backlog 186 task,
katalog temuan sistem lama, bill of materials, dan parking lot — ada di repo Laravel pada
`docs/migration-plan/`. RFC-000 dan seluruh ADR disalin ke `docs/` repo ini pada fase B1.
