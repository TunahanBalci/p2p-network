package main

import (
	"fmt"
	"os"

	"github.com/ipfs/go-log/v2"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env file
	_ = godotenv.Load()
	if tag := os.Getenv("DISCOVERY_SERVICE_TAG"); tag != "" {
		DiscoveryServiceTag = tag
	}

	log.SetAllLoggers(log.LevelError)

	// initialize p2p node
	node, err := NewP2PNode("User") 
	if err != nil {
		fmt.Printf("Error creating P2P node: %v\n", err)
		os.Exit(1)
	}
	defer node.Close()

	// ıpdate nickname stableID 
	node.Nick = fmt.Sprintf("User-%s", node.StableID[:8])

	// start p2p svc
	if err := node.Start(); err != nil {
		fmt.Printf("Error starting P2P services: %v\n", err)
		os.Exit(1)
	}

	//  join global + local channels
	if err := node.JoinChannel("global-chat", "", false); err != nil {
		fmt.Printf("Error joining global chat: %v\n", err)
		os.Exit(1)
	}
	if err := node.JoinChannel("local-chat", "", false); err != nil {
		fmt.Printf("Error joining local chat: %v\n", err)
		os.Exit(1)
	}

	gui := NewGUI(node)
	gui.Run()
}
