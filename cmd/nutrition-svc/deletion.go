package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	unitdeletion "github.com/muhananaufal/selaras-platform-go/internal/nutrition/adapter/deletion"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/deletion"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
)

// DeletionGroup tetap, dan berbeda dari group konsumen lain di service ini.
//
// Group yang dipakai bersama akan membuat kedua konsumen berebut partisi yang
// sama: satu pesan hanya sampai ke salah satunya, dan yang tidak menerimanya
// tidak akan pernah tahu ada yang harus dikerjakan.
const DeletionGroup = "nutrition-deletion"

// startDeletionConsumer menyalakan sisi unit dari saga penghapusan akun.
//
// Tanpa KAFKA_BROKERS ia tidak dinyalakan, dan itu dinyatakan di log: tanpa
// konsumen ini, setiap permintaan penghapusan akan menggantung menunggu unit
// yang tidak pernah mendengarnya.
func startDeletionConsumer(
	ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, brokers string,
) (func(), error) {
	if brokers == "" {
		log.Warn("KAFKA_BROKERS is not set; account deletions will hang waiting for this unit",
			"service", unitdeletion.Service)
		return func() {}, nil
	}

	client, err := kafka.NewConsumer(
		kafka.Config{Brokers: brokers, ClientID: unitdeletion.Service + "-deletion"},
		DeletionGroup, outbox.TopicUserDeletion)
	if err != nil {
		return nil, err
	}

	consumer, err := deletion.NewConsumer(
		client, pool, unitdeletion.Service, unitdeletion.Erase, log)
	if err != nil {
		client.Close()
		return nil, err
	}

	go func() {
		if err := consumer.Run(ctx); err != nil {
			log.Error("the deletion consumer stopped",
				"service", unitdeletion.Service, "error", err)
		}
	}()

	return client.Close, nil
}
