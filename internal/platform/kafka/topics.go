package kafka

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Topic is one topic together with the reasoning for its partition count.
//
// The reasoning lives here, not only in the documents, because the partition
// count is the upper bound on consumer parallelism (ADR-014 rule 1) and raising
// it later changes the key-to-partition mapping - meaning per-key ordering
// already in flight breaks at the point of change.
type Topic struct {
	Name       string
	Partitions int32
	Why        string
}

// Topics is every platform topic.
//
// It is the single list: the topic-creation tool and the documentation read
// the same one, so the documentation cannot drift from what was actually
// created.
func Topics() []Topic {
	return []Topic{
		{
			Name:       "profile.updated",
			Partitions: 3,
			Why: "Konsumennya penulis cache (F2-16) - pekerjaan pendek, terikat basis data. " +
				"Tiga cukup untuk memisahkan pengguna yang sibuk dari yang lain tanpa " +
				"menyebar beban yang belum ada.",
		},
		{
			Name:       "assessment.completed",
			Partitions: 3,
			Why: "Pemicu personalisasi. Lajunya terikat pada berapa banyak penilaian yang " +
				"diselesaikan orang, bukan pada kecepatan mesin - dan itu angka yang kecil.",
		},
		{
			Name:       "coaching.program.updated",
			Partitions: 3,
			Why: "Perubahan program coaching. Selajur dengan profile.updated - dibaca " +
				"pembaca yang sama dan lajunya ditentukan orang, bukan mesin.",
		},
		{
			Name:       "llm.jobs",
			Partitions: 12,
			Why: "Satu-satunya topic yang butuh paralelisme sungguhan. Pekerjaannya menunggu " +
				"jaringan selama puluhan detik, jadi jumlah partisi menentukan berapa banyak " +
				"pekerjaan yang boleh menunggu bersamaan. Dua belas adalah plafon " +
				"maxReplicaCount llm-worker di F9-22; menaikkannya nanti mudah, " +
				"menurunkannya tidak.",
		},
		{
			Name:       "llm.results",
			Partitions: 12,
			Why: "Dipasangkan dengan llm.jobs supaya satu worker bisa memegang partisi yang " +
				"bersesuaian di keduanya. Jumlah yang berbeda akan membuat hasil sebuah job " +
				"mendarat di partisi yang dipegang worker lain.",
		},
		{
			Name:       "llm.dlq",
			Partitions: 1,
			Why: "Antrean surat mati (F3-13). Ia dibaca manusia saat menyelidiki, bukan oleh " +
				"armada konsumen. Satu partisi menjaga urutannya utuh - dan kalau ia sampai " +
				"butuh lebih, yang salah bukan jumlah partisinya.",
		},
		{
			Name:       "user.deletion",
			Partitions: 1,
			Why: "Penghapusan akun harus berurutan terhadap dirinya sendiri dan jarang terjadi. " +
				"Paralelisme di sini hanya menambah cara untuk salah.",
		},
	}
}

// EnsureTopics creates the topics that do not exist yet.
//
// Topics that already exist are LEFT ALONE, neither altered nor deleted.
// Raising the partition count of a topic that holds data changes the
// key-to-partition mapping: messages for the same key suddenly land somewhere
// else, and ordering that has held so far breaks without a single error.
func EnsureTopics(ctx context.Context, client *kgo.Client, topics []Topic, replicas int16) ([]string, error) {
	if len(topics) == 0 {
		return nil, errors.New("no topics were given")
	}
	if replicas < 1 {
		return nil, errors.New("a topic needs at least one replica")
	}

	admin := kadm.NewClient(client)

	var created []string
	for _, t := range topics {
		// kadm.CreateTopic returns the broker's refusal through err, not only
		// through resp.Err [kadm@v1.18.0/topics.go:139]. Checking resp.Err alone
		// makes the "already exists" branch unreachable, and this tool fails on
		// its second run - exactly what happened before this line was written.
		_, err := admin.CreateTopic(ctx, t.Partitions, replicas, nil, t.Name)
		switch {
		case err == nil:
			created = append(created, t.Name)
		case errors.Is(err, kerr.TopicAlreadyExists):
			// The correct state, not a failure: this tool must be runnable
			// repeatedly.
		default:
			return created, fmt.Errorf("creating %q: %w", t.Name, err)
		}
	}
	return created, nil
}

// DescribeTopics reads back what actually exists on the broker.
//
// It is used to prove, not to assume: creating topics and reporting success
// without reading them back would hide a broker that accepted the request and
// then quietly created something else.
func DescribeTopics(ctx context.Context, client *kgo.Client) (map[string]int, error) {
	admin := kadm.NewClient(client)

	details, err := admin.ListTopics(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing topics: %w", err)
	}

	out := make(map[string]int, len(details))
	for name, d := range details {
		if d.Err != nil {
			return nil, fmt.Errorf("the broker reported %q as broken: %w", name, d.Err)
		}
		out[name] = len(d.Partitions)
	}
	return out, nil
}

// WaitForTopics waits until the broker actually announces the topics.
//
// A successful CreateTopic does NOT mean the topic shows up in metadata right
// away: the broker propagates it asynchronously, and a read made immediately
// afterwards reports a topic that was just created successfully as missing.
// This really happened here once - four of six topics were "missing" on the
// first read.
//
// Waiting here is more honest than loosening the check, because what is being
// proven stays the same: the topic exists, with the requested number of
// partitions.
func WaitForTopics(ctx context.Context, client *kgo.Client, topics []Topic) (map[string]int, error) {
	const interval = 250 * time.Millisecond

	var last map[string]int
	for {
		found, err := DescribeTopics(ctx, client)
		if err != nil {
			return nil, err
		}
		last = found

		settled := true
		for _, t := range topics {
			if got, ok := found[t.Name]; !ok || got != int(t.Partitions) {
				settled = false
				break
			}
		}
		if settled {
			return found, nil
		}

		select {
		case <-ctx.Done():
			// Deadline exhausted. The last thing read is still returned so the
			// caller can name which topics never appeared.
			return last, fmt.Errorf("the broker never announced every topic: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

// DeleteTopics deletes topics. Used by TESTS to clean up their own.
//
// It exists because the test harness creates one topic per test - which is
// right, since a shared topic makes tests inherit each other's messages - but
// without deletion every run leaves its leftovers on the broker. Two hundred
// and fifty orphan topics piled up in the development environment during this
// session before anyone noticed, and the broker's metadata carried all of
// them.
//
// A failure to delete does NOT fail the test: cleanup that fails green tests
// makes people switch the cleanup off.
func DeleteTopics(ctx context.Context, client *kgo.Client, names ...string) error {
	if len(names) == 0 {
		return nil
	}

	responses, err := kadm.NewClient(client).DeleteTopics(ctx, names...)
	if err != nil {
		return fmt.Errorf("deleting topics: %w", err)
	}

	// Just like CreateTopic, per-topic refusals are inside the response, not
	// only in err.
	for _, r := range responses {
		if r.Err != nil && !errors.Is(r.Err, kerr.UnknownTopicOrPartition) {
			return fmt.Errorf("deleting topic %s: %w", r.Topic, r.Err)
		}
	}
	return nil
}
