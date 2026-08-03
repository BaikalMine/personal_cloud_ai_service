package main

import (
	"log"

	"ai-access-gateway/internal/gateway"
)

func main() {
	if err := gateway.Run(); err != nil {
		log.Fatal(err)
	}
}
