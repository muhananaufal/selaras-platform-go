# Runbook — mutation testing (gremlins)

Coverage hanya mengukur baris mana yang *dijalankan* test. Mutation testing
mengukur apakah test *peduli* pada baris itu. Alat ini sengaja merusak kode,
misalnya `<` menjadi `<=`, `+` menjadi `-`, atau `*` menjadi `/`, lalu
menjalankan test:

- **KILLED**: ada test yang gagal. Bagus, test mendeteksi perubahan itu.
- **LIVED**: semua test tetap lulus. Artinya kode bisa diubah tanpa ada yang
  menyadarinya.
- **NOT COVERED**: tidak ada test yang menjalankan baris itu sama sekali.

## Gerbangnya

`test/mutation/run.sh` (job CI `mutation testing`) memutasi setiap paket di
`test/mutation/baseline.txt`, lalu menerapkan dua aturan:

- **Jumlah LIVED dan NOT COVERED tidak boleh naik** dari baseline. Kalau
  turun, skrip meminta baseline diturunkan supaya perbaikannya terkunci.
- **Run yang punya mutan TIMED OUT gagal** sebagai *inconclusive*. Mutan
  yang timeout tidak dihitung sebagai killed maupun lived, jadi hitungan
  dengan timeout bisa tampak lebih baik daripada kenyataannya.

Cakupan gerbang ini hanya **paket domain**, yang test-nya murni. Paket
integrasi sengaja dikeluarkan: tanpa database di job itu, setiap test
gagal, dan gremlins akan menghitung setiap mutan sebagai "killed". Efikasi
yang dihasilkan tinggi tetapi palsu.

Mode yang dipakai adalah **mode normal**: setiap paket dinilai hanya oleh
test-nya sendiri.

## Keputusan yang diambil dari pengukuran

| Pengukuran | Keputusan |
| :--- | :--- |
| `--threshold-efficacy 99` pada paket dengan efikasi 95,24%: **exit 0**, tanpa pesan. Nilai yang sama lewat variabel lingkungan juga diabaikan. Lewat berkas konfigurasi: **exit 10**, `below efficacy-threshold` | Ambang bawaan gremlins **tidak dipakai lewat flag**. Gerbang yang tidak pernah bisa merah adalah gerbang palsu. Batasnya ditegakkan sendiri oleh `run.sh` |
| Paket yang sama di tiga run berturut-turut: 21 timeout (efikasi 0%), lalu dua kali 95,24% tanpa timeout. Dengan `GOCACHE` baru pun tanpa timeout, jadi dugaan "cache dingin" **tidak terbukti** | Dengan batas waktu bawaan, hasilnya tidak stabil |
| `nutrition/domain`: `timeout-coefficient` bawaan menghasilkan 10 timeout (efikasi 82,50%); koefisien 10 menghasilkan 0 timeout (86,00%), dengan durasi yang praktis sama | `.gremlins.yaml` memakai `timeout-coefficient: 10`, dan run yang masih timeout ditolak |
| Baseline pertama tercemar timeout (misalnya `profile/domain`: 0 killed karena 30 mutan timeout) | Baseline diukur ulang dengan konfigurasi final; semua paket tanpa timeout |

## Baseline (2026-09-26, konfigurasi final, tanpa timeout)

| Paket | Killed | Lived | Not covered |
| :--- | ---: | ---: | ---: |
| `assessment/domain/score` (mesin risiko SCORE2) | 194 | **21** | 20 |
| `assessment/domain` | 0 | 0 | **244** |
| `coaching/domain` | 43 | 6 | 14 |
| `chat/domain` | 26 | 7 | 2 |
| `dashboard/domain` | 20 | 1 | 9 |
| `identity/domain` | 44 | 3 | 3 |
| `nutrition/domain` | 43 | 7 | 14 |
| `profile/domain` | 30 | 0 | 2 |

## Yang ditemukan, dan belum diperbaiki

- **Paritas mesin risiko hanya untuk perokok.** Semua 288 golden vector
  adalah perokok aktif dengan satu set nilai lab per mode. Mutan
  `coef.Smoking*smoking` → `coef.Smoking/smoking`, yaitu pembagian dengan
  nol untuk bukan perokok, lolos seluruh test. Rinciannya ada di
  [`docs/parity-report.md`](../parity-report.md). Memperbaikinya butuh vektor
  baru dari sistem lama sebagai oracle, dan itu berarti mengubah repo
  `selaras-backend-api`, yang merupakan keputusan pemilik.
- **`assessment/domain` tidak punya test sendiri.** 244 mutan tanpa cakupan
  di paketnya. Logikanya diuji tidak langsung lewat `assessment/app`
  (cakupan pernyataan 78,9% dari sana), dan mode normal tidak menghitung itu.
  Aturan domain yang hanya teruji lewat use case bisa berubah tanpa test
  domain yang gagal.
- LIVED lain per paket tercetak lengkap di log job. Masing-masing perlu
  dinilai satu per satu: celah test yang nyata, atau mutan ekuivalen
  (perubahan yang memang tidak mengubah perilaku).

## Menjalankan

```sh
go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0
bash test/mutation/run.sh 4          # 4 worker; butuh jq
```

Di Windows, gremlins kadang gagal menghapus folder sementaranya karena
berkas masih dikunci proses lain. Itu tidak memengaruhi hasil. Runner CI
berbasis Linux.
