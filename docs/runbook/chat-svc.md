# Runbook on-call — chat-svc

Pemilik percakapan asisten umum dan pesannya. Tidak memanggil unit lain
secara sinkron. Setiap pesan pengguna mengantre `chat.reply.requested` lewat
outbox; balasannya datang dari llm-worker lewat `llm.results` (aggregate
`conversation`) dan disimpan sebagai pesan model. Bergantung keras pada
Postgres (skema `chat`).

## Siapa yang terdampak bila ia mati

- **Mati**: seluruh `/chat/*`.
- **Bertahan**: semua unit lain; balasan yang tiba saat ia mati menunggu di
  topic.

## Gejala dan cara membacanya

| Gejala | Yang hampir pasti terjadi | Periksa |
| :--- | :--- | :--- |
| Pesan terkirim (202) tetapi balasan tidak pernah muncul | llm-worker mati / job `dead` | `llm.llm_jobs` menurut `aggregate_id` = id percakapan; runbook llm-worker |
| Balasan tidak muncul dan log `a chat reply was not usable` | model menjawab di luar bentuk `{"text": ...}` | template `chat_reply.v1`; balasan yang tidak bisa dibaca TIDAK disimpan (agar JSON mentah tidak tampil ke pengguna) |
| Kegagalan AI tidak menghasilkan pesan galat di riwayat | disengaja (D9): kegagalan dijawab ramah oleh pemanggil, bukan disimpan sebagai balasan model | tidak ada tindakan |
| Konsumen berputar `holding offsets so failed replies are redelivered` | balasan untuk percakapan yang SUDAH DIHAPUS — **sudah diperbaiki** (B22, FK dinamai `ErrConversationNotFound`); bila muncul lagi, ada galat baru yang dianggap sementara | log galatnya |
| Konteks jawaban "lupa" pesan lama | jendela konteks 20 pesan (D8) — bukan bug | `domain.ContextWindow` |
| Judul percakapan aneh | dibuat otomatis dari 45 karakter pertama pesan pertama bila tidak diberi (D12) | bukan bug |

## Triase dalam lima menit

1. `curl :9503/readyz`.
2. Grafana → gRPC `chat.v1.Chat/*` dan lag llm-worker.
3. `trace_id` dari log → Tempo: trace balasan lengkap = `SendMessage` →
   `llm.jobs process` → `llm.generate` → `llm.results process` (chat-svc).
4. `SELECT count(*) FROM chat.outbox WHERE published_at IS NULL` untuk
   memisahkan "belum terkirim" dari "belum dijawab".

## Cara pulih

- Nyalakan lagi; konsumen melanjutkan dari offset.
- Balasan yang hilang karena job `dead`: pengguna mengirim pesan lagi. Tidak
  ada antrean ulang server-side, dengan sengaja — pesan yang sama akan gagal
  dengan cara yang sama.

## Yang JANGAN dilakukan

- Jangan menyimpan pesan galat sebagai balasan model "supaya pengguna tahu".
  Itu mengubah riwayat percakapan menjadi log sistem, dan D9 memutuskan
  sebaliknya.

## Metrik yang membuktikan pulih

`rpc_server_call_duration_seconds{rpc_method=~"chat.*"}` OK; lag llm-worker
nol; `chat.outbox` tanpa baris tertunda.
