package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/sonm-io/core/insonmnia/auth"
	"github.com/sonm-io/core/proto"
	"github.com/sonm-io/core/util"
	"github.com/sonm-io/core/util/xgrpc"
)

var (
	endpointFlag      string
	peerAddrFlag      string
	expectedCountFlag uint
	debugLogPath      string
)

func init() {
	flag.StringVar(&endpointFlag, "endpoint", "", "relay monitoring endpoint")
	flag.StringVar(&peerAddrFlag, "peer", "0x181b6f75B00e79382aa32D81c7734a46E9F9aF40", "relay peer address")
	flag.UintVar(&expectedCountFlag, "count", 0, "how many members expect to see in the cluster")
	flag.StringVar(&debugLogPath, "debugLog", "/tmp/relay_mon.log", "file to write debug info")
	flag.Parse()
}

func main() {
	if len(endpointFlag) == 0 {
		fmt.Fprintln(os.Stderr, "host list is empty, exiting")
		os.Exit(1)
	}

	if expectedCountFlag == 0 {
		fmt.Fprintln(os.Stderr, "expected count cannot be zero")
		os.Exit(1)
	}

	// init logger
	logFile, err := os.OpenFile(debugLogPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open file for wrinig: %v\n", err)
		os.Exit(1)
	}

	defer logFile.Close()
	log.SetOutput(logFile)

	key, err := crypto.GenerateKey()
	if err != nil {
		log.Printf("cannot generate key: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, TLSConfig, err := util.NewHitlessCertRotator(ctx, key)
	if err != nil {
		log.Printf("cannot create TLS config: %v\n", err)
		os.Exit(1)
	}

	creds := auth.NewWalletAuthenticator(util.NewTLS(TLSConfig), common.HexToAddress(peerAddrFlag))
	client, err := xgrpc.NewClient(ctx, endpointFlag, creds)
	if err != nil {
		log.Printf("cannot create client connection: %v\n", err)
		os.Exit(1)
	}

	relay := sonm.NewRelayClient(client)
	clusterChan := make(chan *sonm.RelayClusterReply)
	metricsChan := make(chan *sonm.RelayMetrics)

	go func() {
		cluster, err := relay.Cluster(ctx, &sonm.Empty{})
		if err != nil {
			log.Printf("cannot query cluster members: %v\n", err)
			os.Exit(1)
		}

		clusterChan <- cluster
	}()

	go func() {
		metrics, err := relay.Metrics(ctx, &sonm.Empty{})
		if err != nil {
			log.Printf("cannot query metrics: %v\n", err)
			os.Exit(1)
		}

		metricsChan <- metrics
	}()

	cluster := <-clusterChan
	metrics := <-metricsChan

	// calculate metrics
	members := len(cluster.GetMembers())
	membersDiff := uint(members) - expectedCountFlag
	iponly := strings.Replace(strings.Split(endpointFlag, ":")[0], ".", "_", 4)
	connCount := metrics.GetConnCurrent()

	// show metrics to telegraf collector
	fmt.Printf("relay_%s_members count=%d,expect=%d,diff=%d,conn_count=%d\n",
		iponly, members, expectedCountFlag, membersDiff, connCount)
}
