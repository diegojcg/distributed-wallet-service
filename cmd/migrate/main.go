package main

import (
	"context"
	"github.com/jackc/pgx/v5"
	"jungle-wallet-service/migrations"
	"log"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: migrate up|down")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("database connection failed")
	}
	defer conn.Close(ctx)
	if err = migrations.Apply(ctx, conn, os.Args[1]); err != nil {
		log.Fatal(err)
	}
	log.Print("migration " + os.Args[1] + " complete")
}
