# ADR-006 — Database: PostgreSQL, schema per service, satu instance


**Konteks.** Sistem sekarang **MySQL** satu database. Isolasi data adalah syarat agar unit bisa
dideploy sendiri, tetapi menjalankan 7 instance database di satu mesin tidak realistis (R4).

**Kenapa meninggalkan MySQL — perbandingan yang semula tidak pernah ditulis.** ADR ini sempat
memilih PostgreSQL tanpa satu kalimat pun tentang alasan meninggalkan MySQL. Itu lubang, dan
inilah pengisinya:

| Dimensi | MySQL (sekarang) | PostgreSQL | Menentukan? |
| :--- | :--- | :--- | :--- |
| Isolasi per service | Satu database per service, atau satu database bersama | **Schema terpisah dalam satu instance, dengan hak akses per schema** | **Ya.** Ini yang membuat aturan "dilarang query lintas schema" ditegakkan mesin database, bukan disiplin |
| Kolom JSON | Tipe `json` | `JSONB` — terindeks dan bisa di-query | **Ya.** `result_details`, `inputs`, `generated_values`, `guide_data`, `graduation_report` semuanya JSON, dan dashboard read-model membacanya |
| Ekosistem outbox dan CDC | Ada | Lebih banyak contoh dan tooling `[memori-model]` | Tidak menentukan |
| Biaya migrasi | Nol — tetap di tempat | Pemetaan tipe saat porting 16 migrasi | Ditanggung |

`[inferensi]` Bila kedua alasan penentu di atas tidak ada, tetap di MySQL adalah pilihan yang
lebih murah dan sah. Yang membalikkannya adalah kombinasi schema-per-service dan JSONB, bukan
selera.

**Opsi.** (a) satu instance Postgres, satu schema per service, kredensial terpisah ·
(b) satu instance database per service · (c) tetap satu database bersama.

**Keputusan.** (a) untuk lokal, dengan aturan keras: **dilarang query lintas schema**, dan tiap
service memakai kredensial yang hanya bisa melihat schema-nya sendiri.

**Konsekuensi.** Positif: isolasi ditegakkan oleh hak akses, bukan oleh disiplin; beban lokal
tetap masuk akal; pemisahan jadi instance terpisah nanti tinggal mengubah connection string.
Negatif: secara fisik masih satu titik kegagalan; godaan melakukan join lintas schema selalu ada;
bukan gambaran produksi yang sesungguhnya.

**Pembatal.** Kalau saat masuk cloud biaya instance terpisah ternyata dapat diterima, (b) lebih
jujur dan keputusan ini digantikan.

---

