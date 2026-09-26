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

## Baseline awal (2026-09-26, runner CI Linux, tanpa timeout)

Angka terkini ada di `test/mutation/baseline.txt`; lihat juga "Mutan yang ditutup" di bawah.

Satu path adalah satu **pohon direktori**: gremlins memutasi setiap paket di
bawahnya. Karena itu `assessment/domain` mencakup subpaket `score`.

| Pohon | Killed | Lived | Not covered |
| :--- | ---: | ---: | ---: |
| `assessment/domain` (termasuk `score`, mesin risiko SCORE2) | 194 | **21** | 29 (20 di `score`, 9 di berkas domain assessment) |
| `coaching/domain` | 43 | 6 | 14 |
| `chat/domain` | 26 | 7 | 2 |
| `dashboard/domain` | 20 | 1 | 9 |
| `identity/domain` | 44 | 3 | 3 |
| `nutrition/domain` | 43 | 7 | 14 |
| `profile/domain` | 30 | 0 | 2 |

## Mutan yang ditutup (2026-09-26)

Setiap mutan LIVED di luar mesin risiko dianalisis satu per satu. Sebagian
besar adalah batas yang hanya diuji jauh melewati garis: panjang judul,
pesan, masakan, alergi, jumlah tag, panjang tag, ukuran halaman 1, pekan 1,
dan selisih tepat di deadband tren. Setiap mutan ditutup dengan test yang
**lulus dengan kode asli** dan **gagal saat mutan itu dipasang manual**.

| Pohon | Lived sebelum → sesudah |
| :--- | :--- |
| `dashboard/domain` | 1 → 0 |
| `identity/domain` | 3 → 2 |
| `chat/domain` | 7 → 2 |
| `nutrition/domain` | 7 → 2 |
| `coaching/domain` | 6 → 1 |

Dua di antaranya mengungkap hal yang layak dicatat:

- **Test deadband dashboard tidak pernah menyentuh batasnya.** Kasus "just
  inside" memakai 12,6 − 12,5, yang dalam float bernilai 0,0999...96 dan
  bukan 0,1. Test batas yang benar memakai selisih yang tepat 0,1 (0,1 − 0).
- **Test batas pekan yang saya tulis di PR #19 tidak pernah memvalidasi pekan
  1 sendirian.** Kasus yang memuat pekan 1 sudah gagal di pekan keduanya.

### Mutan ekuivalen yang tersisa, dan alasannya

Mutan ekuivalen tidak mengubah perilaku, jadi tidak ada test yang bisa, atau
perlu, membunuhnya. Semuanya diperiksa manual:

| Mutan | Kenapa ekuivalen |
| :--- | :--- |
| `identity/domain/email.go:40` (2 mutan pada `len(v)-1`) | Cek "@ di akhir" berlebih: baris 43 (domain harus memuat titik) sudah menolak domain kosong. Cek eksplisitnya dipertahankan karena mendokumentasikan maksud |
| `chat/domain/repository.go:20` dan `nutrition/domain/repository.go:21` (`Number < 1` → `<= 1`) | Nomor 1 dipetakan ke 1 oleh kedua versi |
| `chat/domain/repository.go:26` dan `nutrition/domain/repository.go:27` (`Size > 100` → `>= 100`) | Ukuran 100 dipetakan ke 100 oleh kedua versi |
| `coaching/domain/program.go:318` (`MaxWeeks*7` di argumen `Errorf`) | Hanya mengubah teks pesan error, bukan keputusan |

## Yang ditemukan, dan belum diperbaiki

- **Paritas mesin risiko hanya untuk perokok.** Semua 288 golden vector
  adalah perokok aktif dengan satu set nilai lab per mode. Mutan
  `coef.Smoking*smoking` → `coef.Smoking/smoking`, yaitu pembagian dengan
  nol untuk bukan perokok, lolos seluruh test. Rinciannya ada di
  [`docs/parity-report.md`](../parity-report.md). Memperbaikinya butuh vektor
  baru dari sistem lama sebagai oracle, dan itu berarti mengubah repo
  `selaras-backend-api`, yang merupakan keputusan pemilik.
- **Berkas domain assessment (di luar `score`) tidak punya test sendiri.** 9
  mutan tanpa cakupan. Logikanya diuji tidak langsung lewat `assessment/app`
  (cakupan pernyataan 78,9% dari sana), dan mode normal tidak menghitung itu.

  **Koreksi:** laporan pertama di runbook ini menyebut **244**. Angka itu
  berasal dari run di Windows, yang kehilangan cakupan paket bersarang dan
  melaporkan semua mutan pohon ini sebagai NOT COVERED (194 + 21 + 20 + 9 =
  244). Runner CI Linux menunjukkan angka sebenarnya. Angka lokal Windows
  tidak dipakai sebagai rujukan.
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
