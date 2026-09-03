// Command topics membuat topic platform, lalu membacanya kembali.
//
// Ia bisa dijalankan berkali-kali: topic yang sudah ada dibiarkan apa adanya.
// Yang dicetak di akhir adalah apa yang benar-benar ada di broker, bukan apa
// yang barusan diminta - keduanya tidak selalu sama.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
)

func main() {
	brokers := flag.String("brokers", os.Getenv("KAFKA_BROKERS"),
		"comma-separated broker addresses")
	replicas := flag.Int("replicas", 1,
		"replication factor; 1 is for local development only")
	flag.Parse()

	if err := run(*brokers, int16(*replicas)); err != nil {
		fmt.Fprintf(os.Stderr, "topics: %v\n", err)
		os.Exit(1)
	}
}

func run(brokers string, replicas int16) error {
	client, err := kafka.NewProducer(kafka.Config{Brokers: brokers, ClientID: "topics"})
	if err != nil {
		return err
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := kafka.Ping(ctx, client); err != nil {
		return err
	}

	wanted := kafka.Topics()
	created, err := kafka.EnsureTopics(ctx, client, wanted, replicas)
	if err != nil {
		return err
	}
	for _, name := range created {
		fmt.Printf("created %s\n", name)
	}

	// Dibaca kembali dari broker, lalu dicocokkan dengan yang diminta.
	found, err := kafka.WaitForTopics(ctx, client, wanted)
	if err != nil {
		// Dilaporkan, tetapi tidak langsung keluar: laporan per topic di
		// bawah yang memberi tahu topic mana yang tidak pernah muncul, dan
		// itu justru yang dibutuhkan saat ini gagal.
		fmt.Fprintf(os.Stderr, "topics: %v\n", err)
	}

	sort.Slice(wanted, func(i, j int) bool { return wanted[i].Name < wanted[j].Name })
	var wrong int
	for _, t := range wanted {
		got, ok := found[t.Name]
		switch {
		case !ok:
			fmt.Printf("MISSING  %-24s\n", t.Name)
			wrong++
		case got != int(t.Partitions):
			fmt.Printf("MISMATCH %-24s has %d partitions, wanted %d\n", t.Name, got, t.Partitions)
			wrong++
		default:
			fmt.Printf("ok       %-24s %2d partitions\n", t.Name, got)
		}
	}

	if wrong > 0 {
		return fmt.Errorf("%d topic(s) are not what the platform expects", wrong)
	}
	return nil
}
