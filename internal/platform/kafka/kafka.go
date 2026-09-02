// Package kafka membungkus franz-go menjadi satu cara menyambung ke broker.
//
// Alasannya bukan kerapian. Setiap service yang menyusun kliennya sendiri akan
// menyusunnya sedikit berbeda, dan perbedaan yang paling mahal - acks, idempoten,
// ukuran batch - adalah perbedaan yang tidak terlihat sampai ada pesan yang
// hilang di produksi.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Config adalah yang dibutuhkan untuk menyambung.
type Config struct {
	// Brokers dipisah koma, mengikuti bentuk yang lazim di variabel lingkungan.
	Brokers string

	// ClientID muncul di log broker. Ia dipakai untuk mengetahui service mana
	// yang menghasilkan beban - tanpa itu, semua klien terlihat sama.
	ClientID string
}

// NewProducer membuka klien yang hanya menerbitkan.
//
// Tiga pilihannya disengaja dan tidak boleh dilonggarkan tanpa alasan:
//
//   - RequiredAcks(AllISRAcks): broker baru mengakui setelah seluruh replika
//     yang tersinkron menyimpannya. Dengan acks=1, pesan yang sudah diakui bisa
//     hilang saat leader-nya jatuh sebelum replikanya menyusul.
//   - Idempotent (bawaan franz-go): percobaan ulang di dalam klien tidak
//     menggandakan pesan. Tanpa ini, retry yang sehat menjadi duplikat.
//   - ProducerLinger: menahan sebentar supaya pesan berkumpul menjadi batch.
//     Nol berarti satu permintaan jaringan per pesan.
func NewProducer(cfg Config) (*kgo.Client, error) {
	brokers, err := parseBrokers(cfg.Brokers)
	if err != nil {
		return nil, err
	}
	if cfg.ClientID == "" {
		return nil, errors.New("a kafka client needs an id so its load can be attributed")
	}

	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(cfg.ClientID),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(5*time.Millisecond),
		kgo.RetryTimeout(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("opening a kafka producer: %w", err)
	}
	return client, nil
}

// NewConsumer membuka klien yang tergabung dalam sebuah group.
//
// DisableAutoCommit dipasang dengan sengaja. Auto-commit menandai pesan sudah
// diproses berdasarkan waktu, bukan berdasarkan hasil: pekerjaan yang gagal di
// tengah jalan tetap tercatat selesai, dan pesannya tidak pernah datang lagi.
// Offset di sini dikomit oleh pemanggil, setelah pekerjaannya benar-benar
// selesai.
func NewConsumer(cfg Config, group string, topics ...string) (*kgo.Client, error) {
	brokers, err := parseBrokers(cfg.Brokers)
	if err != nil {
		return nil, err
	}
	if cfg.ClientID == "" {
		return nil, errors.New("a kafka client needs an id so its load can be attributed")
	}
	if group == "" {
		return nil, errors.New("a consumer needs a group so its offsets are remembered")
	}
	if len(topics) == 0 {
		return nil, errors.New("a consumer with no topics would sit idle forever")
	}

	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(cfg.ClientID),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topics...),
		kgo.DisableAutoCommit(),

		// Group baru mulai dari awal topic, bukan dari ujungnya. Yang kedua
		// membuat consumer yang baru dipasang melewatkan seluruh pekerjaan
		// yang sudah menunggu di sana.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		return nil, fmt.Errorf("opening a kafka consumer: %w", err)
	}
	return client, nil
}

// Ping memastikan brokernya benar-benar terjangkau.
//
// kgo.NewClient tidak menyambung; ia hanya menyiapkan. Tanpa ping, service akan
// melapor sehat saat start dan baru gagal pada pesan pertama - jauh setelah
// orang yang menjalankannya berhenti memperhatikan.
func Ping(ctx context.Context, client *kgo.Client) error {
	if err := client.Ping(ctx); err != nil {
		return fmt.Errorf("the kafka broker did not answer: %w", err)
	}
	return nil
}

func parseBrokers(raw string) ([]string, error) {
	var out []string
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no kafka brokers were configured")
	}
	return out, nil
}
