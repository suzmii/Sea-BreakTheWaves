package infrastructure

import (
	"context"
	"fmt"
	"time"

	"recommendation_v2/config"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"trpc.group/trpc-go/trpc-agent-go/log"
)

var neo4jDriver neo4j.DriverWithContext

func Neo4j() neo4j.DriverWithContext {
	return neo4jDriver
}

func InitNeo4j() error {
	if config.Cfg.Neo4j.Address == "" {
		log.Warn("[infra] neo4j address empty, skip")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	d, err := neo4j.NewDriverWithContext(
		config.Cfg.Neo4j.Address,
		neo4j.BasicAuth(config.Cfg.Neo4j.Username, config.Cfg.Neo4j.Password, ""),
	)
	if err != nil {
		return fmt.Errorf("neo4j driver: %w", err)
	}
	if err := d.VerifyConnectivity(ctx); err != nil {
		d.Close(ctx)
		return fmt.Errorf("neo4j verify: %w", err)
	}
	neo4jDriver = d
	log.Info("[infra] neo4j initialized")
	return nil
}

func CloseNeo4j(ctx context.Context) {
	if neo4jDriver != nil {
		neo4jDriver.Close(ctx)
	}
}
