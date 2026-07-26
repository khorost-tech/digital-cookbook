package main

import (
	"flag"
	"fmt"
	"log"
	"strings"
)

func main() {
	brokers := flag.String("brokers", "kafka1:9092,kafka2:9092,kafka3:9092",
		"comma-separated bootstrap servers (compose-net internal listeners)")
	scenario := flag.String("scenario", "all", "rebalance|strategies|static|commits|all")
	flag.Parse()
	seeds = strings.Split(*brokers, ",")

	ensureTopic(seeds)

	run := func(name string, fn func()) {
		fmt.Printf("\n================ сценарий: %s ================\n", name)
		startClock()
		fn()
	}

	switch *scenario {
	case "rebalance":
		run("rebalance (join/leave)", scenarioRebalance)
	case "strategies":
		run("strategies (range/roundrobin/sticky/cooperative-sticky)", scenarioStrategies)
	case "static":
		run("static membership", scenarioStatic)
	case "commits":
		run("commit offset (auto vs manual)", scenarioCommits)
	case "all":
		run("rebalance (join/leave)", scenarioRebalance)
		run("strategies (range/roundrobin/sticky/cooperative-sticky)", scenarioStrategies)
		run("static membership", scenarioStatic)
		run("commit offset (auto vs manual)", scenarioCommits)
	default:
		log.Fatalf("неизвестный сценарий: %s (ожидается rebalance|strategies|static|commits|all)", *scenario)
	}

	fmt.Println("\n[assert] все сценарии пройдены")
}
